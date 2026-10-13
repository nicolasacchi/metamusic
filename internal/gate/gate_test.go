package gate_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/gate"
	"github.com/nicolasacchi/metamusic/internal/idem"
	"github.com/nicolasacchi/metamusic/internal/ratelimit"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type idemHeader struct {
	IdempotencyKey string `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"255" pattern:"^[!-~]+$"`
}

type createIn struct {
	idemHeader
	Body struct {
		Name string `json:"name" minLength:"1"`
	}
}

type okOut struct {
	Body struct {
		OK    bool   `json:"ok"`
		Name  string `json:"name,omitempty"`
		Name2 string `json:"name2,omitempty"`
	}
}

type secretOut struct {
	Body struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
}

type writeIn struct {
	Body struct {
		Name string `json:"name,omitempty"`
	}
}

type rig struct {
	t       *testing.T
	clk     *clock
	st      *store.Store
	audit   *audit.Store
	idem    *idem.Store
	limiter *ratelimit.Limiter
	h       http.Handler
	ticks   chan time.Time

	handled  atomic.Int64 // handler executions of create ops
	consumed atomic.Int64
	block    func() // called inside t_create when set
	counter  atomic.Int64
}

type rigOpts struct {
	limits  ratelimit.Config
	maxBody int64
}

func newRig(t *testing.T, o rigOpts) *rig {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r := &rig{t: t, st: st, clk: &clock{t: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}, ticks: make(chan time.Time)}
	n := 0
	var mu sync.Mutex
	r.audit = audit.NewStore(st, func() string { mu.Lock(); n++; defer mu.Unlock(); return fmt.Sprintf("a%06d", n) })
	r.idem = idem.NewStore(st, r.clk.Now)
	if o.limits == nil {
		o.limits = ratelimit.Default.Scaled(1000)
	}
	r.limiter = ratelimit.New(o.limits, r.clk.Now)

	mux := http.NewServeMux()
	a := humago.New(mux, api.HumaConfig())
	reg := func(id, method, path string, m api.Meta) huma.Operation {
		return huma.Operation{OperationID: id, Method: method, Path: path, Summary: id, Description: id}
	}
	api.Register(a, reg("t_read", "GET", "/api/t/read", api.Meta{}), api.Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "none:test"},
		func(ctx context.Context, _ *struct{}) (*okOut, error) { o := &okOut{}; o.Body.OK = true; return o, nil })
	api.Register(a, reg("t_write", "PATCH", "/api/t/write", api.Meta{}), api.Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "none:test"},
		func(ctx context.Context, in *writeIn) (*okOut, error) { o := &okOut{}; o.Body.OK = true; return o, nil })
	api.Register(a, reg("t_search", "POST", "/api/t/search", api.Meta{}), api.Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "none:test"},
		func(ctx context.Context, in *writeIn) (*okOut, error) { o := &okOut{}; o.Body.OK = true; return o, nil })
	createOp := reg("t_create", "POST", "/api/t/create", api.Meta{})
	createOp.DefaultStatus = 201
	api.Register(a, createOp, api.Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "none:test", Idempotency: "create"},
		func(ctx context.Context, in *createIn) (*okOut, error) {
			r.handled.Add(1)
			if r.block != nil {
				r.block()
			}
			id := fmt.Sprintf("new-%d", r.counter.Add(1))
			audit.NoteCreated(ctx, id)
			o := &okOut{}
			o.Body.OK, o.Body.Name, o.Body.Name2 = true, in.Body.Name, in.Body.Name
			return o, nil
		})
	heavyOp := reg("t_heavy", "POST", "/api/t/heavy", api.Meta{})
	heavyOp.DefaultStatus = 201
	api.Register(a, heavyOp, api.Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "none:test", Idempotency: "create", Rate: "heavy"},
		func(ctx context.Context, in *createIn) (*okOut, error) {
			r.handled.Add(1)
			o := &okOut{}
			o.Body.OK = true
			return o, nil
		})
	secretOp := reg("t_secret", "POST", "/human/api/t/secret", api.Meta{})
	secretOp.DefaultStatus = 201
	api.Register(a, secretOp, api.Meta{Scope: auth.ScopeAdmin, Cost: "free", MCP: "exclude:test", UI: "none:test", HumanOnly: true,
		Idempotency: "create", SecretFields: []string{"token"}},
		func(ctx context.Context, in *createIn) (*secretOut, error) {
			o := &secretOut{}
			o.Body.ID, o.Body.Token = "tok-1", "canary-secret-value"
			return o, nil
		})
	consumeOp := reg("t_consume", "POST", "/api/t/consume", api.Meta{})
	consumeOp.DefaultStatus = 201
	api.Register(a, consumeOp, api.Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "none:test", Idempotency: "create"},
		func(ctx context.Context, in *createIn) (*okOut, error) {
			if r.consumed.Add(1) > 1 {
				return nil, api.Err(409, "ALREADY_CONSUMED", "single-use row already consumed", nil)
			}
			o := &okOut{}
			o.Body.OK = true
			return o, nil
		})

	r.h = gate.New(gate.Options{API: a, Inner: mux, Audit: r.audit, Idem: r.idem, Limiter: r.limiter, Now: r.clk.Now, MaxBody: o.maxBody,
		Ticker: func(time.Duration) (<-chan time.Time, func()) { return r.ticks, func() {} }})
	return r
}

