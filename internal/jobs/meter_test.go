package jobs

import (
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var meterKey = []byte("meter-key-for-tests-0123456789")

// meterRig builds a rig whose runner derives worker keys and asks the reconciler's meter before finishing a batch.
func meterRig(t *testing.T) (*rig, *Reconciler) {
	t.Helper()
	r := newRig(t, withOpts(func(o *RunnerOptions) { o.MeterKey = meterKey }))
	rec := r.recon()
	r.run.o.Meter = rec
	return r, rec
}

// running scripts the attempt's status as Running on worker w and polls once.
func (r *rig) running(job, w string) {
	r.t.Helper()
	r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Running, Raw: "IN_PROGRESS", WorkerID: w})
	r.advance(11 * time.Second)
	r.step()
}

func TestSettle_OverheadFromMeter(t *testing.T) {
	t.Parallel()
	r, rec := meterRig(t)
	s := r.seedBatch(batchSpec{})
	job := s.Jobs[0]
	spec := r.submitOne(job)
	r.running(job, "w1")
	r.wantState(job, StateGenerating)
	if got := r.att(job, 1).WorkerKey; got != workerKey(meterKey, "w1") {
		t.Fatalf("worker key %q", got)
	}
	// uptime_s is billable seconds accumulated from observations: 10 s on the first look, then 120 s more
	r.sweep(worker("w1", "RUNNING", upt(10), t0))
	r.mustPass(rec)
	r.advance(120 * time.Second)
	r.sweep(worker("w1", "RUNNING", upt(130), t0))
	r.mustPass(rec)
	if got := r.count(`SELECT uptime_s FROM worker_meter`); got != 130 {
		t.Fatalf("billable seconds %d, want 130", got)
	}
	out := r.deliver(spec, deliverOpts{})
	st := completed(out, 60000)
	st.WorkerID = "w1"
	r.setStatus(r.refOf(job, 1), st)
	r.steps(4)
	r.wantState(job, StateCompleted)
	if r.str(`SELECT state FROM batches WHERE id = ?`, s.Batch) != "active" {
		t.Fatal("the batch must wait for its worker lifetime to end")
	}
	// the worker has vanished from the next sweep: lifetime = 130 s + one sweep gap, minus 60 s of execution
	r.advance(10 * time.Second)
	r.sweep()
	r.mustPass(rec)
	r.step()
	if got := r.str(`SELECT state FROM batches WHERE id = ?`, s.Batch); got != "finished" {
		t.Fatalf("batch %s", got)
	}
	want := int64(money.ForSeconds(130+10-60, testPrice))
	if got := r.ledger(s.Batch, "settle", "overhead"); got != want {
		t.Errorf("overhead settled %d, want %d", got, want)
	}
	if got := r.ledger(s.Batch, "settle", "execution"); got != int64(money.ForMillis(60000, testPrice)) {
		t.Errorf("execution settled %d", got)
	}
	if r.count(`SELECT COUNT(*) FROM ledger WHERE batch_id = ? AND kind = 'residual'`, s.Batch) != 1 {
		t.Error("residual row missing")
	}
}

func TestBatch_NothingRanSettlesZeroOverhead(t *testing.T) {
	t.Parallel()
	r, _ := meterRig(t)
	s := r.seedBatch(batchSpec{jobs: 2})
	for _, j := range s.Jobs {
		r.cancelIntent(j)
	}
	r.steps(2)
	for _, j := range s.Jobs {
		r.wantState(j, StateCancelled)
	}
	if got := r.str(`SELECT state FROM batches WHERE id = ?`, s.Batch); got != "finished" {
		t.Fatalf("batch %s", got)
	}
	if got := r.ledger(s.Batch, "settle", "overhead"); got != 0 {
		t.Errorf("overhead %d, want 0", got)
	}
	if r.count(`SELECT COUNT(*) FROM ledger WHERE batch_id = ? AND kind = 'settle' AND reason = 'overhead'`, s.Batch) != 1 {
		t.Error("a zero overhead settle row is the record that nothing ran")
	}
}

