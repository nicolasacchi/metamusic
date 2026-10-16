package jobs

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

const testConsole = "https://console.example.com/serverless"

// recon builds a reconciler over the rig (runner `run` unless a modifier replaces it).
func (r *rig) recon(mods ...func(*ReconcilerOptions)) *Reconciler {
	r.t.Helper()
	o := ReconcilerOptions{Store: r.st, Backend: r.be, Spend: r.sp, Uploads: r.up, Notify: r.notes, Audit: audit.NewStore(r.st, r.newID), Runner: r.run,
		Clock: r.clk, NewID: r.newID, Owner: "runner-1", MeterKey: []byte("meter-key-for-tests-0123456789"), PublicURL: testPublicURL, ConsoleURL: testConsole,
		BillingLag: 6 * time.Hour, ResidualHold: 48 * time.Hour, ColdStartS: 180, KillSlackS: 15, MaxRuntime: 15 * time.Minute,
		GateCfg: provider.GateConfig{Mode: provider.ModeFake, KeysLoaded: true, ColdStartS: 180, MaxRuntime: 15 * time.Minute}, Location: time.UTC}
	for _, m := range mods {
		m(&o)
	}
	return NewReconciler(o)
}

// hold makes run the lease holder (renewing the lease), as its Run loop would.
func (r *rig) hold(run *Runner) {
	r.t.Helper()
	if ok, err := run.acquire(r.t.Context()); err != nil || !ok {
		r.t.Fatalf("acquire: %v %v", ok, err)
	}
}

// pass renews the lease and runs one reconciliation pass.
func (r *rig) pass(rec *Reconciler) error {
	r.t.Helper()
	r.hold(rec.o.Runner)
	return rec.Pass(r.t.Context())
}

func (r *rig) mustPass(rec *Reconciler) {
	r.t.Helper()
	if err := r.pass(rec); err != nil {
		r.t.Fatalf("Pass: %v", err)
	}
}

func (r *rig) auditOps(op string) int {
	r.t.Helper()
	return r.count(`SELECT COUNT(*) FROM audit_log WHERE op = ?`, op)
}

func (r *rig) noteTitles() []string {
	var out []string
	for _, m := range r.notes.all() {
		out = append(out, m.Title)
	}
	return out
}

func (r *rig) findNote(prefix string) (notify.Message, bool) {
	for _, m := range r.notes.all() {
		if strings.HasPrefix(m.Title, prefix) {
			return m, true
		}
	}
	return notify.Message{}, false
}

func (r *rig) openTripReason() string {
	return r.str(`SELECT reason FROM provider_trips WHERE reset_at IS NULL`)
}

func (r *rig) sweep(ws ...provider.Worker) {
	r.mu.Lock()
	r.sweepFn = func() (provider.SweepReport, error) {
		return provider.SweepReport{Workers: ws, ObservedAt: r.clk.Now()}, nil
	}
	r.mu.Unlock()
}

func upt(n int) *int { return &n }

func worker(id, status string, uptime *int, started time.Time) provider.Worker {
	return provider.Worker{ID: id, Status: status, UptimeS: uptime, StartedAt: &started, Image: "reg.example.com/img@sha256:" + strings.Repeat("c", 64)}
}

func TestReconciler_AdoptExpiredLeases(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	job := r.seedBatch(batchSpec{}).Jobs[0]
	r.step()
	r.wantState(job, StateStarting)
	if r.job(job).LeaseOwner != "runner-1" {
		t.Fatal("setup")
	}
	r.advance(2 * LeaseLength) // runner-1 died
	b := r.second("runner-2")
	rec := r.recon(func(o *ReconcilerOptions) { o.Runner = b })
	r.mustPass(rec)
	if got := r.job(job).LeaseOwner; got != "runner-2" {
		t.Errorf("lease owner %q, want runner-2", got)
	}
	if !r.hasEvent(job, EvAdopted) {
		t.Errorf("no adopted event: %v", r.events(job))
	}
	if r.auditOps("reconciler:adopt") != 1 {
		t.Error("adoption must be audited")
	}
	// a runner that does not hold the lease does nothing
	other := r.second("runner-3")
	rec3 := r.recon(func(o *ReconcilerOptions) { o.Runner = other })
	before := r.be.Calls().Total()
	if err := rec3.Pass(t.Context()); err != nil {
		t.Fatal(err)
	}
	if r.be.Calls().Total() != before {
		t.Error("a pass without the lease called the provider")
	}
}

