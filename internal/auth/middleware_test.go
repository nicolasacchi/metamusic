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
	return Principal{}, ErrInvalidSession
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
									r.AddCookie(&http.Cookie{Name: "__Host-mm_session", Value: "goodsess"})
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
	mk := func(method, site, origin string) *http.Request {
		r := httptest.NewRequest(method, "https://mm.example.com/api/x", nil)
		r.RemoteAddr = "10.0.0.1:1"
		if site != "" {
			r.Header.Set("Sec-Fetch-Site", site)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		r.AddCookie(&http.Cookie{Name: "__Host-mm_session", Value: "goodsess"})
		r.Header.Set("Remote-User", "alice")
		r.Header.Set("X-MM-Edge", edge)
		return r
	}
	tests := []struct {
		name                 string
		method, site, origin string
		want                 int
	}{
		{"cross-site POST", "POST", "cross-site", "", 403},
		{"same-site POST", "POST", "same-site", "", 403},
		{"foreign Origin only", "POST", "", "https://evil.example.org", 403},
		{"same-origin POST", "POST", "same-origin", "", 200},
		{"none POST", "POST", "none", "", 200},
		{"cross-site GET", "GET", "cross-site", "", 200},
		{"same-site GET", "GET", "same-site", "", 200},
	}
	for _, mode := range []string{"proxy", "local"} {
		for _, tc := range tests {
			rec, seen := run(mode, mk(tc.method, tc.site, tc.origin))
			if rec.Code != tc.want {
				t.Errorf("%s/%s: %d, want %d", mode, tc.name, rec.Code, tc.want)
			}
			if tc.want == 403 && (seen != "" || !strings.Contains(rec.Body.String(), "CROSS_ORIGIN")) {
				t.Errorf("%s/%s: handler reached or wrong body: %q %s", mode, tc.name, seen, rec.Body.String())
			}
			if tc.want == 200 && seen != KindHuman {
				t.Errorf("%s/%s: kind %q", mode, tc.name, seen)
			}
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

// runP is like run but returns the principal seen by the handler.
func runP(c config.Config, s config.Secrets, r *http.Request) (*httptest.ResponseRecorder, Principal) {
	var seen Principal
	h := Middleware(c, s, fakeTokens{}, fakeSessions{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = FromContext(r.Context())
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec, seen
}

func proxyReq(peer string) *http.Request {
	r := httptest.NewRequest("GET", "/api/x", nil)
	r.RemoteAddr = peer
	r.Header.Set("Remote-User", "alice")
	r.Header.Set("X-MM-Edge", edge)
	return r
}

func TestResolvedPrincipals(t *testing.T) {
	c, s := cfg("proxy")
	_, p := runP(c, s, proxyReq("10.0.0.1:1"))
	if p.Kind != KindHuman || p.Name != "alice" || p.ID != "alice" || p.Via != "proxy" || len(p.Scopes) != 4 {
		t.Fatalf("proxy principal = %+v", p)
	}
	r := httptest.NewRequest("GET", "/api/x", nil)
	r.Header.Set("Authorization", "Bearer mmp_good")
	_, p = runP(c, s, r)
	if p.Kind != KindAgent || p.Via != "token" || p.ID != "t1" || p.Name != "t" {
		t.Fatalf("agent principal = %+v", p)
	}
}

func TestProxyEmptyEdgeSecret(t *testing.T) {
	c, s := cfg("proxy")
	s.EdgeSecret = ""
	r := proxyReq("10.0.0.1:1")
	r.Header.Set("X-MM-Edge", "")
	rec, p := runP(c, s, r)
	if p.Kind == KindHuman || rec.Code != 401 {
		t.Fatalf("empty edge secret accepted: %d %+v", rec.Code, p)
	}
}

func TestIPv4MappedPeerTrusted(t *testing.T) {
	c, s := cfg("proxy")
	rec, p := runP(c, s, proxyReq("[::ffff:10.0.0.1]:1"))
	if rec.Code != 200 || p.Kind != KindHuman {
		t.Fatalf("mapped peer: %d %+v", rec.Code, p)
	}
}

func TestSessionCookieRefreshed(t *testing.T) {
	c, s := cfg("local")
	r := httptest.NewRequest("GET", "/api/x", nil)
	r.AddCookie(&http.Cookie{Name: "__Host-mm_session", Value: "goodsess"})
	rec, _ := runP(c, s, r)
	var got *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "__Host-mm_session" {
			got = ck
		}
	}
	if got == nil || got.Value != "goodsess" || got.MaxAge != 43200 || !got.HttpOnly || !got.Secure ||
		got.SameSite != http.SameSiteLaxMode || got.Path != "/" {
		t.Fatalf("refreshed cookie = %+v", got)
	}
	// anonymous requests get no cookie
	rec, _ = runP(c, s, httptest.NewRequest("GET", "/api/x", nil))
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("cookie set for anonymous")
	}
}

type errTokens struct{}

func (errTokens) Verify(context.Context, string) (Principal, error) {
	return Principal{}, errors.New("db down")
}

type errSessions struct{}

func (errSessions) VerifySession(context.Context, string) (Principal, error) {
	return Principal{}, errors.New("db down")
}

func TestInfrastructureErrorsAre500(t *testing.T) {
	c, s := cfg("local")
	r := httptest.NewRequest("GET", "/api/x", nil)
	r.Header.Set("Authorization", "Bearer mmp_whatever")
	rec := httptest.NewRecorder()
	Middleware(c, s, errTokens{}, fakeSessions{})(http.NotFoundHandler()).ServeHTTP(rec, r)
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), `"code":"INTERNAL"`) || strings.Contains(rec.Body.String(), "db down") {
		t.Fatalf("token infra error: %d %s", rec.Code, rec.Body.String())
	}
	r = httptest.NewRequest("GET", "/api/x", nil)
	r.AddCookie(&http.Cookie{Name: "__Host-mm_session", Value: "x"})
	rec = httptest.NewRecorder()
	Middleware(c, s, fakeTokens{}, errSessions{})(http.NotFoundHandler()).ServeHTTP(rec, r)
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), `"code":"INTERNAL"`) {
		t.Fatalf("session infra error: %d %s", rec.Code, rec.Body.String())
	}
}