var (
	allScopes = []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate, auth.ScopeAdmin}
	human     = auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice", Scopes: allScopes, Via: "proxy"}
	device    = auth.Principal{Kind: auth.KindDevice, ID: "d1", Name: "phone", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "device"}
)

func agent(id string, scopes ...auth.Scope) auth.Principal {
	if len(scopes) == 0 {
		scopes = []auth.Scope{auth.ScopeRead, auth.ScopeWrite}
	}
	return auth.Principal{Kind: auth.KindAgent, ID: id, Name: "bot-" + id, Scopes: scopes, Via: "token"}
}

type req struct {
	p      auth.Principal
	method string
	path   string
	body   string
	hdr    map[string]string
	ctx    func(context.Context) context.Context
}

func (r *rig) do(q req) *httptest.ResponseRecorder {
	r.t.Helper()
	hr := httptest.NewRequest(q.method, q.path, strings.NewReader(q.body))
	if q.body != "" {
		hr.Header.Set("Content-Type", "application/json")
	}
	for k, v := range q.hdr {
		hr.Header.Set(k, v)
	}
	ctx := auth.WithPrincipal(hr.Context(), q.p)
	if q.ctx != nil {
		ctx = q.ctx(ctx)
	}
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, hr.WithContext(ctx))
	return w
}

func create(p auth.Principal, key, body string) req {
	h := map[string]string{}
	if key != "" {
		h["Idempotency-Key"] = key
	}
	return req{p: p, method: "POST", path: "/api/t/create", body: body, hdr: h}
}

func (r *rig) rows() []audit.Row {
	r.t.Helper()
	p, err := r.audit.List(context.Background(), audit.Filter{Limit: 200})
	if err != nil {
		r.t.Fatal(err)
	}
	return p.Items
}

func code(w *httptest.ResponseRecorder) string {
	var v struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	return v.Code
}

func idemCount(r *rig) int {
	var n int
	_ = r.st.R.QueryRow(`SELECT COUNT(*) FROM idempotency`).Scan(&n)
	return n
}

func TestIdempotency_RESTRequiresKey(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	w := r.do(create(agent("t1"), "", `{"name":"x"}`))
	if w.Code != 428 || code(w) != "IDEMPOTENCY_KEY_REQUIRED" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	rows := r.rows()
	if len(rows) != 1 || rows[0].Idempotency != "missing" || rows[0].ErrorCode != "IDEMPOTENCY_KEY_REQUIRED" || rows[0].Status != 428 {
		t.Fatalf("%+v", rows)
	}
	if r.handled.Load() != 0 {
		t.Fatal("handler ran")
	}
}