func TestReconciler_PendingAttemptBecomesGhost(t *testing.T) {
	t.Parallel()
	c := newCrash(FaultAfterClaimCommit)
	r := newRig(t, withOpts(func(o *RunnerOptions) { o.Fault = c.fault }))
	b := r.seedBatch(batchSpec{})
	job := b.Jobs[0]
	if _, err := r.run.Step(t.Context()); !errors.Is(err, errCrash) {
		t.Fatalf("crash: %v", err)
	}
	if a := r.att(job, 1); a.State != "pending" {
		t.Fatalf("attempt %+v", a)
	}
	r.advance(2 * LeaseLength)
	nb := r.second("runner-2", func(o *RunnerOptions) { o.Fault = nil })
	rec := r.recon(func(o *ReconcilerOptions) { o.Runner = nb })
	r.mustPass(rec)
	if a := r.att(job, 1); a.State != "unknown" {
		t.Errorf("attempt %+v, want unknown", a)
	}
	if !r.hasEvent(job, EvGhost) {
		t.Errorf("events %v", r.events(job))
	}
	r.wantState(job, StateQueued)
	if r.ledger(b.Batch, "reserve", "topup_ambiguous") == 0 {
		t.Error("the ghost needs its ambiguous top-up before any resubmission")
	}
	if r.auditOps("reconciler:ghost") != 1 || r.auditOps("reconciler:adopt") != 1 {
		t.Error("adoption and ghost are audited")
	}
}

func TestReconciler_UnknownAttemptTimeBound(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(r *rig, o *RunnerOptions) { o.Audit = audit.NewStore(r.st, r.newID) })
	b := r.seedBatch(batchSpec{workersMax: 1})
	job := b.Jobs[0]
	g := r.ghost(job, "unknown", true)
	r.step()
	if r.att(job, 1).State != "unknown" {
		t.Fatal("setup: the ghost is live until its window closes")
	}
	r.advance(3*time.Hour + time.Second)
	r.step()
	a := r.att(job, 1)
	if a.ID != g || a.State != "done" || a.TerminalBasis != "time_bound" || !a.Settled {
		t.Fatalf("ghost after its window: %+v", a)
	}
	want := int64(money.ForSeconds(186+15, testPrice))
	if got := r.ledger(b.Batch, "settle", "execution_unknown"); got != want {
		t.Errorf("settled %d, want %d (timeout + kill slack)", got, want)
	}
	if r.auditOps("reconciler:settle_time_bound") != 1 {
		t.Error("a time-bound close is audited")
	}
	// a submitted attempt whose cancel was never confirmed closes the same way
	r2 := newRig(t)
	j2 := r2.seedBatch(batchSpec{}).Jobs[0]
	r2.submitOne(j2)
	r2.cancelIntent(j2)
	r2.step()
	r2.wantState(j2, StateCancelling)
	r2.advance(3*time.Hour + time.Minute)
	r2.step()
	r2.wantState(j2, StateCancelled)
	if a := r2.att(j2, 1); a.State != "done" || a.TerminalBasis != "time_bound" {
		t.Errorf("unconfirmed cancel: %+v", a)
	}
}

func TestReconciler_ApprovalExpiry(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	b := r.seedBatch(batchSpec{state: "awaiting_confirmation", jobs: 2})
	rec := r.recon()
	r.mustPass(rec)
	if r.str(`SELECT state FROM approvals WHERE batch_id = ?`, b.Batch) != "pending" {
		t.Fatal("a fresh approval must stay pending")
	}
	r.advance(24*time.Hour + time.Second)
	r.mustPass(rec)
	if got := r.str(`SELECT state || '|' || via || '|' || decided_by FROM approvals WHERE batch_id = ?`, b.Batch); got != "expired|expiry|system:reconciler" {
		t.Errorf("approval %q", got)
	}
	if got := r.str(`SELECT state FROM batches WHERE id = ?`, b.Batch); got != "expired" {
		t.Errorf("batch %q", got)
	}
	if r.str(`SELECT reconciled_at FROM batches WHERE id = ?`, b.Batch) == "" {
		t.Error("reconciled_at must be set")
	}
	if got, want := r.ledger(b.Batch, "release", "expired"), r.ledger(b.Batch, "reserve", ""); got != want || want == 0 {
		t.Errorf("released %d of %d", got, want)
	}
	if r.outstanding(b.Batch) != 0 {
		t.Error("nothing may stay outstanding")
	}
	r.step()
	for _, j := range b.Jobs {
		r.wantState(j, StateExpired)
	}
	if r.auditOps("reconciler:approval_expired") != 1 {
		t.Errorf("audit rows %d", r.auditOps("reconciler:approval_expired"))
	}
}