func TestBearerSchemeCaseInsensitive(t *testing.T) {
	for _, h := range []string{"bearer mmp_good", "BEARER mmp_good", "Bearer mmp_good"} {
		r := httptest.NewRequest("GET", "/api/x", nil)
		r.Header.Set("Authorization", h)
		if rec, k := run("proxy", r); rec.Code != 200 || k != KindAgent {
			t.Errorf("%q: %d %q", h, rec.Code, k)
		}
	}
	for _, h := range []string{"Bearermmp_good", "Bearer  mmp_good", "Bearer"} {
		r := httptest.NewRequest("GET", "/api/x", nil)
		r.Header.Set("Authorization", h)
		if rec, _ := run("proxy", r); rec.Code != 401 {
			t.Errorf("%q: %d", h, rec.Code)
		}
	}
}

func TestPlantedDuplicateSessionCookieDoesNotShadow(t *testing.T) {
	c, s := cfg("local")
	for _, order := range [][]string{{"bad", "goodsess"}, {"goodsess", "bad"}, {"bad", "bad2", "goodsess"}} {
		r := httptest.NewRequest("GET", "/api/x", nil)
		for _, v := range order {
			r.AddCookie(&http.Cookie{Name: "__Host-mm_session", Value: v})
		}
		if rec, p := runP(c, s, r); rec.Code != 200 || p.Kind != KindHuman {
			t.Errorf("cookies %v: %d %+v", order, rec.Code, p)
		}
	}
	// only bad cookies: still 401
	r := httptest.NewRequest("GET", "/api/x", nil)
	r.AddCookie(&http.Cookie{Name: "__Host-mm_session", Value: "bad"})
	r.AddCookie(&http.Cookie{Name: "__Host-mm_session", Value: "bad2"})
	if rec, _ := runP(c, s, r); rec.Code != 401 {
		t.Errorf("all-bad: %d", rec.Code)
	}
}

func TestHostPrefixedCookieOverHTTPS(t *testing.T) {
	c, s := cfg("local")
	// the unprefixed name (which a sibling host could plant) is not accepted over https
	r := httptest.NewRequest("GET", "/api/x", nil)
	r.AddCookie(&http.Cookie{Name: "mm_session", Value: "goodsess"})
	if rec, _ := runP(c, s, r); rec.Code != 401 {
		t.Errorf("unprefixed cookie accepted over https: %d", rec.Code)
	}
}

type brokenSessions struct{}

func (brokenSessions) VerifySession(context.Context, string) (Principal, error) {
	return Principal{}, errors.New("db down")
}

func TestMiddleware_SessionStoreErrorIs500(t *testing.T) {
	c, s := cfg("local")
	c.PublicURL = "http://mm.example.com"
	h := Middleware(c, s, fakeTokens{}, brokenSessions{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler must not run")
	}))
	r := httptest.NewRequest("GET", "/api/v1/system/info", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: "x"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), `"code":"INTERNAL"`) || strings.Contains(rec.Body.String(), "db down") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestTrustedPeer_Malformed(t *testing.T) {
	pfx := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	for addr, want := range map[string]bool{"": false, "garbage": false, "10.1.2.3": false, "10.1.2.3:80": true, "[::ffff:10.1.2.3]:80": true, "11.0.0.1:80": false} {
		if got := trustedPeer(addr, pfx); got != want {
			t.Errorf("%q: got %v", addr, got)
		}
	}
}

func TestTokenStore_ClosedStoreErrors(t *testing.T) {
	st := newStore(t)
	ts := NewTokenStore(st, key)
	_ = st.Close()
	if _, err := ts.List(context.Background()); err == nil {
		t.Error("List on closed store")
	}
	if err := ts.Revoke(context.Background(), "x"); err == nil {
		t.Error("Revoke on closed store")
	}
	if _, _, err := ts.Create(context.Background(), "n", []Scope{ScopeRead}); err == nil {
		t.Error("Create on closed store")
	}
}