func TestIdempotency_ReplayAndConflict(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	a := agent("t1")
	w1 := r.do(create(a, "k1", `{"name":"x"}`))
	if w1.Code != 201 {
		t.Fatalf("%d %s", w1.Code, w1.Body)
	}
	w2 := r.do(create(a, "k1", `{ "name": "x" }`))
	if w2.Code != 201 || w2.Body.String() != w1.Body.String() || w2.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d %q hdr=%v", w2.Code, w2.Body, w2.Header())
	}
	if w1.Header().Get("Idempotent-Replayed") != "" {
		t.Fatal("first response must not be marked replayed")
	}
	if w2.Header().Get("Content-Type") != w1.Header().Get("Content-Type") {
		t.Fatal("content type differs on replay")
	}
	if r.handled.Load() != 1 {
		t.Fatalf("handler ran %d times", r.handled.Load())
	}
	w3 := r.do(create(a, "k1", `{"name":"other"}`))
	if w3.Code != 409 || code(w3) != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("%d %s", w3.Code, w3.Body)
	}
	w4 := r.do(create(agent("t2"), "k1", `{"name":"other"}`))
	if w4.Code != 201 || w4.Header().Get("Idempotent-Replayed") != "" {
		t.Fatalf("other principal must be independent: %d %s", w4.Code, w4.Body)
	}
	rows := r.rows() // newest first: w4, w3, w2, w1
	if rows[2].Idempotency != "replayed" || rows[1].Idempotency != "conflict" || rows[3].Idempotency != "stored" {
		t.Fatalf("audit idempotency: %q %q %q", rows[3].Idempotency, rows[2].Idempotency, rows[1].Idempotency)
	}
	if len(rows[2].CreatedIDs) != 0 || len(rows[3].CreatedIDs) != 1 {
		t.Fatalf("created ids: replay=%v first=%v", rows[2].CreatedIDs, rows[3].CreatedIDs)
	}
}

func TestIdempotency_InProgress(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	entered, release := make(chan struct{}), make(chan struct{})
	r.block = func() { close(entered); <-release }
	a := agent("t1")
	done := make(chan *httptest.ResponseRecorder)
	go func() { done <- r.do(create(a, "k", `{"name":"x"}`)) }()
	<-entered
	w := r.do(create(a, "k", `{"name":"x"}`))
	if w.Code != 409 || code(w) != "IDEMPOTENCY_IN_PROGRESS" || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("%d %s %v", w.Code, w.Body, w.Header())
	}
	close(release)
	if first := <-done; first.Code != 201 {
		t.Fatalf("first: %d", first.Code)
	}
	if r.handled.Load() != 1 {
		t.Fatalf("handler ran %d times", r.handled.Load())
	}
}