func TestReconciler_StuckRunningTrips(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.seedBatch(batchSpec{}) // an active batch, nothing running yet
	rec := r.recon()
	r.sweep(worker("w1", "RUNNING", upt(30), t0))
	r.mustPass(rec)
	if r.count(`SELECT COUNT(*) FROM provider_trips`) != 0 {
		t.Fatal("one observation must never trip")
	}
	r.advance(2*time.Minute + time.Second)
	r.sweep(worker("w1", "RUNNING", upt(150), t0))
	r.mustPass(rec)
	if got := r.openTripReason(); got != TripStuckRunning {
		t.Fatalf("trip %q", got)
	}
	r.mu.Lock()
	purges, scales := r.purges, r.scales
	r.mu.Unlock()
	if purges != 1 || scales != 1 {
		t.Errorf("purge %d, scale-to-zero %d", purges, scales)
	}
	if m, ok := r.findNote("Generation stopped: stuck_running"); !ok || m.Priority != 5 {
		t.Errorf("notes %v", r.noteTitles())
	}
	if r.auditOps("reconciler:trip") != 1 || r.auditOps("reconciler:purge_queue") != 1 || r.auditOps("reconciler:scale_to_zero") != 1 {
		t.Error("trip actions must be audited")
	}
	// acting is once per trip, however many passes follow
	r.advance(time.Minute)
	r.mustPass(rec)
	r.mu.Lock()
	again := r.purges
	r.mu.Unlock()
	if again != 1 {
		t.Errorf("purged %d times", again)
	}
}

func TestReconciler_StuckRunningIgnoresAnnouncedWorkers(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	job := r.seedBatch(batchSpec{}).Jobs[0]
	r.submitOne(job)
	r.exec(`UPDATE job_attempts SET announced_at = ? WHERE job_id = ?`, store.FormatTime(r.clk.Now()), job)
	rec := r.recon()
	for range 4 {
		r.sweep(worker("w1", "RUNNING", upt(30), t0))
		r.mustPass(rec)
		r.advance(2 * time.Minute)
	}
	if n := r.count(`SELECT COUNT(*) FROM provider_trips`); n != 0 {
		t.Errorf("an announced attempt's worker tripped (%d)", n)
	}
}

func TestReconciler_StuckNeedsTwoObservations(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.seedBatch(batchSpec{})
	rec := r.recon()
	r.sweep(worker("w1", "RUNNING", upt(30), t0))
	r.mustPass(rec)
	// the next sweep shows nothing running: the first observation is forgotten
	r.advance(3 * time.Minute)
	r.sweep()
	r.mustPass(rec)
	r.advance(3 * time.Minute)
	r.sweep(worker("w1", "RUNNING", upt(30), t0))
	r.mustPass(rec)
	if n := r.count(`SELECT COUNT(*) FROM provider_trips`); n != 0 {
		t.Fatal("two non-consecutive observations tripped")
	}
	r.advance(2*time.Minute + time.Second)
	r.mustPass(rec)
	if r.openTripReason() != TripStuckRunning {
		t.Error("two consecutive observations 2 minutes apart must trip")
	}
}

func TestReconciler_CrashLoopUsesUptime(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.seedBatch(batchSpec{jobs: 3}) // reserves enough that the meter stays quiet
	rec := r.recon()
	// a null uptime (unbilled image pull) never counts, however many sweeps see it
	for range 5 {
		r.sweep(worker("w1", "INITIALIZING", nil, t0))
		r.mustPass(rec)
		r.advance(time.Minute)
	}
	// exactly ColdStartS + 60 is still inside the allowance
	for range 3 {
		r.sweep(worker("w1", "INITIALIZING", upt(240), t0))
		r.mustPass(rec)
	}
	if r.count(`SELECT COUNT(*) FROM provider_trips`) != 0 {
		t.Fatalf("tripped inside the allowance: %s (%v)", r.openTripReason(), r.noteTitles())
	}
	r.sweep(worker("w1", "UNHEALTHY", upt(245), t0))
	r.mustPass(rec)
	if r.count(`SELECT COUNT(*) FROM provider_trips`) != 0 {
		t.Fatal("one observation tripped")
	}
	r.mustPass(rec)
	if got := r.openTripReason(); got != TripCrashLoop {
		t.Errorf("trip %q", got)
	}
}

