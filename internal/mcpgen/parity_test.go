package mcpgen_test

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
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
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/mcpgen"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
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
	// Every rig holds the same fixture library under a fixed clock, so the REST
	// face and the MCP face (and the two rigs of a mutating op) agree exactly.
	lt.Fixture(t, st, lt.FixedNow)
	dataDir := t.TempDir()
	lt.WriteFiles(t, dataDir)
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	now := func() time.Time { return lt.FixedNow }
	h, err := httpserver.New(httpserver.Options{
		Cfg: cfg, Sec: config.Secrets{Key: key, EdgeSecret: "edge"}, Store: st, Tokens: tokens,
		Sessions: auth.NewLocalAuth(st, key, cfg.PublicURL),
		SPA:      fstest.MapFS{"index.html": {Data: []byte("x")}},
		Version:  "v-test", Commit: "abc123",
		Started: lt.FixedNow.Add(-time.Hour),
		Library: library.New(st, root, now), Signer: serve.NewSigner(key, now), Now: now,
		Probe: &serve.Object{RelPath: "derived/probe/probe-v1.flac", ContentType: "audio/flac", ETag: strings.Repeat("ab", 32)},
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

func (r *rig) rest(t *testing.T, tok, method, path string, body any) any {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, r.srv.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, b)
	}
	var v any
	if len(b) > 0 {
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
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

// parityFixture is the REST rendering of one MCP call: tool arguments plus the
// method, path (with query string) and JSON body the same call uses over REST.
type parityFixture struct {
	Args   map[string]any
	Method string // "" means GET
	Path   string
	Body   any
}

// parityArgs holds a fixture for every tool or generic operation that is not a
// parameterless GET. needsFixture decides which operations those are; one
// without an entry fails TestParityEveryOperation, so new ops cannot escape
// parity unnoticed (and -update on the golden cannot hide it).
var parityArgs = map[string]parityFixture{
	"search_tracks":        {Args: map[string]any{"q": "luna", "limit": 10}, Path: "/api/tracks?q=luna&limit=10"},
	"get_track":            {Args: map[string]any{"id": lt.TrackLuna}, Path: "/api/tracks/" + lt.TrackLuna},
	"get_track_peaks":      {Args: map[string]any{"id": lt.TrackLuna, "level": "detail"}, Path: "/api/tracks/" + lt.TrackLuna + "/peaks?level=detail"},
	"get_media_links":      {Args: map[string]any{"id": lt.TrackLuna}, Path: "/api/tracks/" + lt.TrackLuna + "/media"},
	"list_drafts":          {Args: map[string]any{"limit": 10}, Path: "/api/drafts?limit=10"},
	"get_draft":            {Args: map[string]any{"id": lt.Draft1}, Path: "/api/drafts/" + lt.Draft1},
	"list_import_runs":     {Args: map[string]any{"limit": 10}, Path: "/api/imports?limit=10"},
	"list_device_profiles": {Args: map[string]any{"limit": 10}, Path: "/api/device-profiles?limit=10"},
	"get_import_run":       {Args: map[string]any{"id": lt.RunID, "section": "warnings"}, Path: "/api/imports/" + lt.RunID + "?section=warnings"},
	"put_device_profile": {
		Args: map[string]any{
			"device_id": "parity-device-1", "label": "Parity", "user_agent": "UA/1", "quality": "high", "quality_source": "probe",
			"probe_result": "failed", "flac_can_play": "maybe", "flac_decode_ok": false, "probe_version": 1,
		},
		Method: "PUT", Path: "/api/device-profiles/parity-device-1",
		Body: map[string]any{
			"label": "Parity", "user_agent": "UA/1", "quality": "high", "quality_source": "probe",
			"probe_result": "failed", "flac_can_play": "maybe", "flac_decode_ok": false, "probe_version": 1,
		},
	},
}

// needsFixture reports whether an operation must carry a parity fixture: any
// non-GET method, or any path/query/header parameter, or a request body.
func needsFixture(method string, o *huma.Operation) bool {
	return method != http.MethodGet || len(o.Parameters) > 0 || o.RequestBody != nil
}

func pathOperation(a huma.API, op api.OpInfo) *huma.Operation {
	item := a.OpenAPI().Paths[op.Path]
	if item == nil {
		return nil
	}
	return map[string]*huma.Operation{
		"GET": item.Get, "PUT": item.Put, "POST": item.Post, "DELETE": item.Delete, "PATCH": item.Patch,
	}[op.Method]
}

func TestNeedsFixture(t *testing.T) {
	tests := []struct {
		name   string
		method string
		op     *huma.Operation
		want   bool
	}{
		{"bare GET", "GET", &huma.Operation{}, false},
		{"GET with optional query", "GET", &huma.Operation{Parameters: []*huma.Param{{Name: "limit", In: "query"}}}, true},
		{"GET with path param", "GET", &huma.Operation{Parameters: []*huma.Param{{Name: "id", In: "path"}}}, true},
		{"bare POST", "POST", &huma.Operation{}, true},
		{"GET with body", "GET", &huma.Operation{RequestBody: &huma.RequestBody{}}, true},
		{"DELETE", "DELETE", &huma.Operation{}, true},
	}
	for _, tc := range tests {
		if got := needsFixture(tc.method, tc.op); got != tc.want {
			t.Errorf("%s: needsFixture = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestParityEveryOperation(t *testing.T) {
	shared := newRig(t)
	sharedTok := shared.token(t, "agent-1", auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate, auth.ScopeAdmin)
	sharedSess := shared.client(t, sharedTok)
	ctx := context.Background()
	n := 0
	for _, op := range api.Operations(shared.api) {
		if op.Meta.MCP != "tool" && op.Meta.MCP != "generic" {
			continue
		}
		o := pathOperation(shared.api, op)
		if o == nil {
			t.Errorf("%s: not found in the OpenAPI document", op.ID)
			continue
		}
		fx, haveFx := parityArgs[op.ID]
		if needsFixture(op.Method, o) && !haveFx {
			t.Errorf("%s: %s %s has inputs or side effects and needs a parityArgs fixture", op.ID, op.Method, op.Path)
			continue
		}
		method, path := op.Method, op.Path
		if haveFx {
			path = fx.Path
			if fx.Method != "" {
				method = fx.Method
			}
		}
		n++
		routes := map[string]*mcp.CallToolParams{
			"call_operation": {Name: "call_operation", Arguments: map[string]any{"operation_id": op.ID, "arguments": fx.Args}},
		}
		if op.Meta.MCP == "tool" {
			routes["tool"] = &mcp.CallToolParams{Name: op.ID, Arguments: fx.Args}
		}
		for route, params := range routes {
			var diff string
			if method == http.MethodGet {
				for attempt := 0; attempt < 3; attempt++ { // uptime_s may tick between the two calls
					res, err := sharedSess.CallTool(ctx, params)
					if err != nil {
						t.Fatalf("%s via %s: %v", op.ID, route, err)
					}
					diff = cmp.Diff(shared.rest(t, sharedTok, method, path, fx.Body), structured(t, res))
					if diff == "" {
						break
					}
				}
			} else {
				// mutating op: each face runs against its own fresh rig, so one
				// call cannot change the other face's result
				ra, rb := newRig(t), newRig(t)
				scopes := []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate, auth.ScopeAdmin}
				ta, tb := ra.token(t, "agent-1", scopes...), rb.token(t, "agent-1", scopes...)
				res, err := rb.client(t, tb).CallTool(ctx, params)
				if err != nil {
					t.Fatalf("%s via %s: %v", op.ID, route, err)
				}
				diff = cmp.Diff(ra.rest(t, ta, method, path, fx.Body), structured(t, res))
			}
			if diff != "" {
				t.Errorf("%s via %s: REST vs MCP mismatch (-rest +mcp):\n%s", op.ID, route, diff)
			}
		}
	}
	if n == 0 {
		t.Fatal("no operations exercised")
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
		_ = resp.Body.Close()
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
	// Budget first, independent of -update: the ceiling is a committed constant.
	if err := checkToolsListBudget(len(got)); err != nil {
		t.Error(err)
	}
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
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("tools/list differs from golden (-want +got; rerun with -update):\n%s", diff)
	}
}

// toolsListBaselineBytes is the size of testdata/tools_list.golden.json when the
// budget was last reviewed. Tool descriptions and schemas cost agent context;
// growth beyond 1.5x this is a deliberate decision (edit this constant in
// review), never an -update side effect.
//
// M1-B4 re-baseline 4412 -> 42997: six new tools (search_tracks, get_track, get_media_links, list_drafts,
// get_draft, get_stats) carry full output schemas (TrackDetail alone is ~15 KB); the 13 M1 operations
// are the milestone's whole read surface.
const toolsListBaselineBytes = 42997

func checkToolsListBudget(n int) error {
	if n*2 > toolsListBaselineBytes*3 {
		return fmt.Errorf("tools/list is %d bytes, over 1.5x the committed baseline (%d)", n, toolsListBaselineBytes)
	}
	return nil
}

func TestToolsListBudgetFires(t *testing.T) {
	if checkToolsListBudget(toolsListBaselineBytes) != nil {
		t.Fatal("baseline size must pass")
	}
	if checkToolsListBudget(toolsListBaselineBytes*3/2+10) == nil {
		t.Fatal("oversized tools/list must fail the budget")
	}
}

func TestWhoamiAcrossProtocolVersions(t *testing.T) {
	r := newRig(t)
	tok := r.token(t, "agent-pv", auth.ScopeRead)
	for _, ver := range []string{"", "2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25"} {
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"whoami","arguments":{}}}`
		req, _ := http.NewRequest("POST", r.srv.URL+"/mcp", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if ver != "" {
			req.Header.Set("MCP-Protocol-Version", ver)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("version %q: status %d %s", ver, resp.StatusCode, b)
			continue
		}
		var out struct {
			Result struct {
				StructuredContent map[string]any `json:"structuredContent"`
			} `json:"result"`
		}
		if err := json.Unmarshal(b, &out); err != nil {
			t.Errorf("version %q: %v: %s", ver, err, b)
			continue
		}
		m := out.Result.StructuredContent
		if m["kind"] != "agent" || m["name"] != "agent-pv" {
			t.Errorf("version %q: principal not propagated: %s", ver, b)
		}
	}
}

func TestMCPTransportErrorsAreProblemJSON(t *testing.T) {
	r := newRig(t)
	tok := r.token(t, "agent-1", auth.ScopeRead)
	tests := []struct {
		name, method, body string
		status             int
		code               string
	}{
		{"GET not allowed", "GET", "", 405, "METHOD_NOT_ALLOWED"},
		{"DELETE not allowed", "DELETE", "", 405, "METHOD_NOT_ALLOWED"},
		{"malformed payload", "POST", `{not json`, 400, "BAD_REQUEST"},
		{"unsupported rpc method", "POST", `{"jsonrpc":"2.0","id":1,"method":"nope/nothing"}`, 400, "BAD_REQUEST"},
	}
	for _, tc := range tests {
		req, _ := http.NewRequest(tc.method, r.srv.URL+"/mcp", strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		var p struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		}
		if resp.StatusCode != tc.status || resp.Header.Get("Content-Type") != "application/problem+json" ||
			json.Unmarshal(b, &p) != nil || p.Code != tc.code || p.Message == "" || p.Details == nil {
			t.Errorf("%s: %d %q %s", tc.name, resp.StatusCode, resp.Header.Get("Content-Type"), b)
		}
	}
}

// search_tracks?style=a&style=b over REST (repeated keys) equals the MCP call
// {"style":["a","b"]}: array parameters are exploded on both faces.
func TestParity_MultiValueQuery(t *testing.T) {
	r := newRig(t)
	tok := r.token(t, "agent-mv", auth.ScopeRead)
	sess := r.client(t, tok)
	for _, tc := range []struct {
		name string
		args map[string]any
		path string
		want int
	}{
		{"style AND", map[string]any{"style": []string{"ambient", "drone"}}, "/api/tracks?style=ambient&style=drone", 1},
		{"style single", map[string]any{"style": []string{"ambient"}}, "/api/tracks?style=ambient", 2},
		{"status OR", map[string]any{"status": []string{"final", "draft"}}, "/api/tracks?status=final&status=draft", 5},
		{"both", map[string]any{"status": []string{"draft"}, "style": []string{"rock", "guitar"}}, "/api/tracks?status=draft&style=rock&style=guitar", 2},
	} {
		viaREST := r.rest(t, tok, "GET", tc.path, nil)
		res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "search_tracks", Arguments: tc.args})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		viaMCP := structured(t, res)
		if diff := cmp.Diff(viaREST, viaMCP); diff != "" {
			t.Errorf("%s: REST vs MCP (-rest +mcp):\n%s", tc.name, diff)
		}
		if got := viaREST.(map[string]any)["total"]; got != float64(tc.want) {
			t.Errorf("%s: total = %v, want %d", tc.name, got, tc.want)
		}
	}
}

// MCP output ends up in third-party transcripts: an agent never sees the
// private reference metadata, only has_reference.
func TestGetDraft_ReferenceHumanOnly(t *testing.T) {
	r := newRig(t)
	tok := r.token(t, "agent-ref", auth.ScopeRead)
	viaREST := r.rest(t, tok, "GET", "/api/drafts/"+lt.Draft1, nil).(map[string]any)
	res, err := r.client(t, tok).CallTool(context.Background(), &mcp.CallToolParams{Name: "get_draft", Arguments: map[string]any{"id": lt.Draft1}})
	if err != nil {
		t.Fatal(err)
	}
	viaMCP := structured(t, res).(map[string]any)
	for name, m := range map[string]map[string]any{"rest": viaREST, "mcp": viaMCP} {
		if m["has_reference"] != true || m["reference"] != nil {
			t.Errorf("%s: has_reference=%v reference=%v", name, m["has_reference"], m["reference"])
		}
	}
	b, _ := json.Marshal(viaMCP)
	if strings.Contains(string(b), "Synthetic Reference Title") {
		t.Errorf("reference leaked over MCP: %s", b)
	}
}
