package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/config"
)

type fakeTokens struct{}

func (fakeTokens) Verify(_ context.Context, pt string) (Principal, error) {
	if pt == "mmp_good" {
		return Principal{Kind: KindAgent, ID: "t1", Name: "t", Scopes: []Scope{ScopeRead}, Via: "token"}, nil
	}
	return Principal{}, ErrInvalidToken
}

type fakeSessions struct{}

func (fakeSessions) VerifySession(_ context.Context, v string) (Principal, error) {
	if v == "goodsess" {
		return Principal{Kind: KindHuman, ID: "alice", Name: "alice", Scopes: allScopes, Via: "local"}, nil
	}
	return Principal{}, errors.New("no")
}

const edge = "edgesecret"

func cfg(mode string) (config.Config, config.Secrets) {
	return config.Config{
			AuthMode:       mode,
			PublicURL:      "https://mm.example.com",
			TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
			TrustedUsers:   []string{"alice"},
		},
		config.Secrets{Key: key, EdgeSecret: edge}
}

// run executes the middleware and returns the response plus the principal
// kind seen by the inner handler ("" if the handler was not reached).
func run(mode string, r *http.Request) (*httptest.ResponseRecorder, Kind) {
	c, s := cfg(mode)
	var seen Kind
	h := Middleware(c, s, fakeTokens{}, fakeSessions{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = FromContext(r.Context()).Kind
		w.WriteHeader(200)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec, seen
}

func TestTrustMatrix(t *testing.T) {
	paths := map[string]PathClass{"/healthz": ClassPublic, "/mcp": ClassMachine, "/api/x": ClassUI, "/": ClassUI}
	authz := []string{"", "Bearer mmp_good", "Bearer mmp_bad", "Basic abc", "mmp_good"}
	peers := []string{"10.1.2.3:5555", "192.0.2.1:5555"}
	edges := [][]string{nil, {"wrong"}, {edge}, {edge, edge}}
	users := [][]string{nil, {"alice"}, {"mallory"}, {"alice", "alice"}, {"alice,mallory"}}
	modes := []string{"proxy", "local"}
	n := 0
	for path, class := range paths {
		for _, az := range authz {
			for _, peer := range peers {
				for _, eh := range edges {
					for _, uh := range users {
						for _, mode := range modes {
							for _, cookie := range []bool{false, true} {
								n++
								name := fmt.Sprintf("%s|az=%q|%s|edge=%v|user=%v|%s|cookie=%v", path, az, peer, eh, uh, mode, cookie)
								r := httptest.NewRequest("GET", path, nil)
								r.RemoteAddr = peer
								if az != "" {
									r.Header.Set("Authorization", az)
								}
								for _, v := range eh {
									r.Header.Add("X-MM-Edge", v)
								}
								for _, v := range uh {
									r.Header.Add("Remote-User", v)
								}
								if cookie {
									r.AddCookie(&http.Cookie{Name: "mm_session", Value: "goodsess"})
								}
								rec, seen := run(mode, r)

								// expected, straight from the C3 table
								var wantKind Kind
								wantStatus := 200
								switch {
								case az != "":
									if az == "Bearer mmp_good" {
										wantKind = KindAgent
									} else {
										wantStatus = 401
									}
								case class == ClassPublic || class == ClassMachine:
									wantKind = KindAnonymous
								case mode == "proxy":
									if strings.HasPrefix(peer, "10.") && len(eh) == 1 && eh[0] == edge && len(uh) == 1 && uh[0] == "alice" {
										wantKind = KindHuman
									} else {
										wantKind = KindAnonymous
									}
								default:
									if cookie {
										wantKind = KindHuman
									} else {
										wantKind = KindAnonymous
									}
								}
								if wantKind == KindAnonymous && class == ClassUI {
									// anonymous on UI is refused before the handler
									if path == "/api/x" {
										wantStatus = 401
									} else if mode == "local" {
										wantStatus = 303
									} else {
										wantStatus = 401
									}
									wantKind = ""
								}
								if wantStatus == 401 && az != "" {
									wantKind = ""
								}
								if rec.Code != wantStatus || seen != wantKind {
									t.Fatalf("%s: got status=%d kind=%q, want status=%d kind=%q", name, rec.Code, seen, wantStatus, wantKind)
								}
							}
						}
					}
				}
			}
		}
	}
	if n < 1000 {
		t.Fatalf("matrix too small: %d", n)
	}
}

func TestUnauthenticatedResponses(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/x", nil)
	r.Header.Set("Authorization", "Bearer mmp_bad")
	rec, _ := run("proxy", r)
	if rec.Code != 401 || rec.Header().Get("WWW-Authenticate") != "Bearer" ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "application/problem+json") ||
		!strings.Contains(rec.Body.String(), `"code":"UNAUTHENTICATED"`) {
		t.Fatalf("bad bearer: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	rec, _ = run("local", httptest.NewRequest("GET", "/tracks", nil))
	if rec.Code != 303 || rec.Header().Get("Location") != "/auth/login" {
		t.Fatalf("html local: %d %v", rec.Code, rec.Header())
	}
	rec, _ = run("proxy", httptest.NewRequest("GET", "/tracks", nil))
	if rec.Code != 401 || !strings.Contains(rec.Body.String(), "Not authenticated") ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("html proxy: %d %s", rec.Code, rec.Body.String())
	}
	rec, _ = run("local", httptest.NewRequest("GET", "/human/api/x", nil))
	if rec.Code != 401 || !strings.Contains(rec.Body.String(), `"code":"UNAUTHENTICATED"`) {
		t.Fatalf("human api anon: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRemoteHeadersIgnoredWithBearerAndLocal(t *testing.T) {
	// valid bearer wins even if proxy headers are present and valid for another user
	r := httptest.NewRequest("GET", "/api/x", nil)
	r.RemoteAddr = "10.0.0.1:1"
	r.Header.Set("Authorization", "Bearer mmp_good")
	r.Header.Set("Remote-User", "alice")
	r.Header.Set("X-MM-Edge", edge)
	if _, k := run("proxy", r); k != KindAgent {
		t.Fatalf("kind = %q", k)
	}
}

func TestCrossOriginProtection(t *testing.T) {
	for _, mode := range []string{"proxy", "local"} {
		r := httptest.NewRequest("POST", "https://mm.example.com/api/x", nil)
		r.RemoteAddr = "10.0.0.1:1"
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		r.AddCookie(&http.Cookie{Name: "mm_session", Value: "goodsess"})
		r.Header.Set("Remote-User", "alice")
		r.Header.Set("X-MM-Edge", edge)
		rec, seen := run(mode, r)
		if rec.Code != 403 || seen != "" || !strings.Contains(rec.Body.String(), "CROSS_ORIGIN") {
			t.Fatalf("%s cross-site POST: %d %q %s", mode, rec.Code, seen, rec.Body.String())
		}
		// same-origin POST passes
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		if rec, seen := run(mode, r); rec.Code != 200 || seen != KindHuman {
			t.Fatalf("%s same-origin POST: %d %q", mode, rec.Code, seen)
		}
	}
	// bearer POST is not subject to it
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set("Authorization", "Bearer mmp_good")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	if rec, _ := run("proxy", r); rec.Code != 200 {
		t.Fatalf("bearer cross-site POST: %d", rec.Code)
	}
}