func TestReconciler_ProviderAccessTrips(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		setup func(r *rig, rec *Reconciler, job string)
	}{
		{"poll", func(r *rig, _ *Reconciler, job string) {
			r.submitOne(job)
			r.mu.Lock()
			r.pollErr = provider.ErrAccess
			r.mu.Unlock()
			r.advance(11 * time.Second)
			r.step()
		}},
		{"cancel", func(r *rig, _ *Reconciler, job string) {
			r.submitOne(job)
			r.mu.Lock()
			r.cancelErr = provider.ErrAccess
			r.mu.Unlock()
			r.cancelIntent(job)
			r.step()
		}},
		{"sweep", func(r *rig, _ *Reconciler, _ string) {
			r.mu.Lock()
			r.sweepFn = func() (provider.SweepReport, error) { return provider.SweepReport{}, provider.ErrAccess }
			r.mu.Unlock()
		}},
		{"quote", func(r *rig, _ *Reconciler, _ string) {
			r.mu.Lock()
			r.quoteFn = func() (provider.Quote, error) { return provider.Quote{}, provider.ErrEndpointNotFound }
			r.mu.Unlock()
		}},
		{"billing", func(r *rig, _ *Reconciler, _ string) {
			r.mu.Lock()
			r.billingFn = func(time.Time, time.Time) ([]provider.BillBucket, error) { return nil, provider.ErrAccess }
			r.mu.Unlock()
			r.advance(2 * time.Hour)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			job := r.seedBatch(batchSpec{}).Jobs[0]
			rec := r.recon()
			c.setup(r, rec, job)
			r.mustPass(rec)
			if got := r.openTripReason(); got != TripProviderAccess {
				t.Fatalf("trip %q (notes %v)", got, r.noteTitles())
			}
			m, ok := r.findNote("RunPod keys refused")
			if !ok || m.Priority != 5 || !slices.Contains(m.Tags, "rotating_light") {
				t.Errorf("critical ntfy missing: %v", r.noteTitles())
			}
		})
	}
}

func TestReconciler_TripWithoutWriteKeyAlerts(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.seedBatch(batchSpec{})
	r.caps = provider.Capabilities{Billing: "hour"} // no scoped write key
	rec := r.recon()
	r.sweep(worker("w1", "RUNNING", upt(30), t0))
	r.mustPass(rec)
	r.advance(3 * time.Minute)
	r.mustPass(rec)
	if r.openTripReason() != TripStuckRunning {
		t.Fatal("no trip")
	}
	r.mu.Lock()
	scales := r.scales
	r.mu.Unlock()
	if scales != 0 {
		t.Errorf("ScaleToZero called %d times without the capability", scales)
	}
	m, ok := r.findNote("Set max workers to 0 now")
	if !ok || m.Priority != 5 || !slices.Contains(m.Tags, "rotating_light") || len(m.Actions) != 1 || m.Actions[0].URL != testConsole || m.Actions[0].Label != "Open console" {
		t.Errorf("manual scale-to-zero ntfy: %+v (%v)", m, r.noteTitles())
	}
	if _, ok := r.findNote("Generation stopped"); !ok {
		t.Error("the trip ntfy must still go out")
	}
}

func TestReconciler_ScaleToZeroErrorFallsBack(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.seedBatch(batchSpec{})
	r.mu.Lock()
	r.scaleFn = func() error { return provider.ErrAccess } // a rotated key, although the capability file says PATCH is allowed
	r.mu.Unlock()
	rec := r.recon()
	r.sweep(worker("w1", "RUNNING", upt(30), t0))
	r.mustPass(rec)
	r.advance(3 * time.Minute)
	r.mustPass(rec)
	r.mu.Lock()
	scales := r.scales
	r.mu.Unlock()
	if scales != 1 {
		t.Errorf("ScaleToZero called %d times", scales)
	}
	if _, ok := r.findNote("Set max workers to 0 now"); !ok {
		t.Errorf("the fallback ntfy is missing: %v", r.noteTitles())
	}
	if got := r.count(`SELECT COUNT(*) FROM audit_log WHERE op = 'reconciler:scale_to_zero' AND error_code IS NOT NULL`); got != 1 {
		t.Errorf("failed scale-to-zero audit rows %d", got)
	}
}

