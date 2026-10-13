package mcpgen

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/gate"
	"github.com/nicolasacchi/metamusic/internal/idem"
	"github.com/nicolasacchi/metamusic/internal/ratelimit"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type createIn struct {
	Key  string `header:"Idempotency-Key" required:"true" doc:"Idempotency key."`
	Body struct {
		Text string `json:"text" doc:"Text."`
	}
}

type createOut struct {
	Body struct {
		N    int32  `json:"n"`
		Text string `json:"text"`
	}
}

// gatedRig is a tiny API with one idempotent create op behind the real gate,
// store, audit and idempotency stores.
type gatedRig struct {
	st     *store.Store
	tokens *auth.TokenStore
	a      huma.API
	gated  http.Handler
	calls  atomic.Int32
}

func newGatedRig(t *testing.T) *gatedRig {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := func() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC) }
	r := &gatedRig{st: st, tokens: auth.NewTokenStore(st, []byte("0123456789abcdef0123456789abcdef"), now)}
	mux := http.NewServeMux()
	cfg := huma.DefaultConfig("t", "0.0.1")
	cfg.OpenAPIPath, cfg.DocsPath, cfg.SchemasPath, cfg.CreateHooks = "/api/openapi", "", "", nil
	r.a = humago.New(mux, cfg)
	meta := api.Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "none:test", Idempotency: "create"}
	api.Register(r.a, huma.Operation{OperationID: "t_create", Method: "POST", Path: "/api/t/create", Summary: "S", Description: "D"}, meta,
		func(ctx context.Context, in *createIn) (*createOut, error) {
			out := &createOut{}
			out.Body.N = r.calls.Add(1)
			out.Body.Text = in.Body.Text
			return out, nil
		})
	humanMeta := api.Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "exclude:human_only", UI: "none:test", HumanOnly: true}
	api.Register(r.a, huma.Operation{OperationID: "h_only", Method: "GET", Path: "/human/api/h", Summary: "S", Description: "D"}, humanMeta,
		func(ctx context.Context, _ *struct{}) (*createOut, error) { return &createOut{}, nil })
	genericMeta := meta
	genericMeta.MCP = "generic"
	api.Register(r.a, huma.Operation{OperationID: "g_create", Method: "POST", Path: "/api/t/gcreate", Summary: "S", Description: "D"}, genericMeta,
		func(ctx context.Context, in *createIn) (*createOut, error) {
			out := &createOut{}
			out.Body.N = r.calls.Add(1)
			return out, nil
		})
	ids := 0
	r.gated = gate.New(gate.Options{API: r.a, Inner: mux, Audit: audit.NewStore(st, func() string {
		ids++
		return "0190" + strings.Repeat("0", 4) + "-0000-7000-8000-" + strings.Repeat("0", 11) + string(rune('a'+ids))
	}), Idem: idem.NewStore(st, now), Limiter: ratelimit.New(ratelimit.Default.Scaled(1000), now), Now: now})
	return r
}

func (r *gatedRig) agent(t *testing.T) (auth.Principal, string) {
	t.Helper()
	tok, id, err := r.tokens.Create(context.Background(), "agent-x", []auth.Scope{auth.ScopeRead, auth.ScopeWrite})
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.tokens.Verify(context.Background(), tok)
	if err != nil || p.ID != id {
		t.Fatalf("verify: %v", err)
	}
	return p, tok
}

// httpSession serves the MCP handler (stateless streamable HTTP) behind a stub
// that injects the principal, as the real auth middleware does.
func (r *gatedRig) httpSession(t *testing.T, p auth.Principal, ua string) *mcp.ClientSession {
	t.Helper()
	srv, err := NewServer(r.a, r.gated, "v0")
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(srv)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		h.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), p)))
	}))
	t.Cleanup(ts.Close)
	sess, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{
			Endpoint: ts.URL, MaxRetries: -1,
			HTTPClient: &http.Client{Transport: uaRT(ua)},
		}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

type uaRT string

func (u uaRT) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	if u != "" {
		req.Header.Set("User-Agent", string(u))
	}
	return http.DefaultTransport.RoundTrip(req)
}

