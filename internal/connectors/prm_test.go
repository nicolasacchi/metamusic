package connectors_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/connectors"
)

func prmConfig(oidc bool) config.Config {
	c := config.Config{PublicURL: "https://mm.example.com", OIDCScopes: []string{"openid", "metamusic"}}
	if oidc {
		c.OIDCIssuer = "https://auth.example.com"
	}
	return c
}

func doPRM(h http.Handler, method string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, "/.well-known/oauth-protected-resource/mcp", nil))
	return w
}

// TestPRM_Golden pins the document in both modes and the method handling (C106).
func TestPRM_Golden(t *testing.T) {
	t.Parallel()
	for _, mode := range []struct {
		name   string
		oidc   bool
		golden string
	}{{"oidc", true, "prm_oidc.golden.json"}, {"no_oidc", false, "prm_no_oidc.golden.json"}} {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()
			h := connectors.PRMHandler(prmConfig(mode.oidc))
			w := doPRM(h, "GET")
			if w.Code != 200 || w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("GET: %d %q", w.Code, w.Header().Get("Content-Type"))
			}
			if cc := w.Header().Get("Cache-Control"); cc != "public, max-age=300" {
				t.Errorf("Cache-Control = %q", cc)
			}
			if w.Header().Get("Access-Control-Allow-Origin") != "*" {
				t.Errorf("ACAO = %q", w.Header().Get("Access-Control-Allow-Origin"))
			}
			path := filepath.Join("testdata", mode.golden)
			if *update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, w.Body.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if d := cmp.Diff(string(want), w.Body.String()); d != "" {
				t.Errorf("document differs from %s (-want +got):\n%s", mode.golden, d)
			}
			if hw := doPRM(h, "HEAD"); hw.Code != 200 || hw.Header().Get("Cache-Control") != "public, max-age=300" {
				t.Errorf("HEAD: %d", hw.Code)
			}
		})
	}
}

func TestPRM_OptionsAndMethods(t *testing.T) {
	t.Parallel()
	h := connectors.PRMHandler(prmConfig(true))
	w := doPRM(h, "OPTIONS")
	if w.Code != 204 {
		t.Fatalf("OPTIONS: %d", w.Code)
	}
	for k, v := range map[string]string{
		"Access-Control-Allow-Origin":  "*",
		"Access-Control-Allow-Methods": "GET, HEAD, OPTIONS",
		"Access-Control-Allow-Headers": "Content-Type",
	} {
		if got := w.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		w := doPRM(h, m)
		if w.Code != 405 || w.Header().Get("Allow") != "GET, HEAD, OPTIONS" || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
			t.Errorf("%s: %d allow=%q ct=%q", m, w.Code, w.Header().Get("Allow"), w.Header().Get("Content-Type"))
		}
		var p struct{ Code string }
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || p.Code != "METHOD_NOT_ALLOWED" {
			t.Errorf("%s body %s", m, w.Body)
		}
	}
}

// TestPRM_ResourceMatchesMCPURL: go-sdk's client accepts the document for <PublicURL>/mcp.
func TestPRM_ResourceMatchesMCPURL(t *testing.T) {
	t.Parallel()
	var srv *httptest.Server
	mux := http.NewServeMux()
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := config.Config{PublicURL: srv.URL, OIDCIssuer: "https://auth.example.com", OIDCScopes: []string{"openid", "metamusic"}}
	mux.Handle("/.well-known/oauth-protected-resource/mcp", connectors.PRMHandler(cfg))
	md, err := oauthex.GetProtectedResourceMetadata(t.Context(), srv.URL+"/.well-known/oauth-protected-resource/mcp", srv.URL+"/mcp", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if md.Resource != srv.URL+"/mcp" || len(md.AuthorizationServers) != 1 || md.AuthorizationServers[0] != "https://auth.example.com" {
		t.Errorf("metadata %+v", md)
	}
}
