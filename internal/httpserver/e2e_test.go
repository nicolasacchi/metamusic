package httpserver_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/httpserver"
	"github.com/nicolasacchi/metamusic/internal/store"
)

const (
	canaryEdge = "canary-edge-secret-value"
	canaryNtfy = "canary-ntfy-token-value"
)

var canaryKey = []byte("0123456789abcdef0123456789abcdef")

func leakRig(t *testing.T, e2e bool) (http.Handler, string) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Config{
		Addr: "127.0.0.1:8080", PublicURL: "http://mm.example.com", AuthMode: "proxy", E2E: e2e,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, TrustedUsers: []string{"alice"},
	}
	sec := config.Secrets{Key: canaryKey, EdgeSecret: canaryEdge, NtfyToken: canaryNtfy}
	tokens := auth.NewTokenStore(st, canaryKey, time.Now)
	tok, _, err := tokens.Create(context.Background(), "leaky", []auth.Scope{auth.ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Leak", canaryEdge)
		w.Header().Set("Content-Type", "application/json")
		hexKey := hex.EncodeToString(canaryKey)
		_, _ = w.Write([]byte(`{"k":"` + hexKey[:20]))
		_, _ = w.Write([]byte(hexKey[20:] + `"}`))
	})
	media := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("xx" + canaryNtfy + "xx")) })
	h, err := httpserver.New(httpserver.Options{Cfg: cfg, Sec: sec, Store: st, Tokens: tokens,
		Sessions: auth.NewLocalAuth(st, canaryKey, cfg.PublicURL), MCP: echo, Media: media})
	if err != nil {
		t.Fatal(err)
	}
	return h, tok
}

func TestE2ELeakScanner(t *testing.T) {
	t.Parallel()
	h, tok := leakRig(t, true)
	do := func(method, path string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.RemoteAddr = "127.0.0.1:5555"
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	bearer := map[string]string{"Authorization": "Bearer " + tok}
	do("POST", "/mcp", bearer)
	human := map[string]string{"Remote-User": "alice", "X-MM-Edge": canaryEdge}
	do("GET", "/media/abc.flac", human)

	w := do("GET", "/__e2e/leaks", human)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	for _, v := range []string{canaryEdge, canaryNtfy, hex.EncodeToString(canaryKey), string(canaryKey)} {
		if strings.Contains(body, v) {
			t.Fatalf("leak list contains a value: %s", body)
		}
	}
	var got []map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, l := range got {
		kinds = append(kinds, l["kind"]+"/"+l["where"]+"/"+l["path"])
	}
	want := map[string]bool{"edge_secret/header//mcp": true, "secret_key/body//mcp": true}
	if len(got) != 2 {
		t.Fatalf("leaks = %v", kinds)
	}
	for _, k := range kinds {
		if !want[k] {
			t.Fatalf("unexpected leak %s in %v", k, kinds)
		}
	}
}

func TestE2ELeakScanner_CleanRunIsEmptyList(t *testing.T) {
	t.Parallel()
	h, _ := leakRig(t, true)
	r := httptest.NewRequest("GET", "/__e2e/leaks", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("Remote-User", "alice")
	r.Header.Set("X-MM-Edge", canaryEdge)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("%q", w.Body)
	}
}

func TestE2ELeakScanner_OffIs404AndUnwrapped(t *testing.T) {
	t.Parallel()
	h, tok := leakRig(t, false)
	r := httptest.NewRequest("GET", "/__e2e/leaks", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("Remote-User", "alice")
	r.Header.Set("X-MM-Edge", canaryEdge)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("%d", w.Code)
	}
	// The echo handler still answers (nothing wraps or blocks it).
	r = httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("X-Leak") != canaryEdge {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
}

func TestHTTPServer_GateWiring(t *testing.T) {
	t.Parallel()
	st, err := store.Open(context.Background(), t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Config{AuthMode: "proxy", PublicURL: "http://mm.example.com", RateScale: 1}
	tokens := auth.NewTokenStore(st, canaryKey, time.Now)
	tok, id, err := tokens.Create(context.Background(), "bot", []auth.Scope{auth.ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	var gated http.Handler
	h, err := httpserver.New(httpserver.Options{Cfg: cfg, Sec: config.Secrets{Key: canaryKey, EdgeSecret: "e"}, Store: st, Tokens: tokens,
		Sessions: auth.NewLocalAuth(st, canaryKey, cfg.PublicURL),
		MCPFactory: func(a huma.API, inner http.Handler) (http.Handler, error) {
			gated = inner
			return http.NotFoundHandler(), nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	// REST through the full chain: an agent read is audited.
	r := httptest.NewRequest("GET", "/api/ping", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	// MCP dispatch uses the gated handler, not the bare mux: same audit effect.
	r = httptest.NewRequest("GET", "/api/ping", nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Kind: auth.KindAgent, ID: id, Name: "bot", Scopes: []auth.Scope{auth.ScopeRead}}))
	gated.ServeHTTP(httptest.NewRecorder(), r)
	rows, err := audit.NewStore(st, func() string { return "x" }).List(context.Background(), audit.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.Items) != 2 {
		t.Fatalf("audit rows = %d, want 2 (REST and MCP-gated)", len(rows.Items))
	}
	for _, row := range rows.Items {
		if row.Op != "ping" || row.PrincipalID != "agent:"+id {
			t.Fatalf("row = %+v", row)
		}
	}
}
