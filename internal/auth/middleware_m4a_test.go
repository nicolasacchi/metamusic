package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeConnectors answers by the first dot-separated segment of the bearer and counts its calls.
type fakeConnectors struct{ calls atomic.Int64 }

func (f *fakeConnectors) VerifyConnector(_ context.Context, bearer string) (Principal, error) {
	f.calls.Add(1)
	kind, _, _ := strings.Cut(bearer, ".")
	switch kind {
	case "valid":
		return Principal{Kind: KindAgent, ID: "grant-1", Name: "Claude", Scopes: []Scope{ScopeRead, ScopeWrite}, Via: "oauth", Client: "test-claude"}, nil
	case "expired":
		return Principal{}, ErrInvalidToken
	case "noscope":
		return Principal{}, &ScopeError{Scope: "metamusic"}
	case "unlisted":
		return Principal{}, ErrConnectorForbidden
	case "revoked":
		return Principal{}, ErrConnectorRevoked
	case "unavail":
		return Principal{}, ErrUnavailable
	}
	return Principal{}, errors.New("verifier exploded: secret-detail")
}

const prm = `resource_metadata="https://mm.example.com/.well-known/oauth-protected-resource/mcp"`

type m4aWant struct {
	status int
	code   string // problem code ("" ⇒ the handler ran)
	www    string
	calls  int64
}

func TestTrustMatrix_M4a(t *testing.T) {
	t.Parallel()
	nine := "a." + strings.Repeat("b", 9000) + ".c"
	invalid := m4aWant{401, "UNAUTHENTICATED", `Bearer error="invalid_token", ` + prm, 0}
	bare := m4aWant{401, "UNAUTHENTICATED", "Bearer", 0}
	bearers := []struct {
		name, tok string
		mcpOn     m4aWant // path /mcp or /mcp/x, connectors on (calls counts the verifier)
	}{
		{"valid", "valid.x.y", m4aWant{200, "", "", 1}},
		{"expired", "expired.x.y", m4aWant{401, "UNAUTHENTICATED", `Bearer error="invalid_token", ` + prm, 1}},
		{"no metamusic scope", "noscope.x.y", m4aWant{403, "INSUFFICIENT_SCOPE", `Bearer error="insufficient_scope", scope="metamusic", ` + prm, 1}},
		{"unlisted client", "unlisted.x.y", m4aWant{403, "CONNECTOR_FORBIDDEN", "", 1}},
		{"revoked grant", "revoked.x.y", m4aWant{403, "CONNECTOR_REVOKED", "", 1}},
		{"provider down", "unavail.x.y", m4aWant{503, "UNAVAILABLE", "", 1}},
		{"verifier bug", "boom.x.y", m4aWant{500, "INTERNAL", "", 1}},
		{"opaque token", "opaquetoken", invalid},
		{"one dot", "a.b", invalid},
		{"three dots", "a.b.c.d", invalid},
		{"empty bearer value", "", invalid},
		{"9000 bytes", nine, invalid},
	}
	paths := []string{"/mcp", "/mcp/x", "/api/tracks", "/rest/ping", "/media/x", "/mcpx"}
	for _, b := range bearers {
		for _, path := range paths {
			for _, on := range []bool{true, false} {
				want := bare
				if on && (path == "/mcp" || path == "/mcp/x") {
					want = b.mcpOn
				}
				fc := &fakeConnectors{}
				c, s := cfg("proxy")
				var seen Principal
				v := Verifiers{Tokens: fakeTokens{}, Sessions: fakeSessions{}}
				if on {
					v.Connectors = fc
				}
				h := Middleware(c, s, v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					seen = FromContext(r.Context())
					w.WriteHeader(200)
				}))
				r := httptest.NewRequest("POST", path, nil)
				r.Header.Set("Authorization", "Bearer "+b.tok)
				r.Header.Set("Origin", "https://mm.example.com")
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, r)
				name := b.name + "|" + path + "|on=" + map[bool]string{true: "y", false: "n"}[on]
				if rec.Code != want.status {
					t.Errorf("%s: status %d, want %d (%s)", name, rec.Code, want.status, rec.Body.String())
					continue
				}
				if want.code == "" {
					if seen.Kind != KindAgent || seen.Via != "oauth" || seen.Client != "test-claude" {
						t.Errorf("%s: principal %+v", name, seen)
					}
				} else {
					var body struct{ Code string }
					_ = json.Unmarshal(rec.Body.Bytes(), &body)
					if body.Code != want.code {
						t.Errorf("%s: code %q, want %q", name, body.Code, want.code)
					}
					if strings.Contains(rec.Body.String(), "secret-detail") {
						t.Errorf("%s: verifier error leaked into the body", name)
					}
				}
				if got := rec.Header().Get("WWW-Authenticate"); got != want.www {
					t.Errorf("%s: WWW-Authenticate %q, want %q", name, got, want.www)
				}
				wantCalls := want.calls
				if !on || !(path == "/mcp" || path == "/mcp/x") {
					wantCalls = 0
				}
				if got := fc.calls.Load(); got != wantCalls {
					t.Errorf("%s: verifier calls %d, want %d", name, got, wantCalls)
				}
			}
		}
	}
}