func TestReconciler_MeterTrip(t *testing.T) {
	t.Parallel()
	r, rec := meterRig(t)
	job := r.seedBatch(batchSpec{}).Jobs[0]
	r.submitOne(job)
	r.running(job, "w1")
	r.sweep(worker("w1", "RUNNING", upt(100), t0))
	r.mustPass(rec)
	if r.count(`SELECT COUNT(*) FROM provider_trips`) != 0 {
		t.Fatal("tripped inside the reservation")
	}
	// the worker keeps running far past what the batch reserved (cold start twice as long as allowed):
	// billable time accrues per observation, so the trip comes once the RUNNING seconds exceed the reserve
	for el := 100; el < 3600 && r.count(`SELECT COUNT(*) FROM provider_trips`) == 0; el += 30 {
		r.advance(30 * time.Second)
		r.sweep(worker("w1", "RUNNING", upt(el+30), t0))
		r.mustPass(rec)
	}
	if got := r.openTripReason(); got != TripMeterExceeds {
		t.Fatalf("trip %q", got)
	}
	if _, ok := r.findNote("Worker time above reservation"); !ok {
		t.Errorf("notes %v", r.noteTitles())
	}
	if n := r.count(`SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event = 'trip'`, job); n != 1 {
		t.Errorf("trip events %d", n)
	}
	r.wantState(job, StateCancelling)
	r.wantCode(job, CodeProviderTripped)
	if r.cancelCount(r.refOf(job, 1)) == 0 {
		t.Error("a trip cancels the live attempts")
	}
}

// meterFixture inserts finished attempts on one worker lifetime that served two batches.
func TestMeter_WarmWorkerTwoBatches(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.recon()
	b1 := r.seedBatch(batchSpec{})
	r.advance(10 * time.Minute)
	b2 := r.seedBatch(batchSpec{})
	key := workerKey(meterKey, "warm")
	attempt := func(job string, n int, submitted time.Time) {
		r.exec(`INSERT INTO job_attempts(id, job_id, n, backend, state, submitted_at, window_ends_at, price_pph_micro_usd, execution_ms, terminal_basis, terminal_at, settled, worker_key, created_at)
			VALUES(?, ?, ?, 'fake', 'done', ?, ?, 690000, 60000, 'provider', ?, 1, ?, ?)`, r.newID(), job, n, store.FormatTime(submitted),
			store.FormatTime(submitted.Add(3*time.Hour)), store.FormatTime(submitted.Add(3*time.Minute)), key, store.FormatTime(submitted))
	}
	attempt(b1.Jobs[0], 1, t0)
	attempt(b2.Jobs[0], 1, t0.Add(10*time.Minute))
	r.exec(`INSERT INTO worker_meter(worker_key, started_at, first_seen_at, last_seen_at, uptime_s, status_last) VALUES(?,?,?,?,1000,'EXITED')`,
		key, store.FormatTime(t0), store.FormatTime(t0), store.FormatTime(t0.Add(20*time.Minute)))
	r.advance(10 * time.Minute)
	got1, ready1, err := rec.OverheadSeconds(t.Context(), r.st.R, b1.Batch)
	if err != nil || !ready1 {
		t.Fatalf("batch 1: %d %v %v", got1, ready1, err)
	}
	// 1000 s billed + the sweep gap, minus 2 x 60 s of execution: charged to the earlier batch only
	if want := int64(1000 + 10 - 120); got1 != want {
		t.Errorf("batch 1 overhead %d s, want %d", got1, want)
	}
	got2, ready2, err := rec.OverheadSeconds(t.Context(), r.st.R, b2.Batch)
	if err != nil || !ready2 || got2 != 0 {
		t.Errorf("batch 2: %d %v %v, want 0 and ready", got2, ready2, err)
	}
}

func TestMeterStats(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	for i, k := range []string{"a", "b", "c"} {
		r.exec(`INSERT INTO worker_meter(worker_key, started_at, first_seen_at, last_seen_at, uptime_s, status_last) VALUES(?,?,?,?,?,'RUNNING')`, k,
			store.FormatTime(t0.Add(time.Duration(i)*time.Minute)), store.FormatTime(t0), store.FormatTime(t0.Add(time.Duration(i+2)*time.Minute)), 100*(i+1))
	}
	st, err := meterStats(t.Context(), r.st.R, t0.Add(time.Hour))
	if err != nil || st.Lifetimes != 3 || st.BilledSeconds != 600 || st.MaxWorkers != 3 {
		t.Errorf("%+v %v", st, err)
	}
	if st, _ = meterStats(t.Context(), r.st.R, t0.Add(48*time.Hour)); st.Lifetimes != 0 {
		t.Errorf("old lifetimes counted: %+v", st)
	}
}

