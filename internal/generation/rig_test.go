package generation_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	mrand "math/rand/v2"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engine/registry"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/providertest"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	os.Exit(m.Run())
}

var (
	imageOnce sync.Once
	image     []byte
)

// fixtureImage builds the FixtureM3 database once per process; every rig copies it.
func fixtureImage(t *testing.T) []byte {
	imageOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mm-generation-fixture-")
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = os.RemoveAll(dir) }()
		p := filepath.Join(dir, "g.db")
		st, err := store.Open(context.Background(), p)
		if err != nil {
			t.Error(err)
			return
		}
		lt.FixtureM3(t, st, lt.FixedNow)
		if _, err := st.W.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			t.Error(err)
		}
		if err := st.Close(); err != nil {
			t.Error(err)
		}
		image, _ = os.ReadFile(p) //nolint:gosec // our own temp dir
	})
	if image == nil {
		t.Fatal("fixture image unavailable")
	}
	return image
}

// clockbox is a per-rig settable clock (no globals).
type clockbox struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clockbox) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clockbox) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// capture records notifications.
type capture struct {
	mu   sync.Mutex
	msgs []notify.Message
}

func (c *capture) Notify(_ context.Context, m notify.Message) error {
	c.mu.Lock()
	c.msgs = append(c.msgs, m)
	c.mu.Unlock()
	return nil
}

func (c *capture) all() []notify.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]notify.Message(nil), c.msgs...)
}

var (
	human = auth.Principal{Kind: auth.KindHuman, ID: "fixture", Name: "fixture", Via: "proxy", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate, auth.ScopeAdmin}}
	agent = auth.Principal{Kind: auth.KindAgent, ID: "fixture-token", Name: "fixture-agent", Via: "token", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeGenerate}}
	other = auth.Principal{Kind: auth.KindAgent, ID: "other-token", Name: "other-agent", Via: "token", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeGenerate}}
)

type rig struct {
	t     *testing.T
	st    *store.Store
	svc   *generation.Service
	sp    *spend.Service
	lib   *library.Service
	clk   *clockbox
	notes *capture
	wakes atomic.Int32
	n     atomic.Int64
	opts  generation.Options
}

type rigOpt func(*rigCfg)

type rigCfg struct {
	mode   string
	margin int64
	caps   *spend.Caps
}

func withMargin(ppm int64) rigOpt     { return func(c *rigCfg) { c.margin = ppm } }
func withCaps(caps spend.Caps) rigOpt { return func(c *rigCfg) { c.caps = &caps } }

func newRig(t *testing.T, opts ...rigOpt) *rig {
	t.Helper()
	cfg := rigCfg{mode: provider.ModeFake}
	for _, o := range opts {
		o(&cfg)
	}
	p := filepath.Join(t.TempDir(), "g.db")
	if err := os.WriteFile(p, fixtureImage(t), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r := &rig{t: t, st: st, clk: &clockbox{t: lt.FixedNow}, notes: &capture{}}
	if cfg.caps != nil {
		c := cfg.caps
		r.exec(`UPDATE spend_caps SET monthly_micro_usd=?, batch_micro_usd=?, human_daily_micro_usd=?, agent_daily_micro_usd=?, auto_approve_takes=?, auto_approve_micro_usd=?, updated_by='system:test', updated_at=? WHERE id = 1`,
			int64(c.MonthlyMicro), int64(c.BatchMicro), int64(c.HumanDailyMicro), int64(c.AgentDailyMicro), c.AutoApproveTakes, int64(c.AutoApproveMicro), store.FormatTime(lt.FixedNow))
	}
	r.lib = library.New(st, nil, r.clk.Now)
	r.lib.UseIDFunc(r.id)
	lin := lineage.New(lineage.Options{Store: st, Now: r.clk.Now})
	r.sp = spend.New(spend.Options{Store: st, Now: r.clk.Now, NewID: r.id, ConfirmKey: []byte("test-key"), Location: time.UTC, Rand: seeded(7)})
	r.opts = generation.Options{
		Store: st, Library: r.lib, Lineage: lin, Spend: r.sp, Engines: registry.Default(), Notify: r.notes, PublicURL: "http://mm.example.com",
		Now: r.clk.Now, NewID: r.id, Wake: func() { r.wakes.Add(1) },
		ColdStartS: 180, KillSlackS: 15, MaxRuntime: 15 * time.Minute, PriceMarginPPM: cfg.margin,
	}
	r.opts.Gate = r.gate(cfg.mode)
	r.svc = generation.New(r.opts)
	return r
}

// seeded is a deterministic random source (ChaCha8 under a lock).
func seeded(seed byte) *lockedReader {
	var k [32]byte
	k[0] = seed
	return &lockedReader{r: mrand.NewChaCha8(k)}
}

type lockedReader struct {
	mu sync.Mutex
	r  *mrand.ChaCha8
}

func (l *lockedReader) Read(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.r.Read(p)
}

func (r *rig) id() string { return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xb000+r.n.Add(1)) }