func TestReconciler_DriftRevertOrAlert(t *testing.T) {
	t.Parallel()
	drift := []provider.Drift{{Field: "gpu.selection", Want: "AMPERE_24", Got: "ADA_24"}}
	idle := 120
	for _, revert := range []bool{true, false} {
		r := newRig(t)
		r.seedBatch(batchSpec{})
		r.caps.RevertDrift = revert
		ep := providertestEndpoint(&idle)
		r.mu.Lock()
		r.sweepFn = func() (provider.SweepReport, error) {
			return provider.SweepReport{Drift: drift, Endpoint: &ep, ObservedAt: r.clk.Now()}, nil
		}
		r.mu.Unlock()
		rec := r.recon()
		r.mustPass(rec)
		r.mu.Lock()
		reverts := slices.Clone(r.reverts)
		r.mu.Unlock()
		m, ok := r.findNote("Endpoint drift: ")
		if !ok || m.Priority != 4 || !strings.Contains(m.Title, "gpu.selection") || !strings.Contains(m.Title, "workers.idleTimeout") {
			t.Fatalf("revert=%v: ntfy %+v (%v)", revert, m, r.noteTitles())
		}
		if revert {
			if len(reverts) != 1 || len(reverts[0]) != 2 || !strings.Contains(m.Body, "Reverted") {
				t.Errorf("reverts %v body %q", reverts, m.Body)
			}
			if r.auditOps("reconciler:revert_drift") != 1 {
				t.Error("revert not audited")
			}
		} else if len(reverts) != 0 || !strings.Contains(m.Body, "Not reverted") {
			t.Errorf("without the capability: reverts %v body %q", reverts, m.Body)
		}
		// the same drift does not repeat within the hour
		r.advance(time.Minute)
		n := len(r.notes.all())
		r.mustPass(rec)
		if len(r.notes.all()) != n {
			t.Error("drift ntfy repeated within the hour")
		}
		snap := rec.Snapshot()
		if len(snap.Drift) != 2 || snap.Endpoint == nil {
			t.Errorf("snapshot %+v", snap)
		}
	}
	// a failing revert is reported as not reverted
	r := newRig(t)
	r.seedBatch(batchSpec{})
	r.mu.Lock()
	r.revertFn = func([]provider.Drift) error { return provider.ErrNotPermitted }
	r.sweepFn = func() (provider.SweepReport, error) {
		return provider.SweepReport{Drift: drift, ObservedAt: r.clk.Now()}, nil
	}
	r.mu.Unlock()
	r.mustPass(r.recon())
	if m, ok := r.findNote("Endpoint drift"); !ok || !strings.Contains(m.Body, "Not reverted") {
		t.Errorf("failed revert: %+v", m)
	}
	if r.count(`SELECT COUNT(*) FROM audit_log WHERE op = 'reconciler:revert_drift' AND error_code IS NOT NULL`) != 1 {
		t.Error("a failed revert is an audit row with an error")
	}
}

func providertestEndpoint(idle *int) provider.EndpointConfig {
	return provider.EndpointConfig{Name: "mm-yue2", Type: "QUEUE", ScalerType: "QUEUE_DELAY", ScalerValue: "240", WorkersMax: 2, IdleTimeoutS: idle,
		TimeoutMS: 900000, GPUCount: 1, Pools: []string{"AMPERE_24"}, Flashboot: "FLASHBOOT"}
}