func TestMeterModel_ExecutionSeconds(t *testing.T) {
	t.Parallel()
	m := &meterModel{now: t0.Add(100 * time.Second)}
	ann, started := t0.Add(40*time.Second), t0.Add(70*time.Second)
	ms := int64(59001)
	for name, c := range map[string]struct {
		a    meterAttempt
		want int64
	}{
		"live, never seen running":         {meterAttempt{live: true, timeoutS: 186, killSlackS: 15}, 0},
		"live, announced":                  {meterAttempt{live: true, ranEvidence: true, announcedAt: &ann, timeoutS: 186, killSlackS: 15}, 60},
		"live, seen running":               {meterAttempt{live: true, ranEvidence: true, jobStarted: &started, timeoutS: 186, killSlackS: 15}, 30},
		"live, submitted time as fallback": {meterAttempt{live: true, ranEvidence: true, submitted: t0, timeoutS: 186, killSlackS: 15}, 100},
		"live, capped at the time bound":   {meterAttempt{live: true, ranEvidence: true, submitted: t0, timeoutS: 60, killSlackS: 15}, 75},
		"done, measured, rounded up":       {meterAttempt{execMS: &ms}, 60},
		"done, time bound":                 {meterAttempt{timeBound: true, timeoutS: 186, killSlackS: 15}, 201},
		"done, nothing known":              {meterAttempt{}, 0},
	} {
		if got := m.execSeconds(c.a); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
}

// TestReconciler_MeterTripOnUnattributedTime: worker time that belongs to no batch still counts against the
// reserves of the open batches (C76.6).
func TestReconciler_MeterTripOnUnattributedTime(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	s := r.seedBatch(batchSpec{})
	r.exec(`UPDATE batches SET state = 'finished', finished_at = ? WHERE id = ?`, store.FormatTime(r.clk.Now().Add(time.Minute)), s.Batch)
	// a lifetime far after the batch's activity window: nobody's, but it started after the oldest open batch
	r.exec(`INSERT INTO worker_meter(worker_key, started_at, first_seen_at, last_seen_at, uptime_s, status_last) VALUES('orphan', ?, ?, ?, 3600, 'RUNNING')`,
		store.FormatTime(r.clk.Now().Add(time.Hour)), store.FormatTime(r.clk.Now()), store.FormatTime(r.clk.Now()))
	r.advance(2 * time.Hour)
	rec := r.recon()
	over, detail, err := rec.meterOver(t.Context())
	if err != nil || !over || detail["used_micro_usd"].(int64) <= detail["reserved_micro_usd"].(int64) {
		t.Fatalf("meterOver: %v %v %v", over, detail, err)
	}
	// and nothing is over when no batch is open
	r2 := newRig(t)
	over, _, err = r2.recon().meterOver(t.Context())
	if err != nil || over {
		t.Errorf("empty system: %v %v", over, err)
	}
}

// TestMeterModel_SeveralWorkers: attempts find their own worker's lifetime, a lifetime recorded after the
// attempt ended still carries it, and an unknown batch is not found.
func TestMeterModel_SeveralWorkers(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	s := r.seedBatch(batchSpec{jobs: 2})
	ka, kb := workerKey(meterKey, "a"), workerKey(meterKey, "b")
	for _, row := range []struct {
		key  string
		from time.Time
	}{{ka, t0.Add(-time.Hour)}, {ka, t0.Add(time.Minute)}, {kb, t0.Add(2 * time.Minute)}} {
		r.exec(`INSERT INTO worker_meter(worker_key, started_at, first_seen_at, last_seen_at, uptime_s, status_last) VALUES(?,?,?,?,300,'EXITED')`,
			row.key, store.FormatTime(row.from), store.FormatTime(row.from), store.FormatTime(row.from.Add(5*time.Minute)))
	}
	att := func(job, key string, n int, at time.Time) {
		r.exec(`INSERT INTO job_attempts(id, job_id, n, backend, state, submitted_at, window_ends_at, price_pph_micro_usd, execution_ms, terminal_basis, terminal_at, settled, worker_key, created_at)
			VALUES(?, ?, ?, 'fake', 'done', ?, ?, 690000, 60000, 'provider', ?, 1, ?, ?)`, r.newID(), job, n, store.FormatTime(at), store.FormatTime(at.Add(3*time.Hour)),
			store.FormatTime(at.Add(2*time.Minute)), key, store.FormatTime(at))
	}
	att(s.Jobs[0], ka, 1, t0)                      // ends inside a's second lifetime window? (t0+2m >= t0+1m): the later lifetime
	att(s.Jobs[1], kb, 1, t0.Add(-30*time.Minute)) // b's only lifetime started after this attempt ended
	r.advance(time.Hour)
	m, err := loadMeter(t.Context(), r.st.R, r.clk.Now(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if m.batchByID("nope") != nil || m.batchByID(s.Batch) == nil {
		t.Error("batchByID")
	}
	for _, a := range m.attempts {
		i := m.lifetimeOf(a)
		switch a.key {
		case ka:
			if i < 0 || !m.lifetimes[i].started.Equal(t0.Add(time.Minute)) {
				t.Errorf("attempt on a -> lifetime %d", i)
			}
		case kb:
			if i < 0 || m.lifetimes[i].key != kb {
				t.Errorf("attempt on b -> lifetime %d", i)
			}
		}
	}
	if i := m.lifetimeOf(meterAttempt{key: "unknown"}); i != -1 {
		t.Errorf("unknown worker -> %d", i)
	}
}
