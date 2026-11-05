package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/providertest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Lane E2-J1: the runner over a backend Set and one Reconciler per backend (C151.2/4/5, D-E2-39). Every test here has
// two backends side by side and proves that what happens to one never reaches the other.

const aceName = "fake-acestep"

// aceBE is a scripted backend that renders acestep15.
type aceBE struct{ *providertest.Backend }

func (aceBE) Engine() string { return provider.EngineACEStep }

// dual is a rig over two backends: the rig's own `fake` (yue2) and a scripted `fake-acestep`.
type dual struct {
	*rig
	ace  aceBE
	set  *provider.Set
	mu   sync.Mutex
	aSw  func() (provider.SweepReport, error)
	aQ   func() (provider.Quote, error)
	aPrg int
}

// newDual builds the rig and puts its runner over the two-backend set. aceArmed seeds the ACE provider_state row.
func newDual(t *testing.T, aceArmed bool) *dual {
	t.Helper()
	d := &dual{rig: newRig(t)}
	d.ace = aceBE{providertest.New(aceName, providertest.Script{
		Capabilities: &d.caps,
		Quote: func(context.Context) (provider.Quote, error) {
			d.mu.Lock()
			fn := d.aQ
			d.mu.Unlock()
			if fn != nil {
				return fn()
			}
			return providertest.GoodQuote(aceName, d.clk.Now()), nil
		},
		Sweep: func(context.Context) (provider.SweepReport, error) {
			d.mu.Lock()
			fn := d.aSw
			d.mu.Unlock()
			if fn != nil {
				return fn()
			}
			return provider.SweepReport{ObservedAt: d.clk.Now()}, nil
		},
		PurgeQueue: func(context.Context) error { d.mu.Lock(); d.aPrg++; d.mu.Unlock(); return nil },
	})}
	var err error
	if d.set, err = provider.NewSet(provider.BackendFake, d.be, d.ace); err != nil {
		t.Fatal(err)
	}
	gates, err := provider.NewGateSet(provider.BackendFake,
		provider.NewGate(d.st.R, provider.GateConfig{Mode: provider.ModeFake, KeysLoaded: true, ColdStartS: 180, MaxRuntime: 15 * time.Minute}, d.clk.Now),
		provider.NewGate(d.st.R, provider.GateConfig{Mode: provider.ModeFake, Backend: aceName, Engine: provider.EngineACEStep, KeysLoaded: true, ColdStartS: 300, MaxRuntime: 15 * time.Minute}, d.clk.Now))
	if err != nil {
		t.Fatal(err)
	}
	d.run.UseSet(d.set, gates, nil)
	armed := 0
	if aceArmed {
		armed = 1
	}
	q, _ := json.Marshal(providertest.GoodQuote(aceName, t0))
	d.exec(`INSERT INTO provider_state(backend, armed, changed_by, changed_at, quote_json, quote_at) VALUES(?, ?, 'system:default', ?, ?, ?)`,
		aceName, armed, store.FormatTime(t0), string(q), store.FormatTime(t0))
	return d
}

func (d *dual) sweepACE(fn func() (provider.SweepReport, error)) {
	d.mu.Lock()
	d.aSw = fn
	d.mu.Unlock()
}

// reconcilers builds the per-backend reconcilers over the rig and returns the first (the one a process runs).
func (d *dual) reconcilers() []*Reconciler {
	d.t.Helper()
	base := d.recon().Options()
	cfgs := map[string]provider.GateConfig{
		provider.BackendFake: {Mode: provider.ModeFake, KeysLoaded: true, ColdStartS: 180, MaxRuntime: 15 * time.Minute},
		aceName:              {Mode: provider.ModeFake, Backend: aceName, Engine: provider.EngineACEStep, KeysLoaded: true, ColdStartS: 300, MaxRuntime: 15 * time.Minute},
	}
	base.Audit = audit.NewStore(d.st, d.newID)
	rs, err := NewReconcilers(d.set, base, cfgs)
	if err != nil {
		d.t.Fatal(err)
	}
	return rs
}