func TestDrift_FieldList(t *testing.T) {
	t.Parallel()
	want := []string{"type", "scaler.type", "scaler.queueDelay", "timeout", "gpu.count", "gpu.minCudaVersion", "gpu.allowedCudaVersions",
		"gpu.selection", "flashboot", "workers.min", "workers.max", "workers.idleTimeout", "image", "env", "requestUrls"}
	if !slices.Equal(DriftFields, want) {
		t.Errorf("DriftFields = %v", DriftFields)
	}
	for _, f := range LocalDriftFields {
		if !slices.Contains(DriftFields, f) {
			t.Errorf("local field %s is not in the list", f)
		}
	}
	cfg := provider.GateConfig{ColdStartS: 180, MaxRuntime: 15 * time.Minute}
	idle := 20
	good := providertestEndpoint(&idle)
	good.RequestURLs = map[string]string{"run": "https://api.runpod.ai/v2/x/run"}
	if d := LocalDrift(cfg, good); len(d) != 0 {
		t.Fatalf("a good endpoint drifts: %v", d)
	}
	bad := provider.EndpointConfig{Type: "LOAD_BALANCER", ScalerType: "REQUEST_COUNT", ScalerValue: "4", WorkersMin: 1, WorkersMax: 3, TimeoutMS: 1000, GPUCount: 2,
		RequestURLs: map[string]string{"run": "https://evil.example.com/run"}}
	got := map[string]bool{}
	for _, d := range LocalDrift(cfg, bad) {
		got[d.Field] = true
	}
	for _, f := range LocalDriftFields {
		if !got[f] {
			t.Errorf("LocalDrift misses %s", f)
		}
	}
	tooLong := 61
	good.IdleTimeoutS = &tooLong
	if d := LocalDrift(cfg, good); len(d) != 1 || d[0].Field != "workers.idleTimeout" {
		t.Errorf("idle 61: %v", d)
	}
}

func TestReconciler_WorkersNotZeroAlert(t *testing.T) {
	t.Parallel()
	for _, up := range []bool{true, false} {
		r := newRig(t)
		s := r.seedBatch(batchSpec{})
		r.exec(`UPDATE batches SET state = 'finished', finished_at = ? WHERE id = ?`, store.FormatTime(r.clk.Now()), s.Batch)
		rec := r.recon()
		if up {
			r.sweep(worker("w1", "IDLE", upt(100), t0))
		} else {
			r.sweep()
		}
		r.advance(20 * time.Second) // idle_s 20 + 30 s is not over yet
		r.mustPass(rec)
		if _, ok := r.findNote("Workers still running"); ok {
			t.Fatal("alerted before finished_at + idle_s + 30 s")
		}
		r.advance(31 * time.Second)
		r.mustPass(rec)
		m, ok := r.findNote("Workers still running")
		if ok != up {
			t.Fatalf("up=%v: alerted=%v", up, ok)
		}
		if up && m.Priority != 4 {
			t.Errorf("priority %d", m.Priority)
		}
		r.advance(time.Minute)
		r.mustPass(rec)
		n := 0
		for _, t := range r.noteTitles() {
			if t == "Workers still running" {
				n++
			}
		}
		if n > 1 {
			t.Errorf("alert repeated %d times", n)
		}
	}
}