// A PAT keeps working on every path with connectors on; a non-bearer scheme and a doubled header stay row 1c.
func TestTrustMatrix_M4aPATAndSchemes(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/mcp", "/api/tracks", "/rest/ping"} {
		fc := &fakeConnectors{}
		c, s := cfg("proxy")
		h := Middleware(c, s, Verifiers{Tokens: fakeTokens{}, Sessions: fakeSessions{}, Connectors: fc})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
		}))
		for _, tc := range []struct {
			hdr  []string
			want int
		}{
			{[]string{"Bearer mmp_good"}, 200},
			{[]string{"Bearer mmp_bad"}, 401},
			{[]string{"Basic abc"}, 401},
			{[]string{"Bearer valid.x.y", "Bearer valid.x.y"}, 401},
		} {
			r := httptest.NewRequest("GET", path, nil)
			r.Header["Authorization"] = tc.hdr
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != tc.want {
				t.Errorf("%s %v: %d, want %d", path, tc.hdr, rec.Code, tc.want)
			}
			if tc.want == 401 && rec.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Errorf("%s %v: challenge %q", path, tc.hdr, rec.Header().Get("WWW-Authenticate"))
			}
		}
		if fc.calls.Load() != 0 {
			t.Errorf("%s: verifier consulted for PAT/other schemes", path)
		}
	}
}

func TestMiddleware_JWTOnAPIIs401(t *testing.T) {
	t.Parallel()
	fc := &fakeConnectors{}
	c, s := cfg("proxy")
	h := Middleware(c, s, Verifiers{Tokens: fakeTokens{}, Sessions: fakeSessions{}, Connectors: fc})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler reached with a JWT")
	}))
	for _, path := range []string{"/api/tracks", "/human/api/x", "/rest/ping", "/.well-known/x"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer valid.x.y")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != 401 || rec.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("%s: %d %q", path, rec.Code, rec.Header().Get("WWW-Authenticate"))
		}
	}
	if fc.calls.Load() != 0 {
		t.Fatal("verifier consulted off /mcp")
	}
}

func TestScopeError_Is(t *testing.T) {
	t.Parallel()
	var err error = &ScopeError{Scope: "metamusic"}
	if !errors.Is(err, ErrInsufficientScope) || errors.Is(err, ErrInvalidToken) {
		t.Fatal("ScopeError must match ErrInsufficientScope only")
	}
	if !strings.Contains(err.Error(), "metamusic") {
		t.Fatal(err.Error())
	}
}

func TestPrincipal_ClientField(t *testing.T) {
	t.Parallel()
	p := Principal{Kind: KindAgent, ID: "g1", Via: "oauth", Client: "c"}
	if p.Key() != "agent:g1" || p.ActsAsHuman() {
		t.Fatalf("%+v", p)
	}
	d := Principal{Kind: KindDevice, ID: "c1", Via: "subsonic", Client: "amperfy"}
	if d.Key() != "device:c1" || !d.ActsAsHuman() {
		t.Fatalf("%+v", d)
	}
}