func TestRunner_ClaimsPerBackend(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		aceArmed     bool
		aceTripped   bool
		foreignQuote bool
		wantACE      string
	}{
		{name: "both armed", aceArmed: true, wantACE: StateGenerating},
		{name: "ACE disarmed", aceArmed: false, wantACE: StateQueued},
		{name: "ACE tripped", aceArmed: true, aceTripped: true, wantACE: StateQueued},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDual(t, tc.aceArmed)
			if tc.aceTripped {
				d.exec(`INSERT INTO provider_trips(id, backend, tripped_at, reason, details_json, actions_json) VALUES('t1', ?, ?, 'crash_loop', '{}', '[]')`, aceName, store.FormatTime(t0))
			}
			yue := d.seedBatch(batchSpec{jobs: 1})
			ace := d.seedBatch(batchSpec{jobs: 1, backend: aceName})
			d.steps(2)
			if got := d.state(yue.Jobs[0]); got == StateQueued {
				t.Errorf("yue2 job did not move: %s", got)
			}
			if got := d.state(ace.Jobs[0]); (got == StateQueued) != (tc.wantACE == StateQueued) {
				t.Errorf("ACE job state %s, want queued=%v", got, tc.wantACE == StateQueued)
			}
			wantACE := 0
			if tc.wantACE != StateQueued {
				wantACE = 1
			}
			if got := d.be.Calls().Submit; got != 1 {
				t.Errorf("default backend submits %d, want 1", got)
			}
			if got := d.ace.Calls().Submit; got != wantACE {
				t.Errorf("ACE backend submits %d, want %d", got, wantACE)
			}
			// a job is only ever submitted to its own backend
			for _, sp := range d.be.Specs() {
				if sp.JobID == ace.Jobs[0] {
					t.Errorf("the ACE job reached the default backend")
				}
			}
			for _, sp := range d.ace.Specs() {
				if sp.JobID == yue.Jobs[0] {
					t.Errorf("the yue2 job reached the ACE backend")
				}
			}
			if a := d.atts(ace.Jobs[0]); len(a) > 0 && a[0].Backend != aceName {
				t.Errorf("attempt backend %q", a[0].Backend)
			}
		})
	}
}

func TestRunner_ClaimsPerBackend_InFlightLimitsIndependent(t *testing.T) {
	t.Parallel()
	d := newDual(t, true)
	// one worker per batch: each backend admits one attempt in flight, whatever the other runs
	yue := d.seedBatch(batchSpec{jobs: 2, workersMax: 1})
	ace := d.seedBatch(batchSpec{jobs: 2, workersMax: 1, backend: aceName})
	d.steps(2)
	live := func(backend string) int {
		return d.count(`SELECT COUNT(*) FROM job_attempts WHERE backend = ? AND state IN ('pending','submitted','unknown')`, backend)
	}
	if live(provider.BackendFake) != 1 || live(aceName) != 1 {
		t.Fatalf("in flight: default %d, ACE %d, want 1 and 1", live(provider.BackendFake), live(aceName))
	}
	if d.state(yue.Jobs[1]) != StateQueued || d.state(ace.Jobs[1]) != StateQueued {
		t.Errorf("second jobs: %s %s", d.state(yue.Jobs[1]), d.state(ace.Jobs[1]))
	}
}

func TestRunner_PollsAttemptOnItsOwnBackend(t *testing.T) {
	t.Parallel()
	d := newDual(t, true)
	yue := d.seedBatch(batchSpec{jobs: 1})
	ace := d.seedBatch(batchSpec{jobs: 1, backend: aceName})
	d.steps(2)
	d.advance(time.Minute)
	d.steps(2)
	if d.be.Calls().Poll == 0 || d.ace.Calls().Poll == 0 {
		t.Fatalf("polls: default %d, ACE %d", d.be.Calls().Poll, d.ace.Calls().Poll)
	}
	// the attempt refs are unique per backend; the default backend never saw the ACE ref and vice versa
	aceRef, yueRef := d.refOf(ace.Jobs[0], 1), d.refOf(yue.Jobs[0], 1)
	if aceRef == "" || yueRef == "" || aceRef == yueRef {
		t.Fatalf("refs %q %q", aceRef, yueRef)
	}
	if d.pollCount(aceRef) != 0 {
		t.Errorf("the ACE ref was polled on the default backend")
	}
}

