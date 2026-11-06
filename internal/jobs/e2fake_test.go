package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/engine/registry"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
	"github.com/nicolasacchi/metamusic/internal/provider/providertest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Lane E2-INT-B: the property and chaos suites over BOTH real fake backends (`fake` serving yue2 and `fake-acestep` serving
// acestep15), each with its simulated worker uploading through the rig's own handlers (header-only FLAC, stub ingester).

// twoEnv is a propEnv whose runner and reconcilers run over the two-backend set.
type twoEnv struct {
	*propEnv
	ace    *fake.Backend
	aceSim *fake.SimWorker
	set    *provider.Set
	gates  *provider.GateSet
	recs   []*Reconciler

	// scripted input chain: the minter the runner signs inputs with and the fetcher the simulated worker GETs them through
	fmu     sync.Mutex
	minter  func(req json.RawMessage) (map[string]provider.InputRef, error)
	fetch   func(url string) (int, []byte, error)
	mints   int
	fetches int

	aceBatches int
}

type e2Minter struct{ e *twoEnv }

func (m e2Minter) MintInputs(_ context.Context, req json.RawMessage, _ time.Time) (map[string]provider.InputRef, error) {
	m.e.fmu.Lock()
	defer m.e.fmu.Unlock()
	m.e.mints++
	if m.e.minter == nil {
		return nil, errors.New("no scripted minter")
	}
	return m.e.minter(req)
}

type e2Fetcher struct{ e *twoEnv }

func (f e2Fetcher) Get(_ context.Context, url string) (int, []byte, error) {
	f.e.fmu.Lock()
	defer f.e.fmu.Unlock()
	f.e.fetches++
	if f.e.fetch == nil {
		return 404, nil, nil
	}
	return f.e.fetch(url)
}

const aceBackendName = provider.BackendFakeACEStep

func aceWeights() map[string]string { return acestep.WeightsFor(acestep.TasksTurbo) }

// newTwoEnv builds a quiet two-backend timeline (no random events; the caller scripts them). aceOpts tune the ACE-Step fake.
func newTwoEnv(t *testing.T, seed uint64, aceOpts ...func(*fake.Options)) *twoEnv {
	t.Helper()
	e := &twoEnv{}
	e.propEnv = newPropEnv(t, seed, "within", func(o *RunnerOptions) {
		o.Engines = registry.Default(registry.Options{ACEStep: true})
		o.Inputs = e2Minter{e}
	})
	r := e.r
	e.aceSim = fake.NewSimWorker(fake.SimWorkerOptions{Uploader: fake.HandlerUploader{UploadHandler: r.up.Handler(), StartHandler: r.up.StartHandler()},
		Clock: r.clk, HeaderOnlyFLAC: headerOnly, TempDir: t.TempDir(), Weights: aceWeights(), Fetcher: e2Fetcher{e}})
	fo := fake.Options{Clock: r.clk, Worker: e.aceSim, PricePPH: propPrice,
		BatchOf: func(job string) string {
			var b string
			_ = r.st.R.QueryRow(`SELECT batch_id FROM jobs WHERE id = ?`, job).Scan(&b)
			return b
		}}
	for _, f := range aceOpts {
		f(&fo)
	}
	e.ace = fake.NewACEStep(fo)
	var err error
	if e.set, err = provider.NewSet(provider.BackendFake, e.fb, e.ace); err != nil {
		t.Fatal(err)
	}
	e.gates, err = provider.NewGateSet(provider.BackendFake,
		provider.NewGate(r.st.R, provider.GateConfig{Mode: provider.ModeFake, KeysLoaded: true, ColdStartS: 180, MaxRuntime: 15 * time.Minute}, r.clk.Now),
		provider.NewGate(r.st.R, provider.GateConfig{Mode: provider.ModeFake, Backend: aceBackendName, Engine: provider.EngineACEStep, KeysLoaded: true, ColdStartS: 300, MaxRuntime: 15 * time.Minute}, r.clk.Now))
	if err != nil {
		t.Fatal(err)
	}
	q, _ := json.Marshal(providertest.GoodQuote(aceBackendName, t0))
	r.exec(`INSERT INTO provider_state(backend, armed, changed_by, changed_at, quote_json, quote_at) VALUES(?, 1, 'system:default', ?, ?, ?)`,
		aceBackendName, store.FormatTime(t0), string(q), store.FormatTime(t0))
	e.wire()
	return e
}

