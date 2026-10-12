package mcpgen_test

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/httpserver"
	"github.com/nicolasacchi/metamusic/internal/mcpgen"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var update = flag.Bool("update", false, "rewrite golden files")

type rig struct {
	srv    *httptest.Server
	tokens *auth.TokenStore
	api    huma.API
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	key := []byte("0123456789abcdef0123456789abcdef")
	cfg := config.Config{
		Addr: "127.0.0.1:8080", PublicURL: "http://mm.example.com", AuthMode: "proxy",
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
		TrustedUsers:   []string{"alice"},
	}
	tokens := auth.NewTokenStore(st, key)
	r := &rig{tokens: tokens}
	h, err := httpserver.New(httpserver.Options{
		Cfg: cfg, Sec: config.Secrets{Key: key, EdgeSecret: "edge"}, Store: st, Tokens: tokens,
		Sessions: auth.NewLocalAuth(st, key, cfg.PublicURL),
		SPA:      fstest.MapFS{"index.html": {Data: []byte("x")}},
		Version:  "v-test", Commit: "abc123",
		Started: time.Now().Add(-time.Hour),
		MCPFactory: func(a huma.API, inner http.Handler) (http.Handler, error) {
			r.api = a
			s, err := mcpgen.NewServer(a, inner, "v-test")
			if err != nil {
				return nil, err
			}
			return mcpgen.Handler(s), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.srv = httptest.NewServer(h)
	t.Cleanup(r.srv.Close)
	return r
}

type bearerRT struct{ tok string }

func (b bearerRT) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.tok)
	return http.DefaultTransport.RoundTrip(req)
}

func (r *rig) token(t *testing.T, name string, scopes ...auth.Scope) string {
	t.Helper()
	tok, _, err := r.tokens.Create(context.Background(), name, scopes)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (r *rig) client(t *testing.T, tok string) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   r.srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerRT{tok}},
		MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func (r *rig) rest(t *testing.T, tok, path string) any {
	t.Helper()
	req, _ := http.NewRequest("GET", r.srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: %d %s", path, resp.StatusCode, b)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func structured(t *testing.T, res *mcp.CallToolResult) any {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	b, _ := json.Marshal(res.StructuredContent)
	var v any
	_ = json.Unmarshal(b, &v)
	return v
}

func TestParityEveryGETTool(t *testing.T) {
	r := newRig(t)
	tok := r.token(t, "agent-1", auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate, auth.ScopeAdmin)
	sess := r.client(t, tok)
	n := 0
	for _, op := range api.Operations(r.api) {
		if op.Method != "GET" || op.Meta.MCP != "tool" || strings.Contains(op.Path, "{") {
			continue
		}
		n++
		var diff string
		for attempt := 0; attempt < 3; attempt++ { // uptime_s may tick between the two calls
			res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: op.ID})
			if err != nil {
				t.Fatalf("%s: %v", op.ID, err)
			}
			diff = cmp.Diff(r.rest(t, tok, op.Path), structured(t, res))
			if diff == "" {
				break
			}
		}
		if diff != "" {
			t.Errorf("%s: REST vs MCP mismatch (-rest +mcp):\n%s", op.ID, diff)
		}
	}
	if n == 0 {
		t.Fatal("no GET tools exercised")
	}
}

func TestWhoamiPropagatesAgentPrincipal(t *testing.T) {
	r := newRig(t)
	tok := r.token(t, "agent-7", auth.ScopeRead)
	res, err := r.client(t, tok).CallTool(context.Background(), &mcp.CallToolParams{Name: "whoami"})
	if err != nil {
		t.Fatal(err)
	}
	m := structured(t, res).(map[string]any)
	if m["kind"] != "agent" || m["name"] != "agent-7" || m["via"] != "token" {
		t.Fatalf("principal not propagated: %v", m)
	}
	if sc := m["scopes"].([]any); len(sc) != 1 || sc[0] != "read" {
		t.Fatalf("scopes %v", sc)
	}
}

func TestScopeEnforcedThroughMCP(t *testing.T) {
	r := newRig(t)
	tok := r.token(t, "w", auth.ScopeWrite) // no read
	res, err := r.client(t, tok).CallTool(context.Background(), &mcp.CallToolParams{Name: "ping"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "FORBIDDEN_SCOPE") {
		t.Fatalf("want FORBIDDEN_SCOPE error result, got %+v", res)
	}
}

func TestAnonymousMCPIs401(t *testing.T) {
	r := newRig(t)
	for _, auth := range []string{"", "Bearer mmp_bogus"} {
		req, _ := http.NewRequest("POST", r.srv.URL+"/mcp", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		wa := resp.Header.Get("WWW-Authenticate")
		if resp.StatusCode != 401 || !strings.HasPrefix(wa, "Bearer") {
			t.Fatalf("auth=%q: %d %q", auth, resp.StatusCode, wa)
		}
		if auth == "" && !strings.Contains(wa, `resource_metadata="http://mm.example.com/.well-known/oauth-protected-resource/mcp"`) {
			t.Fatalf("missing resource_metadata: %q", wa)
		}
	}
}

func TestToolsListGolden(t *testing.T) {
	r := newRig(t)
	sess := r.client(t, r.token(t, "g", auth.ScopeRead))
	res, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(res.Tools, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "tools_list.golden.json")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	// Budget: tool descriptions and schemas cost agent context; growth beyond 1.5x
	// the reviewed golden must be a deliberate decision, not drift.
	if len(got)*2 > len(want)*3 {
		t.Errorf("tools/list is %d bytes, over 1.5x golden (%d)", len(got), len(want))
	}
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("tools/list differs from golden (-want +got; rerun with -update):\n%s", diff)
	}
}