func TestRunner_AttemptOfDepartedBackendIsNeverSentElsewhere(t *testing.T) {
	t.Parallel()
	d := newDual(t, true)
	ace := d.seedBatch(batchSpec{jobs: 1, backend: aceName})
	d.steps(2)
	if d.ace.Calls().Submit != 1 {
		t.Fatal("setup: not submitted")
	}
	// the process restarts without MM_ACESTEP: the set holds the default backend only, with the ACE attempt still live
	only, err := provider.NewSet(provider.BackendFake, d.be)
	if err != nil {
		t.Fatal(err)
	}
	d.run.UseSet(only, nil, nil)
	before := d.be.Calls()
	d.advance(time.Minute)
	d.steps(2)
	if got := d.be.Calls(); got.Poll != before.Poll || got.Cancel != before.Cancel {
		t.Errorf("the default backend was asked about an ACE attempt: %+v -> %+v", before, got)
	}
	_ = ace
}

func TestReadGate_ForeignQuoteNeverEnables(t *testing.T) {
	t.Parallel()
	d := newDual(t, true)
	gates, err := provider.NewGateSet(provider.BackendFake,
		provider.NewGate(d.st.R, provider.GateConfig{Mode: provider.ModeFake, KeysLoaded: true, ColdStartS: 180, MaxRuntime: 15 * time.Minute}, d.clk.Now),
		provider.NewGate(d.st.R, provider.GateConfig{Mode: provider.ModeRunPod, Backend: aceName, Engine: provider.EngineACEStep, KeysLoaded: true, ColdStartS: 180, MaxRuntime: 15 * time.Minute}, d.clk.Now))
	if err != nil {
		t.Fatal(err)
	}
	d.run.UseSet(d.set, gates, nil)
	views := d.run.claimViews()
	if len(views) != 2 || views[1].o.Backend.Name() != aceName || views[1].o.ColdStartS != d.ace.ColdStartS() {
		t.Fatalf("views %d", len(views))
	}
	read := func() gateInfo {
		g, err := views[1].readGate(t.Context(), d.st.R)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	// own quote: enabled
	own, _ := json.Marshal(providertest.GoodQuote(aceName, d.clk.Now()))
	d.exec(`UPDATE provider_state SET quote_json = ?, quote_at = ? WHERE backend = ?`, string(own), store.FormatTime(d.clk.Now()), aceName)
	if g := read(); !g.ok {
		t.Fatalf("own quote refused: %s", g.reason)
	}
	// the default backend's quote written under the ACE row: refused with endpoint_config
	foreign, _ := json.Marshal(providertest.GoodQuote(provider.BackendFake, d.clk.Now()))
	d.exec(`UPDATE provider_state SET quote_json = ? WHERE backend = ?`, string(foreign), aceName)
	if g := read(); g.ok || g.reason != provider.ReasonEndpointConfig {
		t.Errorf("foreign quote: ok=%v reason=%q", g.ok, g.reason)
	}
}

func TestReconciler_TripIsPerBackend(t *testing.T) {
	t.Parallel()
	d := newDual(t, true)
	// an ACE worker that is UNHEALTHY for good: init failure
	d.sweepACE(func() (provider.SweepReport, error) {
		return provider.SweepReport{ObservedAt: d.clk.Now(), Health: &provider.Health{Unhealthy: 1}, Endpoint: &provider.EndpointConfig{Name: "mm-ace", WorkersMax: 1}}, nil
	})
	yue := d.seedBatch(batchSpec{jobs: 1})
	ace := d.seedBatch(batchSpec{jobs: 1, backend: aceName})
	d.steps(2)
	rs := d.reconcilers()
	d.hold(d.run)
	for range 8 { // 7 minutes of passes: UNHEALTHY for unhealthyGrace (5 min) trips the ACE backend only
		if err := rs[0].Pass(t.Context()); err != nil {
			t.Fatal(err)
		}
		d.advance(time.Minute)
		d.hold(d.run)
	}
	if got := d.count(`SELECT COUNT(*) FROM provider_trips WHERE backend = ? AND reset_at IS NULL AND reason = 'crash_loop'`, aceName); got != 1 {
		t.Fatalf("ACE trips %d, want 1", got)
	}
	if got := d.count(`SELECT COUNT(*) FROM provider_trips WHERE backend = ?`, provider.BackendFake); got != 0 {
		t.Errorf("the default backend tripped %d times", got)
	}
	if d.aPrg == 0 {
		t.Error("the ACE queue was not purged")
	}
	if got := d.be.Calls(); got.PurgeQueue != 0 || got.ScaleToZero != 0 || got.Cancel != 0 {
		t.Errorf("the default backend was acted on: %+v", got)
	}
	if d.state(ace.Jobs[0]) == StateGenerating || d.state(ace.Jobs[0]) == StateStarting {
		t.Errorf("the ACE job was not stopped: %s", d.state(ace.Jobs[0]))
	}
	if got := d.state(yue.Jobs[0]); got == StateCancelling || got == StateCancelled || got == StateFailed {
		t.Errorf("the yue2 job was touched by the ACE trip: %s", got)
	}
	// the default backend keeps claiming: a fresh yue2 batch moves, a fresh ACE batch stays queued
	yue2 := d.seedBatch(batchSpec{jobs: 1})
	ace2 := d.seedBatch(batchSpec{jobs: 1, backend: aceName})
	d.steps(2)
	if d.state(yue2.Jobs[0]) == StateQueued {
		t.Error("the default backend stopped claiming")
	}
	if d.state(ace2.Jobs[0]) != StateQueued {
		t.Errorf("the tripped backend claimed: %s", d.state(ace2.Jobs[0]))
	}
	// the notification names the backend
	named := false
	for _, m := range d.notes.all() {
		if strings.Contains(m.Body, "backend "+aceName) {
			named = true
		}
	}
	if !named {
		t.Errorf("no notification names the backend: %+v", d.notes.all())
	}
}

func TestReconciler_InitializingNeverTrips_PerBackend(t *testing.T) {
	t.Parallel()
	d := newDual(t, true)
	d.sweepACE(func() (provider.SweepReport, error) {
		return provider.SweepReport{ObservedAt: d.clk.Now(), Health: &provider.Health{Initializing: 1}, Endpoint: &provider.EndpointConfig{Name: "mm-ace", WorkersMax: 1}}, nil
	})
	d.seedBatch(batchSpec{jobs: 1, backend: aceName})
	d.steps(2)
	rs := d.reconcilers()
	d.hold(d.run)
	for range 41 { // 40 minutes INITIALIZING
		if err := rs[0].Pass(t.Context()); err != nil {
			t.Fatal(err)
		}
		d.advance(time.Minute)
		d.hold(d.run)
	}
	if got := d.count(`SELECT COUNT(*) FROM provider_trips`); got != 0 {
		t.Fatalf("INITIALIZING tripped: %d", got)
	}
	warned := 0
	for _, m := range d.notes.all() {
		if m.Title == "Worker still initializing" {
			warned++
			if !strings.Contains(m.Body, aceName) {
				t.Errorf("the warning does not name the backend: %q", m.Body)
			}
		}
	}
	if warned != 1 {
		t.Errorf("warnings %d, want 1", warned)
	}
	if st := rs[1].Snapshot(); st.InitializingSince.IsZero() {
		t.Error("the ACE snapshot does not show initializing_since")
	}
	if st := rs[0].Snapshot(); !st.InitializingSince.IsZero() {
		t.Error("the default backend's snapshot shows the ACE worker")
	}
}

func TestReconciler_UnhealthyElsewhereDoesNotCountAsEvidenceHere(t *testing.T) {
	t.Parallel()
	// stuck_running compares RUNNING workers with attempts that show evidence of running: an ACE attempt that is
	// in progress must not hide a stuck yue2 worker
	d := newDual(t, true)
	d.sweepHealth(provider.Health{Running: 1}, 1) // the default backend: one RUNNING worker, no job of its own
	ace := d.seedBatch(batchSpec{jobs: 1, backend: aceName})
	d.steps(2)
	d.exec(`UPDATE job_attempts SET in_progress_seen = 1 WHERE job_id = ?`, ace.Jobs[0])
	rs := d.reconcilers()
	d.hold(d.run)
	for range 6 {
		if err := rs[0].Pass(t.Context()); err != nil {
			t.Fatal(err)
		}
		d.advance(time.Minute)
		d.hold(d.run)
	}
	if got := d.count(`SELECT COUNT(*) FROM provider_trips WHERE backend = ? AND reason = 'stuck_running'`, provider.BackendFake); got != 1 {
		t.Errorf("stuck yue2 worker trips: %d, want 1 (ACE evidence must not mask it)", got)
	}
	if got := d.count(`SELECT COUNT(*) FROM provider_trips WHERE backend = ?`, aceName); got != 0 {
		t.Errorf("ACE tripped: %d", got)
	}
}

func TestReconciler_QuoteStampedWithOwnBackend(t *testing.T) {
	t.Parallel()
	d := newDual(t, true)
	// a backend that stamps nothing, or another backend's name, still stores a quote bearing its own
	d.mu.Lock()
	d.aQ = func() (provider.Quote, error) {
		q := providertest.GoodQuote(provider.BackendFake, d.clk.Now())
		return q, nil
	}
	d.mu.Unlock()
	rs := d.reconcilers()
	d.hold(d.run)
	if err := rs[0].Pass(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{provider.BackendFake, aceName} {
		var raw string
		if err := d.st.R.QueryRow(`SELECT quote_json FROM provider_state WHERE backend = ?`, b).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var q provider.Quote
		if err := json.Unmarshal([]byte(raw), &q); err != nil {
			t.Fatal(err)
		}
		if q.Backend != b {
			t.Errorf("quote of %s names %q", b, q.Backend)
		}
	}
}

func TestReconciler_PeerBillingIsScopedAndHeld(t *testing.T) {
	t.Parallel()
	d := newDual(t, true)
	yue := d.seedBatch(batchSpec{jobs: 1})
	ace := d.seedBatch(batchSpec{jobs: 1, backend: aceName})
	d.exec(`UPDATE batches SET state='finished', finished_at = ? WHERE id IN (?, ?)`, store.FormatTime(t0), yue.Batch, ace.Batch)
	rs := d.reconcilers()
	if !rs[1].o.Peer || rs[0].o.Peer || !rs[0].o.Scoped || !rs[1].o.Scoped {
		t.Fatalf("scoping flags: %+v %+v", rs[0].o, rs[1].o)
	}
	d.advance(49 * time.Hour) // past ResidualHold (48 h)
	d.hold(d.run)
	if err := rs[1].Pass(t.Context()); err != nil { // only the ACE reconciler runs
		t.Fatal(err)
	}
	reconciled := func(b string) bool {
		return d.str(`SELECT COALESCE(reconciled_at, '') FROM batches WHERE id = ?`, b) != ""
	}
	if !reconciled(ace.Batch) {
		t.Error("the ACE batch was not released by the hold")
	}
	if reconciled(yue.Batch) {
		t.Error("the ACE reconciler released a yue2 batch")
	}
	if got := d.ace.Calls().Billing; got != 0 {
		t.Errorf("the ACE backend billing was read %d times (no billing windows of its own)", got)
	}
}

func TestReconcilers_SingleBackendIsUnscoped(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	set, err := provider.NewSet(provider.BackendFake, r.be)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := NewReconcilers(set, r.recon().Options(), nil)
	if err != nil || len(rs) != 1 {
		t.Fatalf("%d reconcilers: %v", len(rs), err)
	}
	if o := rs[0].o; o.Scoped || o.Peer || len(rs[0].peers) != 0 || rs[0].scope() != "" {
		t.Errorf("a single backend must behave as M3: %+v", o)
	}
	if _, err := NewReconcilers(&provider.Set{}, ReconcilerOptions{}, nil); err == nil {
		t.Error("an empty set must be refused")
	}
}

func TestReconciler_PeersRunConcurrentlyAndIsolated(t *testing.T) {
	t.Parallel()
	d := newDual(t, true)
	// the ACE sweep is refused (key revoked): it trips provider_access on ACE; the default pass still runs to the end
	d.sweepACE(func() (provider.SweepReport, error) { return provider.SweepReport{}, provider.ErrAccess })
	d.mu.Lock()
	d.aQ = func() (provider.Quote, error) { return providertest.GoodQuote(aceName, d.clk.Now()), nil }
	d.mu.Unlock()
	rs := d.reconcilers()
	d.hold(d.run)
	before := d.be.Calls().Sweep
	if err := rs[0].Pass(t.Context()); err != nil {
		t.Fatal(err)
	}
	if d.be.Calls().Sweep != before+1 {
		t.Error("the default backend's sweep did not run")
	}
	if got := d.count(`SELECT COUNT(*) FROM provider_trips WHERE backend = ? AND reason = 'provider_access'`, aceName); got != 1 {
		t.Errorf("ACE provider_access trips %d", got)
	}
	if got := d.count(`SELECT COUNT(*) FROM provider_trips WHERE backend = ?`, provider.BackendFake); got != 0 {
		t.Errorf("the default backend tripped: %d", got)
	}
}

func TestScopedWorkerKey_Table(t *testing.T) {
	t.Parallel()
	key := []byte("meter-key-for-tests-0123456789")
	plain := workerKey(key, "w1")
	for _, tc := range []struct {
		name, def, backend, want string
	}{
		{"no set", "", "fake", plain},
		{"default backend keeps the plain key", "fake", "fake", plain},
		{"other backend is prefixed", "fake", aceName, aceName + ":" + plain},
		{"no backend", "fake", "", plain},
	} {
		if got := scopedWorkerKey(key, tc.def, tc.backend, "w1"); got != tc.want {
			t.Errorf("%s: %q", tc.name, got)
		}
	}
	if keyBackend(plain, "fake") != "fake" || keyBackend(aceName+":"+plain, "fake") != aceName {
		t.Error("keyBackend")
	}
	if strings.Contains(scopedWorkerKey(key, "fake", aceName, "raw-worker-id"), "raw-worker-id") {
		t.Error("raw worker id leaked into a key")
	}
}

func TestMeter_ScopedPerBackend(t *testing.T) {
	t.Parallel()
	d := newDual(t, true)
	key := []byte("meter-key-for-tests-0123456789")
	yk, ak := scopedWorkerKey(key, provider.BackendFake, provider.BackendFake, "w-y"), scopedWorkerKey(key, provider.BackendFake, aceName, "w-a")
	ts := store.FormatTime(t0)
	for _, k := range []string{yk, ak} {
		d.exec(`INSERT INTO worker_meter(worker_key, started_at, first_seen_at, last_seen_at, uptime_s, status_last) VALUES(?,?,?,?,100,'RUNNING')`, k, ts, ts, ts)
	}
	yue := d.seedBatch(batchSpec{jobs: 1})
	ace := d.seedBatch(batchSpec{jobs: 1, backend: aceName})
	for _, sc := range []struct {
		scope   meterScope
		lives   int
		batch   string
		notBatc string
	}{
		{meterScope{}, 2, "", ""},
		{meterScope{backend: provider.BackendFake, def: provider.BackendFake}, 1, yue.Batch, ace.Batch},
		{meterScope{backend: aceName, def: provider.BackendFake}, 1, ace.Batch, yue.Batch},
	} {
		m, err := loadMeterFor(t.Context(), d.st.R, t0, time.Time{}, sc.scope)
		if err != nil {
			t.Fatal(err)
		}
		if len(m.lifetimes) != sc.lives {
			t.Errorf("scope %+v sees %d lifetimes, want %d", sc.scope, len(m.lifetimes), sc.lives)
		}
		if sc.batch != "" && (m.batchByID(sc.batch) == nil || m.batchByID(sc.notBatc) != nil) {
			t.Errorf("scope %+v batches wrong", sc.scope)
		}
	}
	// the router sends a batch to the reconciler of its backend
	rs := d.reconcilers()
	rt := NewMeterRouter(rs)
	if _, _, err := rt.OverheadSeconds(t.Context(), d.st.R, ace.Batch); err != nil {
		t.Fatal(err)
	}
	if _, _, err := rt.OverheadSeconds(t.Context(), d.st.R, yue.Batch); err != nil {
		t.Fatal(err)
	}
}

func TestProviderAdmin_UpdateBackend(t *testing.T) {
	t.Parallel()
	d := newDual(t, false)
	a := d.admin(provider.ModeFake, true)
	gates, err := provider.NewGateSet(provider.BackendFake, a.o.Gate,
		provider.NewGate(d.st.R, provider.GateConfig{Mode: provider.ModeFake, Backend: aceName, Engine: provider.EngineACEStep, KeysLoaded: true, ColdStartS: 300, MaxRuntime: 15 * time.Minute}, d.clk.Now))
	if err != nil {
		t.Fatal(err)
	}
	snap := SweepSnapshot{ObservedAt: t0, WorkerCounts: map[string]int{"running": 1}}
	a.UseBackends(gates, map[string]func() SweepSnapshot{provider.BackendFake: func() SweepSnapshot { return snap }},
		map[string]func() provider.Capabilities{provider.BackendFake: func() provider.Capabilities { return provider.Capabilities{Billing: "hour"} }})
	if ss, err := a.statusFor(t.Context(), provider.BackendFake); err != nil || ss.Workers == nil || ss.Workers.Running != 1 || ss.Capability.Billing != "hour" {
		t.Errorf("per-backend sweep and capabilities of the default: %+v %v", ss.Workers, err)
	}
	if _, err := a.statusFor(t.Context(), "nope"); !errors.Is(err, provider.ErrNoBackend) {
		t.Errorf("statusFor unknown backend: %v", err)
	}
	yes, no := true, false
	st, err := a.Update(t.Context(), humanP, UpdateIn{Armed: &yes, Reason: "go", Backend: aceName})
	if err != nil || !st.State.Armed || st.State.Backend != aceName {
		t.Fatalf("arm ACE: %+v %v", st.State, err)
	}
	if got := d.count(`SELECT armed FROM provider_state WHERE backend = 'fake'`); got != 1 {
		t.Errorf("the default backend changed: armed=%d", got)
	}
	// disarming the default leaves the ACE row alone
	if _, err := a.Update(t.Context(), humanP, UpdateIn{Armed: &no, Reason: "maintenance"}); err != nil {
		t.Fatal(err)
	}
	if got := d.count(`SELECT armed FROM provider_state WHERE backend = ?`, aceName); got != 1 {
		t.Errorf("the ACE row changed with the default: armed=%d", got)
	}
	// reset_trip is per backend
	d.exec(`INSERT INTO provider_trips(id, backend, tripped_at, reason, details_json, actions_json) VALUES('ta', ?, ?, 'crash_loop', '{}', '[]'), ('ty', 'fake', ?, 'crash_loop', '{}', '[]')`,
		aceName, store.FormatTime(t0), store.FormatTime(t0))
	if _, err := a.Update(t.Context(), humanP, UpdateIn{ResetTrip: true, Reason: "looked", Backend: aceName}); err != nil {
		t.Fatal(err)
	}
	if got := d.count(`SELECT COUNT(*) FROM provider_trips WHERE reset_at IS NULL AND backend = 'fake'`); got != 1 {
		t.Errorf("the default trip was reset")
	}
	if got := d.count(`SELECT COUNT(*) FROM provider_trips WHERE reset_at IS NULL AND backend = ?`, aceName); got != 0 {
		t.Errorf("the ACE trip is still open")
	}
	if _, err := a.Update(t.Context(), humanP, UpdateIn{Armed: &yes, Reason: "x", Backend: "nope"}); !errors.Is(err, provider.ErrNoBackend) {
		t.Errorf("unknown backend: %v", err)
	}
	// statusFor reads one backend's own state
	ss, err := a.statusFor(t.Context(), aceName)
	if err != nil || ss.State.Backend != aceName || ss.ColdStartS != 300 {
		t.Errorf("statusFor: %+v %v", ss.State, err)
	}
	if ss, err = a.statusFor(t.Context(), ""); err != nil || ss.State.Backend != provider.BackendFake {
		t.Errorf("statusFor default: %+v %v", ss.State, err)
	}
}

func TestRunner_ViewsCarryTheirBackendsConfig(t *testing.T) {
	t.Parallel()
	d := newDual(t, true)
	want := provider.WorkerExpect{Weights: map[string]string{"ACE-Step/Ace-Step1.5": "rev-a"}}
	d.run.UseSet(d.set, nil, map[string]provider.WorkerExpect{provider.EngineACEStep: want})
	vs := d.run.claimViews()
	if len(vs) != 2 {
		t.Fatalf("%d views", len(vs))
	}
	if vs[0].o.Backend.Name() != provider.BackendFake || vs[0].o.ColdStartS != 180 || vs[0].o.Expect.Weights["m-a-p/YuE2-3B"] == "" {
		t.Errorf("default view changed: cold=%d expect=%v", vs[0].o.ColdStartS, vs[0].o.Expect)
	}
	if vs[1].o.Backend.Name() != aceName || vs[1].o.ColdStartS != d.ace.ColdStartS() || vs[1].o.MaxRuntime != d.ace.MaxRuntime() ||
		vs[1].o.Expect.Weights["ACE-Step/Ace-Step1.5"] != "rev-a" {
		t.Errorf("ACE view: cold=%d runtime=%v expect=%v", vs[1].o.ColdStartS, vs[1].o.MaxRuntime, vs[1].o.Expect)
	}
	if vs[0].rstate != d.run.rstate || vs[1].rstate != d.run.rstate {
		t.Error("views must share the runner's lease and clocks")
	}
}

func TestRunner_PlainWorkerKeyWithoutASet(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	if got := r.run.workerKeyFor(provider.BackendFake, "w1"); got != workerKey([]byte(nil), "w1") {
		t.Errorf("no set must keep M3's plain key: %q", got)
	}
}

func TestRunner_ClaimErrorStopsTheStepForEveryBackend(t *testing.T) {
	t.Parallel()
	d := newDual(t, true)
	d.run.o.Fault = func(point string) error {
		if point == FaultAfterClaimCommit {
			return errCrash
		}
		return nil
	}
	d.seedBatch(batchSpec{jobs: 1})
	d.seedBatch(batchSpec{jobs: 1, backend: aceName})
	if _, err := d.run.Step(t.Context()); !errors.Is(err, errCrash) {
		t.Fatalf("Step: %v, want the injected crash", err)
	}
	if d.ace.Calls().Submit != 0 {
		t.Error("the second backend submitted after the step crashed")
	}
}

func TestRunner_CancelOnADepartedBackendSendsNothing(t *testing.T) {
	t.Parallel()
	d := newDual(t, true)
	ace := d.seedBatch(batchSpec{jobs: 1, backend: aceName})
	d.steps(2)
	only, err := provider.NewSet(provider.BackendFake, d.be)
	if err != nil {
		t.Fatal(err)
	}
	d.run.UseSet(only, nil, nil)
	if err := d.run.sendCancels(t.Context(), ace.Jobs[0], d.atts(ace.Jobs[0])); err != nil {
		t.Fatal(err)
	}
	if d.be.Calls().Cancel != 0 || d.ace.Calls().Cancel != 0 {
		t.Errorf("cancels: default %d, ACE %d", d.be.Calls().Cancel, d.ace.Calls().Cancel)
	}
}

func TestMeterRouter_UnknownBackendFallsBackToTheDefault(t *testing.T) {
	t.Parallel()
	d := newDual(t, true)
	rt := NewMeterRouter(d.reconcilers()[:1]) // only the default backend has a reconciler
	ace := d.seedBatch(batchSpec{jobs: 1, backend: aceName})
	if _, _, err := rt.OverheadSeconds(t.Context(), d.st.R, ace.Batch); err != nil {
		t.Errorf("fallback: %v", err)
	}
	if _, _, err := rt.OverheadSeconds(t.Context(), d.st.R, "no-such-batch"); err == nil {
		t.Error("a missing batch must be an error")
	}
}