// wire puts the current runner over the set and rebuilds one reconciler per backend (the first drives the others).
func (e *twoEnv) wire() {
	e.t.Helper()
	e.cur.UseSet(e.set, e.gates, map[string]provider.WorkerExpect{provider.EngineACEStep: {Weights: aceWeights()}})
	base := e.r.recon(func(o *ReconcilerOptions) {
		o.Backend, o.Runner, o.Owner = e.fb, e.cur, fmt.Sprintf("runner-%d", e.owners)
	}).Options()
	base.Audit = audit.NewStore(e.r.st, e.r.newID)
	cfgs := map[string]provider.GateConfig{
		provider.BackendFake: {Mode: provider.ModeFake, KeysLoaded: true, ColdStartS: 180, MaxRuntime: 15 * time.Minute},
		aceBackendName:       {Mode: provider.ModeFake, Backend: aceBackendName, Engine: provider.EngineACEStep, KeysLoaded: true, ColdStartS: 300, MaxRuntime: 15 * time.Minute},
	}
	rs, err := NewReconcilers(e.set, base, cfgs)
	if err != nil {
		e.t.Fatal(err)
	}
	e.recs, e.rec = rs, rs[0]
	e.cur.UseMeter(NewMeterRouter(rs))
}

// takeOver2 starts a new runner instance over the set (after a crash or a long pause).
func (e *twoEnv) takeOver2() {
	e.owners++
	e.cur = e.r.second(fmt.Sprintf("runner-%d", e.owners))
	e.runs = append(e.runs, e.cur)
	e.wire()
}

// aceRecipe inserts (once per marker) an acestep15 text2music recipe whose caption carries the fake markers.
func (e *twoEnv) aceRecipe(marker string) string {
	caption := "synth pop"
	if marker != "" {
		caption += " " + marker
	}
	return e.aceRecipeReq("ace:"+marker, aceRequest(e.t, map[string]any{"id": "ace-" + strconv.Itoa(len(e.recipes)+1), "seed": 1, "caption": caption, "duration_ds": 300, "thinking": true, "bpm": nil}))
}

// aceRecipeReq inserts (once per key) an acestep15 recipe holding the canonical request.
func (e *twoEnv) aceRecipeReq(key, req string) string {
	if id, ok := e.recipes[key]; ok {
		return id
	}
	e.nRecipe++
	id := fmt.Sprintf("01900000-0000-7000-8000-0000000e%04d", e.nRecipe)
	var rid struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(req), &rid)
	e.r.exec(`INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,hash,core_hash,created_by,created_at)
		VALUES(?,'acestep15',?,?,'pop','la',1,'think',?,?,'import',?)`, id, rid.ID, req, "h"+id, "c"+id, store.FormatTime(t0))
	e.recipes[key] = id
	return id
}

// seedACE seeds a batch of n acestep15 jobs on the ACE-Step backend.
func (e *twoEnv) seedACE(marker string, n, timeoutS int) seeded {
	e.t.Helper()
	return e.seedACERecipe(e.aceRecipe(marker), n, timeoutS)
}

func (e *twoEnv) seedACERecipe(recipe string, n, timeoutS int) seeded {
	e.t.Helper()
	return e.r.seedBatch(batchSpec{jobs: n, timeoutS: timeoutS, ceiling: 20_000_000, backend: aceBackendName, engine: provider.EngineACEStep, recipe: recipe})
}

// seedYue seeds a batch of n yue2 jobs on the default backend.
func (e *twoEnv) seedYue(marker string, n, timeoutS int) seeded {
	return e.r.seedBatch(batchSpec{jobs: n, timeoutS: timeoutS, ceiling: 20_000_000, recipe: e.recipeFor(marker)})
}

// drive2 steps both backends (runner) and runs the reconcilers every third tick until done.
func (e *twoEnv) drive2(maxTicks int, dt time.Duration, done func() bool) {
	e.t.Helper()
	for i := 0; i < maxTicks; i++ {
		if done() {
			return
		}
		e.r.clk.Advance(dt)
		if e.step() {
			e.r.clk.Advance(100 * time.Second)
			e.takeOver2()
		}
		if i%3 == 0 {
			e.pass()
		}
	}
	if !done() {
		e.t.Fatalf("drive2: the condition never held after %d ticks of %v\n%s", maxTicks, dt, e.dumpStuck())
	}
}

func (e *twoEnv) quietCaps() {
	e.setCaps(spendCaps(50_000_000))
	e.capsOn = true
	if _, err := e.cur.Step(e.ctx); err != nil { // takes the lease so the reconcilers may run
		e.t.Fatal(err)
	}
	e.pass()
}
