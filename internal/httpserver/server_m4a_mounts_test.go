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

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/connectors"
	"github.com/nicolasacchi/metamusic/internal/httpserver"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// newM4aEnv builds the full handler with the M4a options set by mutate.
func newM4aEnv(t *testing.T, spa fstest.MapFS, mutate func(*httpserver.Options)) *env {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := config.Config{
		Addr: "127.0.0.1:8080", PublicURL: "https://mm.example.com", AuthMode: "proxy",
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, TrustedUsers: []string{"alice"},
		OIDCIssuer: "https://auth.example.com", OIDCScopes: []string{"openid", "metamusic"},
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	tokens := auth.NewTokenStore(st, key, time.Now)
	o := httpserver.Options{Cfg: cfg, Sec: config.Secrets{Key: key, EdgeSecret: edge}, Store: st, Tokens: tokens,
		Sessions: auth.NewLocalAuth(st, key, cfg.PublicURL), SPA: spa, Version: "v-test", Commit: "abc123"}
	if mutate != nil {
		mutate(&o)
	}
	h, err := httpserver.New(o)
	if err != nil {
		t.Fatal(err)
	}
	return &env{h: h, tokens: tokens, st: st}
}

func problemOf(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var p struct{ Code string }
	if w.Code != status || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") || json.Unmarshal(w.Body.Bytes(), &p) != nil || p.Code != code {
		t.Errorf("got %d %q %s, want %d problem %s", w.Code, w.Header().Get("Content-Type"), w.Body, status, code)
	}
}

func TestHTTPServer_M4aMounts(t *testing.T) {
	t.Parallel()
	subsonic := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Subsonic-Stub", r.URL.Path)
		w.WriteHeader(200)
	})
	spa := fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><title>mm</title>")},
		"sw.js":      {Data: []byte("self.addEventListener('install',()=>{})")},
	}
	e := newM4aEnv(t, spa, func(o *httpserver.Options) {
		o.PRM = connectors.PRMHandler(o.Cfg)
		o.Subsonic = subsonic
	})

	t.Run("prm", func(t *testing.T) {
		w := e.do(httptest.NewRequest("GET", "/.well-known/oauth-protected-resource/mcp", nil))
		if w.Code != 200 || w.Header().Get("Cache-Control") != "public, max-age=300" || w.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("GET: %d %v", w.Code, w.Header())
		}
		var md struct {
			Resource string   `json:"resource"`
			AS       []string `json:"authorization_servers"`
		}
		if json.Unmarshal(w.Body.Bytes(), &md) != nil || md.Resource != "https://mm.example.com/mcp" || len(md.AS) != 1 {
			t.Errorf("body %s", w.Body)
		}
		w = e.do(httptest.NewRequest("OPTIONS", "/.well-known/oauth-protected-resource/mcp", nil))
		if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("Access-Control-Allow-Methods") != "GET, HEAD, OPTIONS" {
			t.Errorf("OPTIONS: %d %v", w.Code, w.Header())
		}
		w = e.do(httptest.NewRequest("POST", "/.well-known/oauth-protected-resource/mcp", nil))
		problemOf(t, w, 405, "METHOD_NOT_ALLOWED")
		if w.Header().Get("Allow") != "GET, HEAD, OPTIONS" {
			t.Errorf("Allow = %q", w.Header().Get("Allow"))
		}
	})

	t.Run("other well-known paths are 404 problems", func(t *testing.T) {
		for _, p := range []string{"/.well-known/oauth-protected-resource", "/.well-known/other", "/.well-known/", "/.well-known/oauth-protected-resource/mcp/x", "/.well-known/oauth-authorization-server"} {
			problemOf(t, e.do(httptest.NewRequest("GET", p, nil)), 404, "NOT_FOUND")
		}
	})

	t.Run("rest goes to the subsonic handler", func(t *testing.T) {
		w := e.do(httptest.NewRequest("GET", "/rest/ping.view?u=x&t=y&s=z", nil))
		if w.Code != 200 || w.Header().Get("X-Subsonic-Stub") != "/rest/ping.view" {
			t.Errorf("rest: %d %v", w.Code, w.Header())
		}
		// a bearer on /rest is refused by the middleware before the handler (C104 row 1c)
		r := httptest.NewRequest("GET", "/rest/ping", nil)
		r.Header.Set("Authorization", "Bearer mmp_x")
		if w := e.do(r); w.Code != 401 || w.Header().Get("X-Subsonic-Stub") != "" {
			t.Errorf("rest with bearer: %d", w.Code)
		}
	})

	t.Run("sw.js", func(t *testing.T) {
		w := e.do(httptest.NewRequest("GET", "/sw.js", nil)) // anonymous, proxy mode
		if w.Code != 200 || w.Header().Get("Content-Type") != "text/javascript; charset=utf-8" || w.Header().Get("Cache-Control") != "no-cache" ||
			!strings.Contains(w.Body.String(), "addEventListener") {
			t.Fatalf("sw.js: %d %v %q", w.Code, w.Header(), w.Body)
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("nosniff missing: %v", w.Header())
		}
		if hw := e.do(httptest.NewRequest("HEAD", "/sw.js", nil)); hw.Code != 200 || hw.Body.Len() != 0 {
			t.Errorf("HEAD: %d %d", hw.Code, hw.Body.Len())
		}
		problemOf(t, e.do(httptest.NewRequest("POST", "/sw.js", nil)), 405, "METHOD_NOT_ALLOWED")
	})
}

func TestHTTPServer_M4aMountsNil(t *testing.T) {
	t.Parallel()
	// no SPA file, no PRM handler, no Subsonic handler
	e := newM4aEnv(t, fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}, nil)
	problemOf(t, e.do(httptest.NewRequest("GET", "/rest/ping", nil)), 404, "NOT_FOUND")
	problemOf(t, e.do(httptest.NewRequest("GET", "/.well-known/oauth-protected-resource/mcp", nil)), 404, "NOT_FOUND")
	// a missing service worker is a 404, never the HTML shell with status 200 (a browser would try to run it as a script)
	problemOf(t, e.do(httptest.NewRequest("GET", "/sw.js", nil)), 404, "NOT_FOUND")
	// a nil SPA filesystem must not panic either
	e = newM4aEnv(t, nil, nil)
	problemOf(t, e.do(httptest.NewRequest("GET", "/sw.js", nil)), 404, "NOT_FOUND")
}