func TestReconciler_SweepEvery10sWhileActive(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.recon()
	sweeps := func() int { return r.be.Calls().Sweep }
	r.hold(r.run)
	// idle: a full pass at start (one sweep), then one per 60 s
	tick := func(d time.Duration) {
		r.advance(d)
		r.hold(r.run)
		if err := rec.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if err := rec.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if sweeps() != 1 {
		t.Fatalf("first tick: %d sweeps", sweeps())
	}
	for range 5 {
		tick(10 * time.Second)
	}
	if sweeps() != 1 {
		t.Errorf("an idle system swept %d times in 50 s", sweeps())
	}
	tick(10 * time.Second)
	if sweeps() != 2 {
		t.Errorf("no pass at 60 s: %d sweeps", sweeps())
	}
	// a batch becomes active: every 10 s
	r.seedBatch(batchSpec{})
	base := sweeps()
	for range 4 {
		tick(10 * time.Second)
	}
	if got := sweeps() - base; got != 4 {
		t.Errorf("active: %d sweeps in 40 s, want 4", got)
	}
	tick(5 * time.Second)
	if got := sweeps() - base; got != 4 {
		t.Errorf("a tick 5 s after a sweep swept again (%d)", got)
	}
}

func TestReconciler_QuoteStaleAfter1h(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	be := newNamedBackend(r, provider.BackendRunPod)
	r.exec(`INSERT INTO provider_state(backend, armed, changed_by, changed_at) VALUES(?, 1, 'system:default', ?)`, provider.BackendRunPod, store.FormatTime(t0))
	gate := provider.NewGate(r.st.R, provider.GateConfig{Mode: provider.ModeRunPod, KeysLoaded: true, ColdStartS: 180, MaxRuntime: 15 * time.Minute}, r.clk.Now)
	rec := r.recon(func(o *ReconcilerOptions) { o.Backend = be })
	if st, _ := gate.State(t.Context()); st.Enabled || st.Reason != provider.ReasonQuoteStale {
		t.Fatalf("before any refresh: %+v", st)
	}
	r.mustPass(rec)
	st, err := gate.State(t.Context())
	if err != nil || !st.Enabled || st.Quote == nil || st.Quote.ListPricePPH != 690000 {
		t.Fatalf("after a refresh: %+v %v", st, err)
	}
	for _, u := range st.Quote.Endpoint.RequestURLs {
		if strings.Contains(u, "/v2/endpoint/") {
			t.Errorf("the cached quote keeps the endpoint id: %s", u)
		}
	}
	// the refresh fails from now on: the previous quote serves until it is an hour old
	r.mu.Lock()
	r.quoteFn = func() (provider.Quote, error) { return provider.Quote{}, errors.New("catalog down") }
	r.mu.Unlock()
	r.advance(59 * time.Minute)
	_ = r.pass(rec)
	if st, _ := gate.State(t.Context()); !st.Enabled {
		t.Errorf("59 minutes: %+v", st)
	}
	r.advance(2 * time.Minute)
	_ = r.pass(rec)
	if st, _ := gate.State(t.Context()); st.Enabled || st.Reason != provider.ReasonQuoteStale {
		t.Errorf("past 1 h: %+v", st)
	}
}

func TestReconciler_NotifyAndAuditNeverLeak(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.seedBatch(batchSpec{})
	rec := r.recon()
	r.sweep(worker("secret-worker-id-123", "RUNNING", upt(30), t0))
	r.mustPass(rec)
	r.advance(3 * time.Minute)
	r.mustPass(rec)
	for _, m := range r.notes.all() {
		all := m.Title + m.Body + m.Click
		for _, a := range m.Actions {
			all += a.URL
		}
		if strings.Contains(all, "secret-worker-id-123") || strings.Contains(all, "?") && !strings.HasPrefix(m.Click, testConsole) {
			t.Errorf("leak in %+v", m)
		}
	}
	if r.count(`SELECT COUNT(*) FROM worker_meter WHERE worker_key LIKE '%secret-worker%'`) != 0 {
		t.Error("the raw worker id is stored")
	}
}

var _ = context.Background

func TestDrift_TwoBadURLsAndWorkersAboveABatch(t *testing.T) {
	t.Parallel()
	cfg := provider.GateConfig{ColdStartS: 180, MaxRuntime: 15 * time.Minute}
	idle := 20
	ep := providertestEndpoint(&idle)
	ep.RequestURLs = map[string]string{"run": "https://evil.example.com/a", "status": "https://evil.example.com/b"}
	n := 0
	for _, d := range LocalDrift(cfg, ep) {
		if d.Field == "requestUrls" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("requestUrls reported %d times", n)
	}
	r := newRig(t)
	r.seedBatch(batchSpec{workersMax: 1})
	ep.RequestURLs = nil
	ep.WorkersMax = 2
	hi := 40
	ep.IdleTimeoutS = &hi
	r.mu.Lock()
	r.sweepFn = func() (provider.SweepReport, error) {
		return provider.SweepReport{Endpoint: &ep, ObservedAt: r.clk.Now()}, nil
	}
	r.mu.Unlock()
	r.mustPass(r.recon())
	m, ok := r.findNote("Endpoint drift: ")
	if !ok || !strings.Contains(m.Title, "idle timeout above a running batch") || !strings.Contains(m.Title, "workers.max above a running batch") {
		t.Errorf("ntfy %+v", m)
	}
}

func TestEnvID_NilWeightsAndErrors(t *testing.T) {
	t.Parallel()
	a, err := EnvID(EnvInput{Engine: "e"})
	b, _ := EnvID(EnvInput{Engine: "e", Weights: map[string]string{}})
	if err != nil || a != b {
		t.Errorf("nil and empty weights differ: %v", err)
	}
}

func TestReconciler_UploadSweep(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	job := r.seedBatch(batchSpec{}).Jobs[0]
	spec := r.submitOne(job)
	out := r.deliver(spec, deliverOpts{})
	r.setStatus(r.refOf(job, 1), completed(out, 60000))
	for range 4 {
		r.advance(11 * time.Second)
		r.step()
	}
	r.wantState(job, StateCompleted) // the stub ingester leaves the received files in place
	files := func() int {
		ents, _ := os.ReadDir(filepath.Join(r.dataDir(), "incoming", "uploads"))
		return len(ents)
	}
	n := files()
	if n == 0 {
		t.Fatal("no upload files")
	}
	rec := r.recon()
	r.mustPass(rec)
	if files() != n {
		t.Fatalf("files removed before the 24 h hold: %d -> %d", n, files())
	}
	// a stale part file goes with them
	if err := os.WriteFile(filepath.Join(r.dataDir(), "incoming", "uploads", "orphan.part"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := r.clk.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(filepath.Join(r.dataDir(), "incoming", "uploads", "orphan.part"), old, old)
	r.advance(25 * time.Hour)
	r.mustPass(rec)
	if files() != 0 {
		t.Errorf("%d files left after the hold", files())
	}
	if r.auditOps("reconciler:upload_sweep") != 1 {
		t.Errorf("audit rows %d", r.auditOps("reconciler:upload_sweep"))
	}
	r.advance(time.Minute)
	r.mustPass(rec)
	if r.auditOps("reconciler:upload_sweep") != 1 {
		t.Error("an empty sweep writes no row")
	}
}

// TestReconciler_WithdrawsStuckCancelledApprovals: a batch cancelled while awaiting confirmation before
// cancel withdrew its approval (jobs cancelled, batch awaiting, approval pending) is closed by the
// reconciler once, audited, and left alone afterwards; batches with a live job are untouched.
func TestReconciler_WithdrawsStuckCancelledApprovals(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	stuck := r.seedBatch(batchSpec{state: "awaiting_confirmation", jobs: 2, principal: "agent:a1"})
	live := r.seedBatch(batchSpec{state: "awaiting_confirmation", jobs: 2})
	half := r.seedBatch(batchSpec{state: "awaiting_confirmation", jobs: 2})
	ts := store.FormatTime(r.clk.Now())
	// what the old cancel_batch left: jobs cancelled and the pot released never_ran, nothing else
	for _, j := range stuck.Jobs {
		r.exec(`UPDATE jobs SET state = 'cancelled', error_code = 'CANCELLED_BY_USER', cancel_requested_at = ?, cancel_requested_by = 'agent:a1', finished_at = ?, updated_at = ? WHERE id = ?`, ts, ts, ts, j)
	}
	r.exec(`UPDATE jobs SET state = 'cancelled', finished_at = ?, updated_at = ? WHERE id = ?`, ts, ts, half.Jobs[0])
	reserved := r.ledger(stuck.Batch, "reserve", "")
	rel := r.ledger(stuck.Batch, "reserve", "") / 2
	for _, j := range stuck.Jobs {
		bid, jid := stuck.Batch, j
		if err := r.sp.RunTx(t.Context(), func(tx *sql.Tx) error {
			return r.sp.Ledger().Append(t.Context(), tx, spend.Entry{Kind: spend.KindRelease, MicroUSD: money.MicroUSD(rel), BatchID: &bid, JobID: &jid, PrincipalID: "agent:a1", Reason: "never_ran"})
		}); err != nil {
			t.Fatal(err)
		}
	}
	rec := r.recon()
	r.mustPass(rec)
	if got := r.str(`SELECT state || '|' || via || '|' || decided_by FROM approvals WHERE batch_id = ?`, stuck.Batch); got != "rejected|cancel|system:reconciler" {
		t.Errorf("approval %q", got)
	}
	if got := r.str(`SELECT state FROM batches WHERE id = ?`, stuck.Batch); got != "rejected" {
		t.Errorf("batch %q", got)
	}
	if r.outstanding(stuck.Batch) != 0 || r.ledger(stuck.Batch, "release", "rejected") != 0 {
		t.Errorf("ledger: outstanding %d, extra release %d (reserved %d)", r.outstanding(stuck.Batch), r.ledger(stuck.Batch, "release", "rejected"), reserved)
	}
	for _, b := range []seeded{live, half} {
		if got := r.str(`SELECT state FROM approvals WHERE batch_id = ?`, b.Batch); got != "pending" {
			t.Errorf("batch %s with a live job: approval %q", b.Batch, got)
		}
	}
	if r.auditOps("reconciler:approval_withdrawn") != 1 {
		t.Errorf("audit rows %d", r.auditOps("reconciler:approval_withdrawn"))
	}
	// idempotent
	r.advance(2 * time.Minute)
	r.mustPass(rec)
	if r.auditOps("reconciler:approval_withdrawn") != 1 {
		t.Errorf("audit rows after a second pass %d", r.auditOps("reconciler:approval_withdrawn"))
	}
}
