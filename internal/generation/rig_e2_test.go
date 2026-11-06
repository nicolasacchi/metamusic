package generation_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/engine/registry"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/providertest"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The E2 rig: the FixtureE2 library (yue2 takes of every L-shape plus the ACE-Step takes), a registry with yue2 and an
// ACE-Step engine whose verified features are all on, a backend set and gate set of the two fakes, a scriptable adoption
// gate and a scriptable reference clipper. Synthetic data only.

var (
	e2ImageOnce sync.Once
	e2Image     []byte
	e2Info      lt.E2Info
)

func e2FixtureImage(t *testing.T) ([]byte, lt.E2Info) {
	e2ImageOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mm-generation-e2-fixture-")
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
		e2Info = lt.FixtureE2(t, st, lt.FixedNow)
		if _, err := st.W.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			t.Error(err)
		}
		if err := st.Close(); err != nil {
			t.Error(err)
		}
		e2Image, _ = os.ReadFile(p) //nolint:gosec // our own temp dir
	})
	if e2Image == nil {
		t.Fatal("e2 fixture image unavailable")
	}
	return e2Image, e2Info
}

const allOnFeatures = `{"schema":"mm.engine-features/2","engine":"acestep15","verified_at":"2026-11-05","canary_commit":"abc1234",
 "tasks":["text2music","repaint","extend","complete","extract"],"reference":true,"reference_reproducible":false,"seed_reproducible":true,
 "lrc":false,"lrc_fixed_s":60,"models_mode":"bundle_cached_base_baked","max_s_default":480,
 "pools":{"AMPERE_24":{"max_s":480},"ADA_24":{"max_s":480}},"repaint":{"min_s":3,"max_s":90},"extend":{"min_s":3,"max_s":90},"reference_max_s":30}`

func features(t testing.TB, edit func(*acestep.Features)) acestep.Features {
	t.Helper()
	f, err := acestep.ParseFeatures([]byte(allOnFeatures))
	if err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(&f)
	}
	return f
}

// fakeAdoption is the scriptable AdoptionGate: engines marked adopted are admitted, the rest refused.
type fakeAdoption struct {
	mu      sync.Mutex
	adopted map[string]bool
	trials  []string
}

func (a *fakeAdoption) Admitted(_ context.Context, _ generation.QueryReader, engineName, trialID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.trials = append(a.trials, trialID)
	if engineName == "yue2" || a.adopted[engineName] {
		return nil
	}
	return problem.E(409, "ENGINE_NOT_ADOPTED", "the engine has not been adopted", map[string]any{"engine": engineName, "hint": "propose_engine_trial"})
}

func (a *fakeAdoption) set(engineName string, on bool) {
	a.mu.Lock()
	a.adopted[engineName] = on
	a.mu.Unlock()
}

// fakeClipper is the scriptable ReferenceClipper: the digest is a function of the spec unless pinned, errors can be injected.
type fakeClipper struct {
	mu     sync.Mutex
	calls  []clip.RefSpec
	digest string // "" => derived from the spec
	err    error
}

func (c *fakeClipper) Reference(_ context.Context, sp clip.RefSpec) (clip.RefObject, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, sp)
	if c.err != nil {
		return clip.RefObject{}, c.err
	}
	d := c.digest
	if d == "" {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d", sp.MasterSHA256, sp.FromMS, sp.ToMS)))
		d = hex.EncodeToString(sum[:])
	}
	return clip.RefObject{SHA256: d}, nil
}

func (c *fakeClipper) ToolVersion() string { return "fake-tool" }

func (c *fakeClipper) setDigest(d string) {
	c.mu.Lock()
	c.digest = d
	c.mu.Unlock()
}

func (c *fakeClipper) setErr(err error) {
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
}

func (c *fakeClipper) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

func (c *fakeClipper) last() clip.RefSpec {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[len(c.calls)-1]
}

// runtimeBackend is a scripted backend with a settable maximum runtime.
type runtimeBackend struct {
	*providertest.Backend
	max time.Duration
}

func (b runtimeBackend) MaxRuntime() time.Duration { return b.max }

type recordingHook struct {
	mu    sync.Mutex
	calls [][2]string
}

func (h *recordingHook) OnBatchCreated(_ context.Context, _ *sql.Tx, trialID, batchID string) error {
	h.mu.Lock()
	h.calls = append(h.calls, [2]string{trialID, batchID})
	h.mu.Unlock()
	return nil
}

type rigE2 struct {
	*rig
	info     lt.E2Info
	adoption *fakeAdoption
	clipper  *fakeClipper
	hook     *recordingHook
	yue, ace *providertest.Backend
	gates    *provider.GateSet
	backends *provider.Set
}

type e2Cfg struct {
	features     func(*acestep.Features)
	aceMax       time.Duration
	noAdopt      bool
	noGates      bool
	noRefClipper bool
	aceMode      string
	aceKeys      bool
}

type e2Opt func(*e2Cfg)