// stdioSession connects an in-memory client to NewStdioServer.
func (r *gatedRig) stdioSession(t *testing.T, principal func(context.Context) (auth.Principal, error), info *mcp.Implementation) *mcp.ClientSession {
	t.Helper()
	srv, err := NewStdioServer(r.a, r.gated, StdioOptions{Principal: principal, Version: "v0"})
	if err != nil {
		t.Fatal(err)
	}
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	sess, err := mcp.NewClient(info, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func (r *gatedRig) auditRows(t *testing.T) []map[string]any {
	t.Helper()
	rows, err := r.st.R.Query(`SELECT op, transport, client, status, idempotency, COALESCE(error_code,'') FROM audit_log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var op, tr, cl, idm, ec string
		var status int
		if err := rows.Scan(&op, &tr, &cl, &status, &idm, &ec); err != nil {
			t.Fatal(err)
		}
		out = append(out, map[string]any{"op": op, "transport": tr, "client": cl, "status": status, "idempotency": idm, "error_code": ec})
	}
	return out
}

func tool(t *testing.T, s *mcp.ClientSession, name string) *mcp.Tool {
	t.Helper()
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		if tl.Name == name {
			return tl
		}
	}
	t.Fatalf("no tool %q", name)
	return nil
}

func TestSchema_IdempotencyKeyOptionalForMCP(t *testing.T) {
	r := newGatedRig(t)
	p, _ := r.agent(t)
	tl := tool(t, r.httpSession(t, p, ""), "t_create")
	raw, _ := json.Marshal(tl.InputSchema)
	var in struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	prop, ok := in.Properties["idempotency_key"]
	if !ok {
		t.Fatalf("idempotency_key missing from %s", raw)
	}
	for _, n := range in.Required {
		if n == "idempotency_key" {
			t.Errorf("idempotency_key must not be required: %s", raw)
		}
	}
	if !strings.HasPrefix(prop.Description, "Optional. Omit it and the server derives a key") ||
		!strings.Contains(prop.Description, "explicit keys are remembered for 24 hours") {
		t.Errorf("description %q", prop.Description)
	}
	g := specsOf(t, r.a)
	for _, id := range []string{"t_create"} {
		s := g[id]
		if s == nil {
			t.Fatalf("no spec %s", id)
		}
		// specsOf builds without metadata; apply the same step NewServer does.
		if err := s.markIdempotent(); err != nil {
			t.Fatal(err)
		}
		for _, p := range s.Params {
			if p.Arg == "idempotency_key" && p.Required {
				t.Errorf("toolSpec.Params[idempotency_key].Required must be false")
			}
		}
	}
}

func TestSchema_IdempotentOpNeedsHeader(t *testing.T) {
	s := &toolSpec{ID: "x", Input: map[string]any{"properties": map[string]any{}}}
	if err := s.markIdempotent(); err == nil {
		t.Fatal("a create op without the header parameter must be refused")
	}
}

func createArgs(text string, extra map[string]any) map[string]any {
	m := map[string]any{"text": text}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestDispatch_DerivedKeyBeforeRequiredCheck(t *testing.T) {
	r := newGatedRig(t)
	p, _ := r.agent(t)
	res := call(t, r.httpSession(t, p, ""), "t_create", createArgs("a", nil))
	if res.IsError {
		t.Fatalf("missing key must be derived, got %s", text(res))
	}
	var n int
	if err := r.st.R.QueryRow(`SELECT COUNT(*) FROM idempotency WHERE derived=1 AND key LIKE 'mcp:%' AND principal_key=?`, p.Key()).Scan(&n); err != nil || n != 1 {
		t.Fatalf("derived rows = %d, %v", n, err)
	}
}

func TestDispatch_DerivedIdempotencyKey(t *testing.T) {
	ctx := context.Background()
	spec := &toolSpec{ID: "op", IdemCreate: true}
	p := auth.Principal{Kind: auth.KindAgent, ID: "t1"}
	ctx = auth.WithPrincipal(ctx, p)
	key := func(args string) (string, bool, string) {
		c, out := deriveKey(ctx, spec, json.RawMessage(args))
		k, derived := idem.DerivedFrom(c)
		var m map[string]string
		_ = json.Unmarshal(out, &m)
		return k, derived, m["idempotency_key"]
	}
	k1, d1, a1 := key(`{"a":1,"b":[1,2],"c":{"x":1,"y":2}}`)
	k2, _, _ := key(`{"c":{"y":2,"x":1},"b":[1,2],"a":1}`)
	if !d1 || k1 == "" || k1 != k2 || a1 != k1 {
		t.Fatalf("same args, any order, must derive one key: %q %q %q", k1, k2, a1)
	}
	if k3, _, _ := key(`{"a":2}`); k3 == k1 {
		t.Error("different args must derive different keys")
	}
	if kx, _, _ := key(`{"a":1,"b":[1,2],"c":{"x":1,"y":2},"idempotency_key":null}`); kx != k1 {
		t.Error("a null key is absent")
	}
	k4, d4, a4 := key(`{"a":1,"idempotency_key":"mine-1"}`)
	if d4 || k4 != "" || a4 != "mine-1" {
		t.Errorf("explicit key passes through with derived=0: %q %v %q", k4, d4, a4)
	}
}

func TestMCP_DerivedIdempotency(t *testing.T) {
	r := newGatedRig(t)
	p, _ := r.agent(t)
	s := r.httpSession(t, p, "")
	a := call(t, s, "t_create", createArgs("same", nil))
	b := call(t, s, "t_create", createArgs("same", nil))
	if a.IsError || b.IsError || r.calls.Load() != 1 || text(a) != text(b) {
		t.Fatalf("identical calls must replay: calls=%d %s | %s", r.calls.Load(), text(a), text(b))
	}
	call(t, s, "t_create", createArgs("same", map[string]any{"idempotency_key": "k-1"}))
	call(t, s, "t_create", createArgs("same", map[string]any{"idempotency_key": "k-2"}))
	if got := r.calls.Load(); got != 3 {
		t.Fatalf("two distinct explicit keys must run twice: calls=%d", got)
	}
	// call_operation behaves the same.
	g1 := call(t, s, "call_operation", map[string]any{"operation_id": "g_create", "arguments": map[string]any{"text": "x"}})
	g2 := call(t, s, "call_operation", map[string]any{"operation_id": "g_create", "arguments": map[string]any{"text": "x"}})
	if g1.IsError || text(g1) != text(g2) || r.calls.Load() != 4 {
		t.Fatalf("generic create must derive too: calls=%d", r.calls.Load())
	}
}

func TestHTTPServer_MCPDispatchesThroughGate(t *testing.T) {
	r := newGatedRig(t)
	p, _ := r.agent(t)
	s := r.httpSession(t, p, "")
	call(t, s, "t_create", createArgs("gate", nil))
	call(t, s, "t_create", createArgs("gate", nil))
	var derived int
	if err := r.st.R.QueryRow(`SELECT derived FROM idempotency WHERE principal_key=?`, p.Key()).Scan(&derived); err != nil || derived != 1 {
		t.Fatalf("idempotency row derived=%d %v", derived, err)
	}
	rows := r.auditRows(t)
	if len(rows) != 2 || rows[0]["transport"] != "mcp" || rows[0]["op"] != "t_create" ||
		rows[0]["idempotency"] != "stored" || rows[1]["idempotency"] != "replayed" {
		t.Fatalf("audit rows %v", rows)
	}
}

func TestDispatch_ClientLabel_Stateless(t *testing.T) {
	r := newGatedRig(t)
	p, _ := r.agent(t)
	call(t, r.httpSession(t, p, "test-client/1.0 (x)"), "t_create", createArgs("ua", nil))
	rows := r.auditRows(t)
	if len(rows) != 1 || rows[0]["client"] != "test-client/1.0" {
		t.Fatalf("audit rows %v", rows)
	}
}

func TestDispatch_ClientLabel_Initialized(t *testing.T) {
	r := newGatedRig(t)
	p, _ := r.agent(t)
	s := r.stdioSession(t, func(context.Context) (auth.Principal, error) { return p, nil }, &mcp.Implementation{Name: "c", Version: "2"})
	call(t, s, "t_create", createArgs("ci", nil))
	rows := r.auditRows(t)
	if len(rows) != 1 || rows[0]["client"] != "c/2" || rows[0]["transport"] != "stdio" {
		t.Fatalf("audit rows %v", rows)
	}
}

func TestNewStdioServer_RequiresPrincipal(t *testing.T) {
	r := newGatedRig(t)
	if _, err := NewStdioServer(r.a, r.gated, StdioOptions{}); err == nil {
		t.Fatal("a nil Principal must be refused")
	}
}

func TestStdio_PrincipalErrors(t *testing.T) {
	r := newGatedRig(t)
	for _, tc := range []struct {
		err  error
		code string
	}{
		{auth.ErrInvalidToken, "UNAUTHENTICATED"},
		{context.DeadlineExceeded, "INTERNAL"},
	} {
		s := r.stdioSession(t, func(context.Context) (auth.Principal, error) { return auth.Principal{}, tc.err },
			&mcp.Implementation{Name: "c", Version: "1"})
		res := call(t, s, "t_create", createArgs("x", nil))
		if !res.IsError || !strings.Contains(text(res), tc.code) {
			t.Errorf("%v: %+v", tc.err, res)
		}
	}
	if r.calls.Load() != 0 {
		t.Error("no handler may run without a principal")
	}
}

func TestDescribeOperation_ShowsGateMeta(t *testing.T) {
	r := newGatedRig(t)
	p, _ := r.agent(t)
	res := call(t, r.httpSession(t, p, ""), "describe_operation", map[string]any{"operation_id": "t_create"})
	var m map[string]any
	if err := json.Unmarshal([]byte(text(res)), &m); err != nil || m["idempotency"] != "create" {
		t.Fatalf("%v %s", err, text(res))
	}
}

func TestStdio_HumanOnlyOpsAreNotCallable(t *testing.T) {
	r := newGatedRig(t)
	p, _ := r.agent(t)
	s := r.stdioSession(t, func(context.Context) (auth.Principal, error) { return p, nil }, &mcp.Implementation{Name: "c", Version: "1"})
	tools, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools.Tools {
		if tl.Name == "h_only" {
			t.Error("a human-only op must not be listed")
		}
	}
	res := call(t, s, "call_operation", map[string]any{"operation_id": "h_only"})
	if !res.IsError || !strings.Contains(text(res), "NOT_FOUND") {
		t.Errorf("call_operation must refuse it: %+v", res)
	}
}
