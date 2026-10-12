package httpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/httpserver"
	"github.com/nicolasacchi/metamusic/internal/store"
)

const edge = "edgesecret"

type env struct {
	h      http.Handler
	tokens *auth.TokenStore
}

func newEnv(t *testing.T, mutate func(*config.Config), spa fstestFS, mcp http.Handler) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Config{
		Addr: "127.0.0.1:8080", PublicURL: "http://mm.example.com", AuthMode: "proxy",
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
		TrustedUsers:   []string{"alice"},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	sec := config.Secrets{Key: key, EdgeSecret: edge}
	tokens := auth.NewTokenStore(st, key)
	h, err := httpserver.New(httpserver.Options{
		Cfg: cfg, Sec: sec, Store: st, Tokens: tokens,
		Sessions: auth.NewLocalAuth(st, key, cfg.PublicURL), MCP: mcp, SPA: spa,
		Version: "v-test", Commit: "abc123",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &env{h: h, tokens: tokens}
}

type fstestFS = fstest.MapFS

func human(r *http.Request) *http.Request {
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("Remote-User", "alice")
	r.Header.Set("X-MM-Edge", edge)
	return r
}

func bearer(r *http.Request, tok string) *http.Request {
	r.Header.Set("Authorization", "Bearer "+tok)
	return r
}

func (e *env) do(r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

func (e *env) token(t *testing.T, scopes ...auth.Scope) string {
	t.Helper()
	tok, _, err := e.tokens.Create(context.Background(), "agent-1", scopes)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("body %q: %v", w.Body, err)
	}
	return m
}

var spaFS = fstest.MapFS{
	"index.html":           {Data: []byte("<html>spa</html>")},
	"assets/app.js":        {Data: []byte("console.log(1)")},
	"manifest.webmanifest": {Data: []byte("{}")},
	"other.html":           {Data: []byte("<html>other</html>")},
}

const wantCSP = "default-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; connect-src 'self'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'"

func TestHealthz(t *testing.T) {
	e := newEnv(t, nil, spaFS, nil)
	w := e.do(httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestOpsHumanAndAgent(t *testing.T) {
	e := newEnv(t, nil, spaFS, nil)

	w := e.do(human(httptest.NewRequest("GET", "/api/ping", nil)))
	if m := decode(t, w); w.Code != 200 || m["ok"] != true || m["version"] != "v-test" {
		t.Fatalf("ping %d %v", w.Code, m)
	}
	w = e.do(human(httptest.NewRequest("GET", "/api/me", nil)))
	m := decode(t, w)
	if m["kind"] != "human" || m["name"] != "alice" || m["via"] != "proxy" || len(m["scopes"].([]any)) != 4 {
		t.Fatalf("me human %v", m)
	}
	tok := e.token(t, auth.ScopeRead)
	w = e.do(bearer(httptest.NewRequest("GET", "/api/me", nil), tok))
	m = decode(t, w)
	if m["kind"] != "agent" || m["name"] != "agent-1" || m["via"] != "token" || len(m["scopes"].([]any)) != 1 {
		t.Fatalf("me agent %v", m)
	}
	w = e.do(bearer(httptest.NewRequest("GET", "/api/system", nil), tok))
	m = decode(t, w)
	if w.Code != 200 || m["version"] != "v-test" || m["commit"] != "abc123" || m["auth_mode"] != "proxy" ||
		m["schema_version"] != float64(1) || m["started_at"] == "" {
		t.Fatalf("system %d %v", w.Code, m)
	}
}

func TestScopeEnforcement(t *testing.T) {
	e := newEnv(t, nil, spaFS, nil)
	tok := e.token(t, auth.ScopeWrite)
	w := e.do(bearer(httptest.NewRequest("GET", "/api/me", nil), tok))
	if w.Code != 403 || decode(t, w)["code"] != "FORBIDDEN_SCOPE" || w.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestProblemStatuses(t *testing.T) {
	e := newEnv(t, nil, spaFS, nil)
	for _, tc := range []struct {
		name   string
		req    *http.Request
		status int
		code   string
	}{
		{"401 anon", httptest.NewRequest("GET", "/api/me", nil), 401, "UNAUTHENTICATED"},
		{"401 bad bearer", bearer(httptest.NewRequest("GET", "/api/me", nil), "mmp_bad"), 401, "UNAUTHENTICATED"},
		{"404", human(httptest.NewRequest("GET", "/api/nope", nil)), 404, "NOT_FOUND"},
		{"405", human(httptest.NewRequest("DELETE", "/api/me", nil)), 405, "METHOD_NOT_ALLOWED"},
	} {
		w := e.do(tc.req)
		if w.Code != tc.status || w.Header().Get("Content-Type") != "application/problem+json" || decode(t, w)["code"] != tc.code {
			t.Errorf("%s: %d %q %s", tc.name, w.Code, w.Header().Get("Content-Type"), w.Body)
		}
	}
}

func TestOpenAPIServed(t *testing.T) {
	e := newEnv(t, nil, spaFS, nil)
	w := e.do(human(httptest.NewRequest("GET", "/api/openapi.json", nil)))
	if w.Code != 200 || decode(t, w)["openapi"] == nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestSPA(t *testing.T) {
	e := newEnv(t, nil, spaFS, nil)
	w := e.do(human(httptest.NewRequest("GET", "/library/some-route", nil)))
	if w.Code != 200 || w.Body.String() != "<html>spa</html>" {
		t.Fatalf("fallback %d %s", w.Code, w.Body)
	}
	if w.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("index cache %q", w.Header().Get("Cache-Control"))
	}
	hd := w.Header()
	if hd.Get("Content-Security-Policy") != wantCSP ||
		hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("Referrer-Policy") != "same-origin" {
		t.Errorf("headers %v", hd)
	}
	// assets are public and immutable
	w = e.do(httptest.NewRequest("GET", "/assets/app.js", nil))
	if w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") || w.Body.String() != "console.log(1)" {
		t.Fatalf("asset %d %v", w.Code, w.Header())
	}
	// other HTML files get the same security headers
	w = e.do(human(httptest.NewRequest("GET", "/other.html", nil)))
	if w.Code != 200 || w.Header().Get("Content-Security-Policy") != wantCSP || w.Header().Get("Referrer-Policy") != "same-origin" {
		t.Errorf("other.html %d %v", w.Code, w.Header())
	}
	// missing static files 404 instead of falling back to the shell
	for _, p := range []string{"/assets/gone.js", "/icons/gone.png"} {
		if w = e.do(human(httptest.NewRequest("GET", p, nil))); w.Code != 404 {
			t.Errorf("%s = %d", p, w.Code)
		}
	}
	// POST to SPA is refused
	w = e.do(human(httptest.NewRequest("POST", "/", nil)))
	if w.Code != 405 {
		t.Errorf("POST / = %d", w.Code)
	}
}

func TestSPANotBuilt(t *testing.T) {
	e := newEnv(t, nil, fstest.MapFS{".gitkeep": {}}, nil)
	w := e.do(human(httptest.NewRequest("GET", "/", nil)))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "UI not built") || w.Header().Get("Content-Security-Policy") != wantCSP {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestMCPRouting(t *testing.T) {
	e := newEnv(t, nil, spaFS, nil)
	if w := e.do(httptest.NewRequest("POST", "/mcp", nil)); w.Code != 404 {
		t.Errorf("nil mcp: %d", w.Code)
	}
	called := false
	mcp := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(204)
	})
	e = newEnv(t, nil, spaFS, mcp)
	w := e.do(httptest.NewRequest("POST", "/mcp", nil))
	if w.Code != 401 || !strings.Contains(w.Header().Get("WWW-Authenticate"),
		`Bearer resource_metadata="http://mm.example.com/.well-known/oauth-protected-resource/mcp"`) || called {
		t.Fatalf("anon mcp %d %v", w.Code, w.Header())
	}
	// human principal (proxy headers) is not accepted on machine paths
	w = e.do(human(httptest.NewRequest("POST", "/mcp", nil)))
	if w.Code != 401 || called {
		t.Fatalf("human mcp %d", w.Code)
	}
	w = e.do(bearer(httptest.NewRequest("POST", "/mcp", nil), e.token(t, auth.ScopeRead)))
	if w.Code != 204 || !called {
		t.Fatalf("agent mcp %d", w.Code)
	}
}

func TestAuthRoutesLocalOnly(t *testing.T) {
	e := newEnv(t, nil, spaFS, nil)
	if w := e.do(httptest.NewRequest("GET", "/auth/login", nil)); w.Code != 404 {
		t.Errorf("proxy mode login = %d", w.Code)
	}
	e = newEnv(t, func(c *config.Config) { c.AuthMode = "local" }, spaFS, nil)
	w := e.do(httptest.NewRequest("GET", "/auth/login", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "<form") {
		t.Errorf("local login = %d", w.Code)
	}
}

func TestE2EServed(t *testing.T) {
	// disabled unless E2E
	e := newEnv(t, nil, spaFS, nil)
	if w := e.do(human(httptest.NewRequest("GET", "/__e2e/served", nil))); w.Code == 200 && strings.HasPrefix(w.Body.String(), "[") {
		t.Fatalf("e2e endpoint exposed: %s", w.Body)
	}

	e = newEnv(t, func(c *config.Config) { c.E2E = true }, spaFS, nil)
	r := human(httptest.NewRequest("GET", "/api/ping", nil))
	r.Header.Set("X-MM-Client", "spa")
	e.do(r)
	e.do(human(httptest.NewRequest("GET", "/api/me", nil))) // untagged: not recorded
	e.do(human(httptest.NewRequest("GET", "/api/system?mm_client=spa", nil)))
	bad := human(httptest.NewRequest("GET", "/api/nope", nil))
	bad.Header.Set("X-MM-Client", "spa") // not an operation
	e.do(bad)

	w := e.do(human(httptest.NewRequest("GET", "/__e2e/served", nil)))
	var ids []string
	if err := json.Unmarshal(w.Body.Bytes(), &ids); err != nil {
		t.Fatalf("%v %s", err, w.Body)
	}
	if strings.Join(ids, ",") != "get_system_info,ping" {
		t.Fatalf("served = %v", ids)
	}
}

func TestMCPFactoryShares(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := []byte("0123456789abcdef0123456789abcdef")
	cfg := config.Config{Addr: "127.0.0.1:8080", PublicURL: "http://mm.example.com", AuthMode: "proxy"}
	tokens := auth.NewTokenStore(st, key)
	started := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var gotAPI huma.API
	var gotInner http.Handler
	h, err := httpserver.New(httpserver.Options{
		Cfg: cfg, Sec: config.Secrets{Key: key, EdgeSecret: edge}, Store: st, Tokens: tokens,
		Sessions: auth.NewLocalAuth(st, key, cfg.PublicURL), SPA: spaFS, Started: started,
		MCPFactory: func(a huma.API, inner http.Handler) (http.Handler, error) {
			gotAPI, gotInner = a, inner
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(204)
			}), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAPI == nil || gotInner == nil {
		t.Fatal("factory not called")
	}
	// the inner handler serves /api/me for a principal in the context
	p := auth.Principal{Kind: auth.KindAgent, Name: "x", Scopes: []auth.Scope{auth.ScopeRead}, Via: "token"}
	r := httptest.NewRequest("GET", "/api/me", nil).WithContext(auth.WithPrincipal(ctx, p))
	w := httptest.NewRecorder()
	gotInner.ServeHTTP(w, r)
	if w.Code != 200 || decode(t, w)["name"] != "x" {
		t.Fatalf("inner /api/me: %d %s", w.Code, w.Body)
	}
	// Started is shared with get_system_info
	r = httptest.NewRequest("GET", "/api/system", nil).WithContext(auth.WithPrincipal(ctx, p))
	w = httptest.NewRecorder()
	gotInner.ServeHTTP(w, r)
	if decode(t, w)["started_at"] != "2026-01-02T03:04:05Z" {
		t.Fatalf("started_at %s", w.Body)
	}
	// and /mcp is routed to the factory-built handler for agents
	tok, _, _ := tokens.Create(ctx, "a", []auth.Scope{auth.ScopeRead})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, bearer(httptest.NewRequest("POST", "/mcp", nil), tok))
	if w.Code != 204 {
		t.Errorf("/mcp = %d", w.Code)
	}
}
