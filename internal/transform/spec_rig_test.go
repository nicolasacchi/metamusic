package transform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/idem"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var (
	human  = auth.Principal{Kind: auth.KindHuman, ID: "op", Name: "op", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}
	agentA = auth.Principal{Kind: auth.KindAgent, ID: "tok-a", Name: "agent a", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}
	agentB = auth.Principal{Kind: auth.KindAgent, ID: "tok-b", Name: "agent b", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}
)

// rig is a store holding FixtureT1 and a Service over it with a movable clock.
type rig struct {
	t     *testing.T
	st    *store.Store
	fx    librarytest.T1Info
	svc   *Service
	now   *atomic.Pointer[time.Time]
	wakes *atomic.Int32
	ids   *atomic.Int64
	cfg   config.Config
}

var (
	tmplOnce sync.Once
	tmplPath string
	tmplFX   librarytest.T1Info
	tmplDir  string
)

// templateDB builds FixtureT1 once per test binary into a closed database file that every rig copies (the fixture costs about
// two seconds, a copy a few milliseconds).
func templateDB(t *testing.T) (string, librarytest.T1Info) {
	t.Helper()
	tmplOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mm-transform-tmpl-")
		if err != nil {
			t.Fatal(err)
		}
		tmplDir = dir
		tmplPath = filepath.Join(dir, "t1.db")
		st, err := store.Open(context.Background(), tmplPath)
		if err != nil {
			t.Fatal(err)
		}
		tmplFX = librarytest.FixtureT1(t, st, librarytest.FixedNow)
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	})
	if tmplPath == "" {
		t.Fatal("template database failed to build")
	}
	return tmplPath, tmplFX
}

func (r *rig) clock() time.Time { return *r.now.Load() }

func (r *rig) advance(d time.Duration) {
	n := r.clock().Add(d)
	r.now.Store(&n)
}

func testConfig(dir string) config.Config {
	return config.Config{
		DataDir: dir, TransformEnabled: true, TransformQueueMax: 6, TransformMaxCPUS: 600, TransformMaxOutS: 900, TransformMaxMemMB: 1024,
		TransformAgentDailyCPUS: 1800, TransformHumanDailyCPUS: 14400,
	}
}

func newRig(t *testing.T, mut ...func(*config.Config, *Options)) *rig {
	t.Helper()
	dbPath, fx := templateDB(t)
	path := filepath.Join(t.TempDir(), "t.db")
	b, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := librarytest.FixedNow.Add(24 * time.Hour)
	r := &rig{t: t, st: st, fx: fx, now: &atomic.Pointer[time.Time]{}, wakes: &atomic.Int32{}, ids: &atomic.Int64{}}
	r.now.Store(&now)
	r.cfg = testConfig(t.TempDir())
	o := Options{Store: st, Now: r.clock, Wake: func() { r.wakes.Add(1) }}
	o.NewID = func() string { return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xe10000+r.ids.Add(1)) }
	o.Library = library.New(st, nil, r.clock)
	o.Library.UseIDFunc(func() string { return fmt.Sprintf("01900000-0000-7000-8001-%012x", 0xe20000+r.ids.Add(1)) })
	for _, m := range mut {
		m(&r.cfg, &o)
	}
	o.Config = r.cfg
	r.svc = New(o)
	return r
}

// req builds a request with one member from a JSON object text.
func req(kind Kind, params string) Request {
	return Request{Kind: kind, Members: map[Kind]json.RawMessage{kind: json.RawMessage(params)}}
}

func (r *rig) tx(label string) string { r.t.Helper(); return r.fx.Tx[label] }

// gated returns a context shaped like the gate's: a pending audit entry and an idempotency key.
func (r *rig) gated(p auth.Principal, key string) context.Context {
	r.t.Helper()
	ctx, _ := idem.WithMarks(context.Background())
	ctx = audit.WithPending(ctx, audit.Entry{At: r.clock(), Principal: p, Transport: reqinfo.REST, Client: "test", Op: "create_transform", Method: "POST", ArgsSHA256: "x"})
	if key != "" {
		is := idem.NewStore(r.st, r.clock)
		b, err := is.Begin(ctx, p.Key(), key, "create_transform", "sha-"+key, false)
		if err != nil || b.Outcome != idem.Proceed {
			r.t.Fatalf("idempotency begin: %+v %v", b, err)
		}
		ctx = idem.WithKey(ctx, p.Key(), key, b.Gen)
	}
	return ctx
}

func encodeJSON(c CreateResult) (int, []byte, error) {
	b, err := json.Marshal(map[string]any{"id": c.View.ID, "state": c.View.State})
	return c.Status, b, err
}

func (r *rig) count(q string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func (r *rig) exec(q string, args ...any) {
	r.t.Helper()
	if _, err := r.st.W.Exec(q, args...); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
}

// problemOf unwraps a *problem.Error and fails the test for any other error.
func problemOf(t *testing.T, err error) *problem.Error {
	t.Helper()
	var pe *problem.Error
	if !errors.As(err, &pe) {
		t.Fatalf("error %v is not a problem error", err)
	}
	return pe
}

func wantProblem(t *testing.T, err error, status int, code string) *problem.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("want %d %s, got no error", status, code)
	}
	pe := problemOf(t, err)
	if pe.Status != status || pe.Code != code {
		t.Fatalf("got %d %s (%s), want %d %s", pe.Status, pe.Code, pe.Message, status, code)
	}
	return pe
}

func wantRule(t *testing.T, err error, field, rule string) {
	t.Helper()
	pe := wantProblem(t, err, 422, ErrInvalidTransform)
	if pe.Details["field"] != field || pe.Details["rule"] != rule {
		t.Fatalf("got field=%v rule=%v, want %s %s", pe.Details["field"], pe.Details["rule"], field, rule)
	}
}

// glacier returns a valid glacier request over a fixture take.
func glacier(src string) Request { return req(KindGlacier, fmt.Sprintf(`{"source_id":%q}`, src)) }

// morphAB is a valid morph of A into B with explicit downbeats.
func (r *rig) morphAB(extra string) Request {
	return req(KindMorph, fmt.Sprintf(`{"track_a_id":%q,"track_b_id":%q,"bars":2,"downbeat_a_s":0.4,"downbeat_b_s":0.2%s}`, r.tx("A"), r.tx("B"), extra))
}

type jsonRaw = json.RawMessage

func ptrStr(s string) *string { return &s }

type configT = config.Config

func testPrincipalNoName() auth.Principal { return auth.Principal{Kind: auth.KindAgent, ID: "x"} }

// cancel runs B3's Cancel and reads the view afterwards, as B4's operation will.
func (r *rig) cancel(ctx context.Context, p auth.Principal, id string) (View, error) {
	if err := r.svc.Cancel(ctx, p, id); err != nil {
		return View{}, err
	}
	return r.svc.Get(ctx, p, id)
}