func (r *rig) gate(mode string) *provider.Gate {
	return provider.NewGate(r.st.R, provider.GateConfig{Mode: mode, KeysLoaded: true, ColdStartS: 180, MaxRuntime: 15 * time.Minute}, r.clk.Now)
}

// withGate builds a second service over the same store with another gate.
func (r *rig) withGate(g *provider.Gate) *generation.Service {
	o := r.opts
	o.Gate = g
	return generation.New(o)
}

// runpodService caches q as the quote of the serverless backend and returns a service whose gate is runpod.
func (r *rig) runpodService(q provider.Quote, armed bool, keys bool) *generation.Service {
	r.t.Helper()
	b, _ := json.Marshal(q)
	a := 0
	if armed {
		a = 1
	}
	r.exec(`INSERT INTO provider_state(backend, armed, changed_by, changed_at, quote_json, quote_at) VALUES ('runpod-serverless', ?, 'system:test', ?, ?, ?)
		ON CONFLICT(backend) DO UPDATE SET armed = excluded.armed, quote_json = excluded.quote_json, quote_at = excluded.quote_at`,
		a, store.FormatTime(r.clk.Now()), string(b), store.FormatTime(q.ObservedAt))
	g := provider.NewGate(r.st.R, provider.GateConfig{Mode: provider.ModeRunPod, KeysLoaded: keys, ColdStartS: 180, MaxRuntime: 15 * time.Minute}, r.clk.Now)
	return r.withGate(g)
}

func (r *rig) goodQuote() provider.Quote {
	return providertest.GoodQuote(provider.BackendRunPod, r.clk.Now().Add(-time.Minute))
}

func (r *rig) exec(q string, args ...any) {
	r.t.Helper()
	if _, err := r.st.W.Exec(q, args...); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
}

func (r *rig) count(q string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func (r *rig) val(q string, args ...any) int64 {
	r.t.Helper()
	var n sql.NullInt64
	if err := r.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
	return n.Int64
}

func (r *rig) str(q string, args ...any) string {
	r.t.Helper()
	var n sql.NullString
	if err := r.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		r.t.Fatalf("%s: %v", q, err)
	}
	return n.String
}

func sp(s string) *string { return &s }

// est estimates items as p (via "api") and fails the test on error.
func (r *rig) est(p auth.Principal, items ...generation.Item) generation.Estimate {
	r.t.Helper()
	e, err := r.svc.Estimate(context.Background(), p, generation.EstimateIn{Items: items, Via: viaFor(p)})
	if err != nil {
		r.t.Fatalf("estimate: %v", err)
	}
	return e
}

func viaFor(p auth.Principal) string {
	if p.Kind == auth.KindHuman {
		return spend.ViaHumanCreate
	}
	return spend.ViaAPI
}

func newSeedItem(parent string, n int) generation.Item {
	return generation.Item{Mode: lineage.ModeNewSeed, ParentTrackID: &parent, NSeeds: n}
}

// encode is the stand-in encoder of the operation: the body is the batch id and state.
func encode(c generation.Created) (int, []byte, error) {
	b, err := json.Marshal(map[string]any{"id": c.BatchID, "state": c.State})
	return 201, b, err
}

func (r *rig) create(p auth.Principal, e generation.Estimate, via string) (generation.Created, error) {
	return r.svc.Create(context.Background(), p, generation.CreateIn{Token: e.Token, Via: via, Rationale: "test"}, encode)
}

func (r *rig) mustCreate(p auth.Principal, e generation.Estimate, via string) generation.Created {
	r.t.Helper()
	c, err := r.create(p, e, via)
	if err != nil {
		r.t.Fatalf("create: %v", err)
	}
	return c
}

// pendingCtx is a context as the gate would build it for an audited operation: the precomputed audit entry.
func pendingCtx(r *rig, p auth.Principal, op string) context.Context {
	return audit.WithPending(context.Background(), audit.Entry{At: r.clk.Now(), Principal: p, Transport: reqinfo.REST, Client: "test", Op: op, Method: "POST", ArgsSHA256: "x"})
}