// withACEGate sets the mode and key presence of the ACE-Step gate (runpod without keys is a disabled backend).
func withACEGate(mode string, keys bool) e2Opt {
	return func(c *e2Cfg) { c.aceMode, c.aceKeys = mode, keys }
}

func withFeatures(f func(*acestep.Features)) e2Opt { return func(c *e2Cfg) { c.features = f } }
func withACEMaxRuntime(d time.Duration) e2Opt      { return func(c *e2Cfg) { c.aceMax = d } }
func withoutAdoption() e2Opt                       { return func(c *e2Cfg) { c.noAdopt = true } }
func withoutGates() e2Opt                          { return func(c *e2Cfg) { c.noGates = true } }
func withoutClipper() e2Opt                        { return func(c *e2Cfg) { c.noRefClipper = true } }

// newRigE2 builds a rig over the E2 fixture. ACE-Step is adopted unless withoutAdoption.
func newRigE2(t *testing.T, opts ...e2Opt) *rigE2 {
	t.Helper()
	cfg := e2Cfg{aceMax: 15 * time.Minute, aceMode: provider.ModeFake, aceKeys: true}
	for _, o := range opts {
		o(&cfg)
	}
	img, info := e2FixtureImage(t)
	p := filepath.Join(t.TempDir(), "g.db")
	if err := os.WriteFile(p, img, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r := &rig{t: t, st: st, clk: &clockbox{t: lt.FixedNow}, notes: &capture{}}
	r.n.Store(0x100000) // clear of the ids the fixtures took
	r.lib = library.New(st, nil, r.clk.Now)
	r.lib.UseIDFunc(r.id)
	lin := lineage.New(lineage.Options{Store: st, Now: r.clk.Now})
	r.sp = spend.New(spend.Options{Store: st, Now: r.clk.Now, NewID: r.id, ConfirmKey: []byte("test-key"), Location: time.UTC, Rand: seeded(7)})
	reg := registry.Default(registry.Options{ACEStep: true})
	reg[acestep.Name] = acestep.Engine{Verified: features(t, cfg.features)}
	r.opts = generation.Options{
		Store: st, Library: r.lib, Lineage: lin, Spend: r.sp, Engines: reg, Notify: r.notes, PublicURL: "http://mm.example.com",
		Now: r.clk.Now, NewID: r.id, Wake: func() { r.wakes.Add(1) },
		ColdStartS: 180, KillSlackS: 15, MaxRuntime: 15 * time.Minute,
	}
	r.opts.Gate = r.gate(provider.ModeFake)
	r.svc = generation.New(r.opts)

	x := &rigE2{rig: r, info: info, adoption: &fakeAdoption{adopted: map[string]bool{acestep.Name: !cfg.noAdopt}}, clipper: &fakeClipper{}, hook: &recordingHook{}}
	x.yue = providertest.New(provider.BackendFake, providertest.Script{})
	x.ace = providertest.NewACEStep(providertest.Script{})
	x.backends, err = provider.NewSet(provider.BackendFake, x.yue, runtimeBackend{x.ace, cfg.aceMax})
	if err != nil {
		t.Fatal(err)
	}
	aceGate := provider.NewGate(st.R, provider.GateConfig{Mode: cfg.aceMode, Backend: provider.BackendFakeACEStep, Engine: provider.EngineACEStep,
		KeysLoaded: cfg.aceKeys, ColdStartS: 180, MaxRuntime: cfg.aceMax}, r.clk.Now)
	x.gates, err = provider.NewGateSet(provider.BackendFake, r.opts.Gate, aceGate)
	if err != nil {
		t.Fatal(err)
	}
	var ad generation.AdoptionGate
	if !cfg.noAdopt {
		ad = x.adoption
	}
	var rc generation.ReferenceClipper
	if !cfg.noRefClipper {
		rc = x.clipper
	}
	gs := x.gates
	if cfg.noGates {
		gs = nil
	}
	r.svc.UseEngines(x.backends, gs, ad, rc)
	r.svc.UseBatchHook(x.hook)
	return x
}

// estimate estimates items with the engine on the body (the way the SPA sends it).
func (x *rigE2) estimate(p auth.Principal, engineName string, items ...generation.Item) (generation.Estimate, error) {
	return x.svc.Estimate(context.Background(), p, generation.EstimateIn{Engine: engineName, Items: items, Via: viaFor(p)})
}

func (x *rigE2) mustEstimate(p auth.Principal, engineName string, items ...generation.Item) generation.Estimate {
	x.t.Helper()
	e, err := x.estimate(p, engineName, items...)
	if err != nil {
		x.t.Fatalf("estimate: %v", err)
	}
	return e
}

func rawJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func ctxBG() context.Context { return context.Background() }

// newServiceWithoutACE is x's service with only yue2 registered, over the same store.
func newServiceWithoutACE(x *rigE2) *generation.Service {
	o := x.opts
	o.Engines = registry.Default(registry.Options{})
	return generation.New(o)
}