func TestIdempotency_PendingLeaseReclaimed(t *testing.T) {
	t.Parallel()
	const body = `{"name":"x"}`
	pending := func(t *testing.T) *rig {
		r := newRig(t, rigOpts{})
		sha, _ := idem.RequestSHA("t_create", "POST", "/api/t/create", url.Values{}, []byte(body))
		// A crash after Begin: no Complete, no Release.
		b, err := r.idem.Begin(context.Background(), agent("t1").Key(), "k", "t_create", sha, false)
		if err != nil || b.Outcome != idem.Proceed {
			t.Fatalf("seed: %v %v", b, err)
		}
		return r
	}
	t.Run("same body", func(t *testing.T) {
		t.Parallel()
		r := pending(t)
		r.clk.Advance(time.Minute)
		if w := r.do(create(agent("t1"), "k", body)); w.Code != 409 || code(w) != "IDEMPOTENCY_IN_PROGRESS" {
			t.Fatalf("+1m: %d %s", w.Code, w.Body)
		}
		r.clk.Advance(time.Minute + time.Second)
		if w := r.do(create(agent("t1"), "k", body)); w.Code != 201 {
			t.Fatalf("+2m01s: %d %s", w.Code, w.Body)
		}
		if r.handled.Load() != 1 {
			t.Fatal("handler should have run once after reclaim")
		}
	})
	t.Run("other body", func(t *testing.T) {
		t.Parallel()
		r := pending(t)
		r.clk.Advance(2*time.Minute + time.Second)
		if w := r.do(create(agent("t1"), "k", `{"name":"y"}`)); w.Code != 409 || code(w) != "IDEMPOTENCY_CONFLICT" {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	})
}

func TestIdempotency_LeaseExtendedForLongHandler(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	entered, release := make(chan struct{}), make(chan struct{})
	r.block = func() { close(entered); <-release }
	a := agent("t1")
	done := make(chan *httptest.ResponseRecorder)
	go func() { done <- r.do(create(a, "k", `{"name":"x"}`)) }()
	<-entered
	lease := func() string {
		var s string
		if err := r.st.R.QueryRow(`SELECT lease_until FROM idempotency WHERE key='k'`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	prev := lease()
	for i := 0; i < 3; i++ {
		r.clk.Advance(time.Minute)
		r.ticks <- r.clk.Now()
		r.ticks <- r.clk.Now() // barrier: the extension above has finished once this is received
		if cur := lease(); cur <= prev {
			t.Fatalf("tick %d: lease_until did not move (%s -> %s)", i+1, prev, cur)
		} else {
			prev = cur
		}
	}
	if w := r.do(create(a, "k", `{"name":"x"}`)); w.Code != 409 || code(w) != "IDEMPOTENCY_IN_PROGRESS" {
		t.Fatalf("+3m: %d %s", w.Code, w.Body)
	}
	close(release)
	<-done
}

func TestIdempotency_TTLs(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	a := agent("t1")
	r.do(create(a, "hdr", `{"name":"x"}`))
	r.clk.Advance(23*time.Hour + 59*time.Minute)
	if w := r.do(create(a, "hdr", `{"name":"x"}`)); w.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("23h59m must replay: %d", w.Code)
	}
	r.clk.Advance(2 * time.Minute)
	if w := r.do(create(a, "hdr", `{"name":"x"}`)); w.Code != 201 || w.Header().Get("Idempotent-Replayed") != "" {
		t.Fatalf("24h01m must re-execute: %d", w.Code)
	}
	if r.handled.Load() != 2 {
		t.Fatalf("handled %d", r.handled.Load())
	}

	// Derived keys (MCP) expire after ten minutes.
	r2 := newRig(t, rigOpts{})
	withDerived := func(w req) req {
		w.ctx = func(c context.Context) context.Context {
			return idem.WithDerived(reqinfo.WithTransport(c, reqinfo.MCP), "mcp:abc")
		}
		return w
	}
	r2.do(withDerived(create(a, "", `{"name":"x"}`)))
	r2.clk.Advance(9*time.Minute + 59*time.Second)
	if w := r2.do(withDerived(create(a, "", `{"name":"x"}`))); w.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("9m59s must replay: %d", w.Code)
	}
	r2.clk.Advance(2 * time.Second)
	if w := r2.do(withDerived(create(a, "", `{"name":"x"}`))); w.Header().Get("Idempotent-Replayed") != "" || w.Code != 201 {
		t.Fatalf("10m01s must re-execute: %d", w.Code)
	}
	if r2.handled.Load() != 2 {
		t.Fatalf("handled %d", r2.handled.Load())
	}
	var derived int
	_ = r2.st.R.QueryRow(`SELECT derived FROM idempotency`).Scan(&derived)
	if derived != 1 {
		t.Fatal("row must be marked derived")
	}
}

func TestIdempotency_ErrorsNotStored(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	a := agent("t1")
	if w := r.do(create(a, "k", `{"name":""}`)); w.Code != 422 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if idemCount(r) != 0 {
		t.Fatal("error left an idempotency row")
	}
	if w := r.do(create(a, "k", `{"name":"fixed"}`)); w.Code != 201 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestIdempotency_CheckedBeforeConsumption(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	a := agent("t1")
	call := func() *httptest.ResponseRecorder {
		q := create(a, "k", `{"name":"x"}`)
		q.path = "/api/t/consume"
		return r.do(q)
	}
	w1, w2 := call(), call()
	if w1.Code != 201 || w2.Code != 201 || w2.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("%d %d", w1.Code, w2.Code)
	}
	if r.consumed.Load() != 1 {
		t.Fatalf("single-use row consumed %d times", r.consumed.Load())
	}
}

func TestIdempotency_SecretFieldsRedacted(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	q := req{p: human, method: "POST", path: "/human/api/t/secret", body: `{"name":"x"}`, hdr: map[string]string{"Idempotency-Key": "k"}}
	w1 := r.do(q)
	if w1.Code != 201 || !strings.Contains(w1.Body.String(), "canary-secret-value") {
		t.Fatalf("first response must carry the token: %d %s", w1.Code, w1.Body)
	}
	var stored string
	if err := r.st.R.QueryRow(`SELECT CAST(response AS TEXT) FROM idempotency`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "canary-secret-value") || !strings.Contains(stored, `"token":null`) {
		t.Fatalf("stored row = %s", stored)
	}
	w2 := r.do(q)
	if strings.Contains(w2.Body.String(), "canary-secret-value") || !strings.Contains(w2.Body.String(), `"token":null`) {
		t.Fatalf("replay = %s", w2.Body)
	}
}

func TestRateLimit_GateRefusal(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{limits: ratelimit.Config{ratelimit.Agent: {PerMinute: 60, Burst: 2}, ratelimit.Human: {PerMinute: 60, Burst: 2}}})
	a := agent("t1")
	rd := req{p: a, method: "GET", path: "/api/t/read"}
	r.do(rd)
	r.do(rd)
	w := r.do(rd)
	if w.Code != 429 || code(w) != "RATE_LIMITED" || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("%d %s %v", w.Code, w.Body, w.Header())
	}
	var body struct {
		Details map[string]any `json:"details"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Details["bucket"] != "agent" || body.Details["retry_after_s"] != float64(1) {
		t.Fatalf("details = %v", body.Details)
	}
}

func TestGate_Order(t *testing.T) {
	t.Parallel()
	// The limiter runs before Authorize: a 403 consumes a rate token.
	r := newRig(t, rigOpts{limits: ratelimit.Config{ratelimit.Agent: {PerMinute: 60, Burst: 1}, ratelimit.Human: {PerMinute: 60, Burst: 1}}})
	ro := agent("t1", auth.ScopeRead)
	if w := r.do(req{p: ro, method: "PATCH", path: "/api/t/write", body: `{}`}); w.Code != 403 || code(w) != "FORBIDDEN_SCOPE" {
		t.Fatalf("first: %d %s", w.Code, w.Body)
	}
	if w := r.do(req{p: ro, method: "GET", path: "/api/t/read"}); w.Code != 429 {
		t.Fatalf("refused calls must consume the token bucket: %d", w.Code)
	}

	// Rate limit comes before idempotency: a 429 creates no row and hides the missing key.
	if w := r.do(create(agent("t2"), "k0", `{"name":"x"}`)); w.Code != 201 {
		t.Fatalf("%d", w.Code)
	}
	before := idemCount(r)
	if w := r.do(create(agent("t2"), "k1", `{"name":"x"}`)); w.Code != 429 {
		t.Fatalf("want 429, got %d %s", w.Code, w.Body)
	}
	if w := r.do(create(agent("t2"), "", `{"name":"x"}`)); w.Code != 429 {
		t.Fatalf("limiter must run before the key check: %d", w.Code)
	}
	if idemCount(r) != before {
		t.Fatal("a 429 created an idempotency row")
	}

	// Idempotency comes before the handler: the handler count never moves on refusals.
	if r.handled.Load() != 1 {
		t.Fatalf("handler ran %d times", r.handled.Load())
	}
	// Anonymous: 401 before anything else.
	if w := r.do(req{p: auth.Principal{Kind: auth.KindAnonymous}, method: "GET", path: "/api/t/read"}); w.Code != 401 {
		t.Fatalf("%d", w.Code)
	}
}

func TestGate_BodyCap(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	big := `{"name":"` + strings.Repeat("a", 9<<20) + `"}`
	w := r.do(req{p: agent("t1"), method: "PATCH", path: "/api/t/write", body: big})
	if w.Code != 413 || code(w) != "PAYLOAD_TOO_LARGE" {
		t.Fatalf("%d %.200s", w.Code, w.Body)
	}
	rows := r.rows()
	if len(rows) != 1 || rows[0].Status != 413 || rows[0].ErrorCode != "PAYLOAD_TOO_LARGE" {
		t.Fatalf("%+v", rows)
	}
	// A create op is capped too, before the key check.
	if w := r.do(create(human, "k", big)); w.Code != 413 {
		t.Fatalf("create: %d", w.Code)
	}
	// An unaudited human read with a large body is streamed untouched.
	before := len(r.rows())
	if w := r.do(req{p: human, method: "GET", path: "/api/t/read", body: big}); w.Code != 200 {
		t.Fatalf("human read: %d", w.Code)
	}
	if n := len(r.rows()); n != before {
		t.Fatalf("human read must not be audited (%d rows, was %d)", n, before)
	}
}

func TestGate_TransportLabels(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	ua := map[string]string{"User-Agent": "test-client/1.0 (x)"}
	merge := func(m map[string]string, kv ...string) map[string]string {
		out := map[string]string{}
		for k, v := range m {
			out[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			out[kv[i]] = kv[i+1]
		}
		return out
	}
	write := func(p auth.Principal, path string, h map[string]string, ctx func(context.Context) context.Context) {
		r.do(req{p: p, method: "PATCH", path: path, body: `{}`, hdr: h, ctx: ctx})
	}
	write(human, "/api/t/write", merge(ua, "X-MM-Client", "spa"), nil)
	write(device, "/api/t/write?mm_client=spa", nil, nil)
	write(agent("t1"), "/api/t/write", merge(ua, "X-MM-Client", "spa"), nil)
	write(agent("t1"), "/api/t/write?mm_client=spa", nil, nil)
	write(agent("t1"), "/api/t/write", nil, func(c context.Context) context.Context { return reqinfo.WithTransport(c, reqinfo.MCP) })
	write(agent("t1"), "/api/t/write", nil, func(c context.Context) context.Context {
		return reqinfo.WithClient(reqinfo.WithTransport(c, reqinfo.Stdio), "c/2")
	})
	write(human, "/api/t/write", nil, nil)
	rows := r.rows() // newest first
	want := []struct{ transport, client string }{
		{"rest", ""}, {"stdio", "c/2"}, {"mcp", ""}, {"rest", ""}, {"rest", "test-client/1.0"}, {"spa", ""}, {"spa", "test-client/1.0"},
	}
	if len(rows) != len(want) {
		t.Fatalf("%d rows", len(rows))
	}
	for i, w := range want {
		g := rows[i]
		if g.Transport != w.transport || g.Client != w.client {
			t.Errorf("row %d: transport=%q client=%q, want %+v", i, g.Transport, g.Client, w)
		}
	}
}

func TestAudit_CoverageMatrix(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	principals := map[string]auth.Principal{"human": human, "device": device, "agent": agent("t1", auth.ScopeRead, auth.ScopeWrite, auth.ScopeAdmin)}
	type opCase struct {
		name string
		mk   func(p auth.Principal, i int) req
		// whether a human or device principal's call is audited
		humanAudited bool
	}
	ops := []opCase{
		{"t_read", func(p auth.Principal, i int) req { return req{p: p, method: "GET", path: "/api/t/read"} }, false},
		{"t_write", func(p auth.Principal, i int) req {
			return req{p: p, method: "PATCH", path: "/api/t/write", body: `{"name":"canary-body-text"}`}
		}, true},
		{"t_create", func(p auth.Principal, i int) req {
			return create(p, fmt.Sprintf("k%d", i), `{"name":"canary-body-text"}`)
		}, true},
		{"t_search", func(p auth.Principal, i int) req {
			return req{p: p, method: "POST", path: "/api/t/search", body: `{"name":"canary-body-text"}`}
		}, false},
		{"t_secret", func(p auth.Principal, i int) req {
			return req{p: p, method: "POST", path: "/human/api/t/secret", body: `{"name":"canary-body-text"}`, hdr: map[string]string{"Idempotency-Key": fmt.Sprintf("s%d", i)}}
		}, true},
	}
	i := 0
	for pname, p := range principals {
		for _, op := range ops {
			i++
			before := len(r.rows())
			w := r.do(op.mk(p, i))
			delta := len(r.rows()) - before
			want := 0
			if pname == "agent" || op.humanAudited {
				want = 1
			}
			if delta != want {
				t.Errorf("%s/%s: %d rows added (status %d), want %d", pname, op.name, delta, w.Code, want)
			}
		}
	}
	for _, row := range r.rows() {
		blob, _ := json.Marshal(row)
		if strings.Contains(string(blob), "canary-body-text") {
			t.Fatalf("row carries body bytes: %s", blob)
		}
		if row.ArgsSHA256 == "" || row.Transport == "" || row.Status == 0 {
			t.Errorf("incomplete row %+v", row)
		}
		if row.Op == "t_create" && row.Status == 201 && len(row.CreatedIDs) != 1 {
			t.Errorf("created ids missing: %+v", row)
		}
		if row.Op == "t_secret" && row.PrincipalKind != "human" && row.ErrorCode != "HUMAN_ONLY" {
			t.Errorf("non-human on human-only op must be refused: %+v", row)
		}
	}
}

func TestAudit_RefusalsRecorded(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{limits: ratelimit.Config{ratelimit.Agent: {PerMinute: 60, Burst: 3}}})
	ro := agent("t1", auth.ScopeRead)
	r.do(req{p: ro, method: "PATCH", path: "/api/t/write", body: `{}`})                                // 403, no token consumed
	r.do(create(agent("t2"), "", `{"name":"x"}`))                                                      // 428
	r.do(req{p: agent("t3"), method: "PATCH", path: "/api/t/write", body: strings.Repeat("a", 9<<20)}) // 413
	for i := 0; i < 4; i++ {
		r.do(req{p: agent("t4"), method: "GET", path: "/api/t/read"})
	}
	var codes []string
	for _, row := range r.rows() {
		if row.ErrorCode != "" {
			codes = append(codes, fmt.Sprintf("%d:%s", row.Status, row.ErrorCode))
		}
	}
	got := strings.Join(codes, " ")
	for _, want := range []string{"403:FORBIDDEN_SCOPE", "428:IDEMPOTENCY_KEY_REQUIRED", "413:PAYLOAD_TOO_LARGE", "429:RATE_LIMITED"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
}

func TestAudit_UnknownOpAgent(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	w := r.do(req{p: agent("t1"), method: "GET", path: "/api/nope"})
	if w.Code != 404 {
		t.Fatalf("%d", w.Code)
	}
	rows := r.rows()
	if len(rows) != 1 || rows[0].Op != "-" || rows[0].Status != 404 {
		t.Fatalf("%+v", rows)
	}
	r.do(req{p: human, method: "GET", path: "/api/nope"})
	if len(r.rows()) != 1 {
		t.Fatal("human unknown op must not be audited")
	}
}

func TestGate_PanicReleasesKey(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	r.block = func() { panic("boom") }
	func() {
		defer func() { _ = recover() }()
		r.do(create(agent("t1"), "k", `{"name":"x"}`))
	}()
	if idemCount(r) != 0 {
		t.Fatal("a panicking handler left its key pending")
	}
	rows := r.rows()
	if len(rows) != 1 || rows[0].Status != 500 {
		t.Fatalf("%+v", rows)
	}
}

func TestGate_HeavyBucket(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{limits: ratelimit.Default})
	a := agent("t1")
	for i := 0; i < 6; i++ {
		q := create(a, fmt.Sprintf("k%d", i), `{"name":"x"}`)
		q.path = "/api/t/heavy"
		if w := r.do(q); w.Code != 201 {
			t.Fatalf("heavy %d: %d %s", i, w.Code, w.Body)
		}
	}
	q := create(a, "k7", `{"name":"x"}`)
	q.path = "/api/t/heavy"
	w := r.do(q)
	if w.Code != 429 || !strings.Contains(w.Body.String(), "agent_heavy") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

func TestGate_NewRequiresMux(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("New must panic when Inner is not a mux")
		}
	}()
	gate.New(gate.Options{API: humago.New(http.NewServeMux(), api.HumaConfig()), Inner: http.NotFoundHandler()})
}

func TestIdempotency_InvalidKeyAndOverflow(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	for _, key := range []string{"has space", strings.Repeat("k", 256), "tab\tkey"} {
		w := r.do(create(agent("t1"), key, `{"name":"x"}`))
		if (w.Code != 422) || r.handled.Load() != 0 {
			t.Fatalf("key %q: %d %s", key, w.Code, w.Body)
		}
	}
	if idemCount(r) != 0 {
		t.Fatal("invalid keys must not create rows")
	}
	// A 2xx body above the storage cap is served but not stored (key released).
	r2 := newRig(t, rigOpts{})
	name := strings.Repeat("n", 600<<10)
	if w := r2.do(create(agent("t1"), "big", `{"name":"`+name+`"}`)); w.Code != 201 || w.Body.Len() < 1<<20 {
		t.Fatalf("%d %d", w.Code, w.Body.Len())
	}
	if idemCount(r2) != 0 {
		t.Fatal("oversized response must not be stored")
	}
}

type flushRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (f *flushRecorder) Flush() { f.flushed = true }

func TestGate_FlushPassesThrough(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/t/stream", func(rw http.ResponseWriter, _ *http.Request) { rw.(http.Flusher).Flush() })
	h := gate.New(gate.Options{API: humago.New(mux, api.HumaConfig()), Inner: mux, Audit: r.audit, Idem: r.idem, Limiter: r.limiter})
	hr := httptest.NewRequest("GET", "/api/t/stream", nil)
	w := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	h.ServeHTTP(w, hr.WithContext(auth.WithPrincipal(hr.Context(), human)))
	if !w.flushed {
		t.Fatal("Flush did not reach the underlying writer")
	}
}

func TestSec_UnmatchedAgentCallsAreRateLimited(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{limits: ratelimit.Config{ratelimit.Agent: {PerMinute: 60, Burst: 2}, ratelimit.Human: {PerMinute: 60, Burst: 2}}})
	got429 := false
	for i := 0; i < 20; i++ {
		if w := r.do(req{p: agent("t1"), method: "POST", path: "/api/nope", body: `{}`}); w.Code == 429 {
			got429 = true
		}
	}
	if !got429 {
		t.Fatal("unmatched agent calls were never rate limited")
	}
}

func TestSec_RefusedAgentCallsAreRateLimited(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{limits: ratelimit.Config{ratelimit.Agent: {PerMinute: 60, Burst: 2}, ratelimit.Human: {PerMinute: 60, Burst: 2}}})
	got429 := false
	for i := 0; i < 20; i++ {
		if w := r.do(req{p: agent("t1", auth.ScopeRead), method: "PATCH", path: "/api/t/write", body: `{}`}); w.Code == 429 {
			got429 = true
		}
	}
	if !got429 {
		t.Fatal("scope-refused agent calls were never rate limited")
	}
}
