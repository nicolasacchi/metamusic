package jobs

import (
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The meter bills RUNNING time (plus at most the idle timeout after it), never the listing's uptimeSeconds,
// which counts from the creation of the worker record (X3 canary #3, 2026-10-15).

const testIdleS = 20

// listWith makes every following sweep return fn(now) as the listing, with /health counts derived from it
// (a RUNNING worker has a job in progress, so stuck_running stays out of the way) and an endpoint whose idle
// timeout is testIdleS.
func (r *rig) listWith(fn func(now time.Time) []provider.Worker) {
	r.mu.Lock()
	r.sweepFn = func() (provider.SweepReport, error) {
		ws := fn(r.clk.Now())
		var h provider.Health
		for _, w := range ws {
			switch w.Status {
			case "RUNNING":
				h.Running++
				h.InProgress++
			case "IDLE":
				h.Idle++
			case "INITIALIZING":
				h.Initializing++
			}
		}
		idle := testIdleS
		return provider.SweepReport{Workers: ws, Health: &h, ObservedAt: r.clk.Now(),
			Endpoint: &provider.EndpointConfig{Name: "mm-yue2", WorkersMax: 2, IdleTimeoutS: &idle}}, nil
	}
	r.mu.Unlock()
}

// drive passes every step for total, advancing the fake clock after each pass.
func (r *rig) drive(rec *Reconciler, total, step time.Duration) {
	r.t.Helper()
	for el := time.Duration(0); el < total; el += step {
		r.mustPass(rec)
		r.advance(step)
	}
}

// phased is one worker record created `age` before the first observation whose status follows the phases;
// its listing uptime counts from the record's creation, like RunPod's.
type phase struct {
	status string
	d      time.Duration
}

func phased(id string, created, from time.Time, phases ...phase) func(time.Time) provider.Worker {
	return func(now time.Time) provider.Worker {
		el, st := now.Sub(from), phases[len(phases)-1].status
		var acc time.Duration
		for _, p := range phases {
			if acc += p.d; el < acc {
				st = p.status
				break
			}
		}
		return worker(id, st, upt(int(now.Sub(created).Seconds())), created)
	}
}

func (r *rig) meterRows() map[string]int {
	r.t.Helper()
	rows, err := r.st.R.QueryContext(r.t.Context(), `SELECT worker_key, uptime_s FROM worker_meter`)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var u int
		if err := rows.Scan(&k, &u); err != nil {
			r.t.Fatal(err)
		}
		out[k] = u
	}
	return out
}

func TestMeterBillable_Table(t *testing.T) {
	t.Parallel()
	hourAgo := t0.Add(-time.Hour)
	for _, tc := range []struct {
		name  string
		total time.Duration
		step  time.Duration
		ws    []func(time.Time) provider.Worker
		want  map[string]int // worker id -> billable seconds; absent id means no row
	}{
		{name: "initializing 40 min then idle 60 min bills nothing and writes no row", total: 100 * time.Minute, step: time.Minute,
			ws:   []func(time.Time) provider.Worker{phased("w1", t0, t0, phase{"INITIALIZING", 40 * time.Minute}, phase{"IDLE", 60 * time.Minute})},
			want: map[string]int{}},
		{name: "running 300 s then idle: 300 s plus the idle timeout", total: 20 * time.Minute, step: 10 * time.Second,
			ws:   []func(time.Time) provider.Worker{phased("w1", hourAgo, t0, phase{"RUNNING", 300 * time.Second}, phase{"IDLE", time.Hour})},
			want: map[string]int{"w1": 300 + testIdleS}},
		{name: "flashboot running, idle for 20 min, running again", total: 30 * time.Minute, step: 10 * time.Second,
			ws: []func(time.Time) provider.Worker{phased("w1", hourAgo, t0, phase{"RUNNING", 120 * time.Second}, phase{"IDLE", 20 * time.Minute},
				phase{"RUNNING", 120 * time.Second}, phase{"IDLE", time.Hour})},
			want: map[string]int{"w1": 120 + testIdleS + 120 + testIdleS}},
		{name: "two standby records with the same startedAt, one serves", total: 30 * time.Minute, step: 10 * time.Second,
			ws: []func(time.Time) provider.Worker{
				phased("w1", hourAgo, t0, phase{"IDLE", time.Hour}),
				phased("w2", hourAgo, t0, phase{"IDLE", 10 * time.Minute}, phase{"RUNNING", 100 * time.Second}, phase{"IDLE", time.Hour})},
			want: map[string]int{"w2": 100 + testIdleS}},
		{name: "throttled is not billed", total: 10 * time.Minute, step: 10 * time.Second,
			ws:   []func(time.Time) provider.Worker{phased("w1", hourAgo, t0, phase{"RUNNING", 60 * time.Second}, phase{"THROTTLED", time.Hour})},
			want: map[string]int{"w1": 60}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			rec := r.recon()
			r.listWith(func(now time.Time) []provider.Worker {
				var out []provider.Worker
				for _, f := range tc.ws {
					out = append(out, f(now))
				}
				return out
			})
			r.drive(rec, tc.total, tc.step)
			got := r.meterRows()
			want := map[string]int{}
			for id, s := range tc.want {
				want[workerKey(meterKey, id)] = s
			}
			if len(got) != len(want) {
				t.Fatalf("rows %v, want %v", got, want)
			}
			for k, s := range want {
				if got[k] != s {
					t.Errorf("billable seconds %d, want %d (rows %v)", got[k], s, got)
				}
			}
			if n := r.tripCount(); n != 0 {
				t.Errorf("trips %d", n)
			}
		})
	}
}

// A restart loses the in-memory observation: the first look credits at most one sweep and the stored total
// carries on from the row; a long outage is not billed as running.
func TestMeterBillable_RestartMidRunning(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.recon()
	r.listWith(func(now time.Time) []provider.Worker {
		return []provider.Worker{phased("w1", t0.Add(-time.Hour), t0, phase{"RUNNING", 24 * time.Hour})(now)}
	})
	r.drive(rec, 100*time.Second, 10*time.Second)
	key := workerKey(meterKey, "w1")
	if got := r.meterRows()[key]; got != 100 {
		t.Fatalf("before the restart %d, want 100", got)
	}
	rec2 := r.recon() // restart
	r.advance(10 * time.Minute)
	r.mustPass(rec2)
	if got := r.meterRows()[key]; got != 110 {
		t.Errorf("after a restart with a 10 minute gap %d, want 110 (one sweep credited)", got)
	}
	r.advance(10 * time.Second)
	r.mustPass(rec2)
	if got := r.meterRows()[key]; got != 120 {
		t.Errorf("then %d, want 120", got)
	}
	if n := r.count(`SELECT COUNT(*) FROM worker_meter`); n != 1 {
		t.Errorf("rows %d", n)
	}
}

// Observing twice at the same instant (a retried transaction or a second sweep) cannot add time.
func TestMeterBillable_Idempotent(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.recon()
	r.listWith(func(now time.Time) []provider.Worker {
		return []provider.Worker{phased("w1", t0.Add(-time.Hour), t0, phase{"RUNNING", 24 * time.Hour})(now)}
	})
	r.drive(rec, 60*time.Second, 10*time.Second)
	r.mustPass(rec) // one more sweep at the same instant as the last observation
	a := r.meterRows()
	r.mustPass(rec)
	for k, v := range r.meterRows() {
		if a[k] != v {
			t.Errorf("a repeated observation at the same instant changed %s: %d -> %d", k, a[k], v)
		}
	}
}

// A listed worker whose (key, startedAt) is held by a pre-fix row gets a distinct lifetime: its raw
// uptime cannot be lowered (monotonic trigger) nor trusted.
func TestMeterBillable_LegacyRowIsNotReused(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.exec(`DELETE FROM settings WHERE key = ?`, settingMeterBillableSince)
	key, created := workerKey(meterKey, "w1"), t0.Add(-2*time.Hour)
	r.exec(`INSERT INTO worker_meter(worker_key, started_at, first_seen_at, last_seen_at, uptime_s, status_last) VALUES(?,?,?,?,7200,'IDLE')`,
		key, store.FormatTime(created), store.FormatTime(created), store.FormatTime(t0))
	rec := r.recon()
	r.listWith(func(now time.Time) []provider.Worker {
		return []provider.Worker{phased("w1", created, t0, phase{"RUNNING", time.Hour})(now)}
	})
	r.drive(rec, 60*time.Second, 10*time.Second)
	if n := r.count(`SELECT COUNT(*) FROM worker_meter WHERE worker_key = ?`, key); n != 2 {
		t.Fatalf("rows %d, want the legacy one plus a new one", n)
	}
	if got := r.count(`SELECT uptime_s FROM worker_meter WHERE worker_key = ? AND uptime_s < 7200`, key); got != 60 {
		t.Errorf("new row billable seconds %d, want 60", got)
	}
	st, err := meterStats(t.Context(), r.st.R, r.clk.Now())
	if err != nil || st.Lifetimes != 1 || st.BilledSeconds != 60 {
		t.Errorf("provider status meter %+v %v: the legacy row must not count", st, err)
	}
}

// The numbers get_provider_status shows are billable seconds.
func TestMeterBillable_ProviderStatusMatches(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.recon()
	r.listWith(func(now time.Time) []provider.Worker {
		return []provider.Worker{phased("w1", t0.Add(-time.Hour), t0, phase{"RUNNING", 300 * time.Second}, phase{"IDLE", time.Hour})(now)}
	})
	r.drive(rec, 15*time.Minute, 10*time.Second)
	a := r.admin(provider.ModeFake, true, func(o *ProviderOptions) {
		o.Sweep = rec.Snapshot
		o.Capabilities = r.be.Capabilities
	})
	st, err := a.Status(t.Context(), auth.Principal{Kind: auth.KindHuman, ID: "h1", Name: "n"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Meter == nil || st.Meter.Lifetimes != 1 || st.Meter.BilledSeconds != 300+testIdleS {
		t.Errorf("meter %+v, want 1 lifetime and %d billable seconds (the listing says %d)", st.Meter, 300+testIdleS, 3600+15*60)
	}
}

// The first sweep records meter.billable_since once, audited; later sweeps leave it alone.
func TestMeterBillable_SinceSetOnFirstSweep(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.exec(`DELETE FROM settings WHERE key = ?`, settingMeterBillableSince)
	rec := r.recon()
	r.mustPass(rec)
	want := store.FormatTime(t0)
	if got := r.str(`SELECT value FROM settings WHERE key = ?`, settingMeterBillableSince); got != want {
		t.Fatalf("billable_since %q, want %q", got, want)
	}
	if n := r.auditOps("reconciler:meter_billable_since"); n != 1 {
		t.Errorf("audit rows %d", n)
	}
	r.advance(time.Hour)
	r.mustPass(rec)
	r.mustPass(r.recon()) // and after a restart
	if got := r.str(`SELECT value FROM settings WHERE key = ?`, settingMeterBillableSince); got != want {
		t.Errorf("billable_since moved to %q", got)
	}
	if n := r.auditOps("reconciler:meter_billable_since"); n != 1 {
		t.Errorf("audit rows %d after more sweeps", n)
	}
}

// X3 canary #3 replayed: the four rows RunPod's raw uptime produced, a live canary attempt on the standby
// worker, a reserve of about 214000 micro-USD. The old meter tripped meter_exceeds_reserved.
func TestMeterBillable_IncidentReplay(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.exec(`DELETE FROM settings WHERE key = ?`, settingMeterBillableSince) // as in production before the fix
	r.clk.Advance(time.Date(2026, 10, 15, 15, 12, 51, 0, time.UTC).Sub(r.clk.Now()))
	s := r.seedBatch(batchSpec{timeoutS: 1104})
	if got := r.sum(`SELECT SUM(micro_usd) FROM ledger WHERE batch_id = ? AND kind = 'reserve'`, s.Batch); got < 214000 || got > 215000 {
		t.Fatalf("reserve %d", got)
	}
	for _, w := range []struct {
		key, started, first, last string
		up                        int
		st                        string
	}{
		{"a5ccde82", "2026-10-15T13:22:28.232Z", "2026-10-15T13:22:28.232Z", "2026-10-15T15:12:02.494Z", 6574, "IDLE"},
		{"4b2eec55", "2026-10-15T13:22:28.232Z", "2026-10-15T14:09:00.995Z", "2026-10-15T15:12:02.494Z", 6574, "IDLE"},
		{"9e39c01d", "2026-10-15T15:12:57.344Z", "2026-10-15T15:13:45.714Z", "2026-10-15T15:13:45.714Z", 15, "RUNNING"},
		{"64d618ef", "2026-10-15T15:13:34.852Z", "2026-10-15T15:13:34.852Z", "2026-10-15T15:13:34.852Z", 0, "RUNNING"},
	} {
		r.exec(`INSERT INTO worker_meter(worker_key, started_at, first_seen_at, last_seen_at, uptime_s, status_last) VALUES(?,?,?,?,?,?)`,
			w.key, w.started, w.first, w.last, w.up, w.st)
	}
	job := s.Jobs[0]
	r.exec(`INSERT INTO job_attempts(id, job_id, n, backend, state, submitted_at, window_ends_at, price_pph_micro_usd, settled, worker_key, ref, created_at)
		VALUES(?, ?, 1, 'fake', 'submitted', ?, ?, 690000, 0, 'a5ccde82', 'ref-canary', ?)`, r.newID(), job, store.FormatTime(r.clk.Now()), store.FormatTime(r.clk.Now().Add(3*time.Hour)), store.FormatTime(r.clk.Now()))
	r.clk.Advance(54 * time.Second)
	// the listing is the incident's: the two standby records, IDLE, with their raw uptime
	r.listWith(func(now time.Time) []provider.Worker {
		c := time.Date(2026, 10, 15, 13, 22, 28, 232e6, time.UTC)
		return []provider.Worker{worker("standby-a", "IDLE", upt(int(now.Sub(c).Seconds())), c), worker("standby-b", "IDLE", upt(int(now.Sub(c).Seconds())), c)}
	})
	rec := r.recon()
	r.mustPass(rec)
	if got := r.tripCount(); got != 0 {
		t.Fatalf("the incident replays: %q tripped", r.openTripReason())
	}
	if over, d, err := rec.meterOver(t.Context()); err != nil || over {
		t.Fatalf("meterOver %v %v %v", over, d, err)
	}
	// a genuine overrun later still trips: a worker RUNNING for longer than the reserve (638 s at this price)
	cr := r.clk.Now()
	r.listWith(func(now time.Time) []provider.Worker {
		return []provider.Worker{phased("real", cr, cr, phase{"RUNNING", 24 * time.Hour})(now)}
	})
	for range 300 {
		r.advance(10 * time.Second)
		r.mustPass(rec)
		if r.tripCount() > 0 {
			break
		}
	}
	if got := r.openTripReason(); got != TripMeterExceeds {
		t.Fatalf("a real overrun did not trip meter_exceeds_reserved: %q", got)
	}
	if el := r.clk.Now().Sub(cr); el < 1000*time.Second {
		t.Errorf("tripped after only %v of RUNNING time", el)
	}
}

// Finished canary batches that sat behind the bogus lifetimes close in a billing window once billing is
// readable and quiet: worker_meter plays no part in it.
func TestMeterBillable_FinishedCanaryBatchesReconcile(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.exec(`DELETE FROM settings WHERE key = ?`, settingMeterBillableSince)
	r.clk.Advance(time.Date(2026, 10, 15, 13, 0, 0, 0, time.UTC).Sub(r.clk.Now()))
	var ids []string
	for range 3 {
		s := r.seedBatch(batchSpec{})
		r.exec(`INSERT INTO job_attempts(id, job_id, n, backend, state, submitted_at, window_ends_at, price_pph_micro_usd, execution_ms, terminal_basis, terminal_at, settled, created_at)
			VALUES(?, ?, 1, 'fake', 'done', ?, ?, 690000, 60000, 'provider', ?, 1, ?)`, r.newID(), s.Jobs[0], store.FormatTime(r.clk.Now()), store.FormatTime(r.clk.Now().Add(time.Hour)),
			store.FormatTime(r.clk.Now().Add(time.Minute)), store.FormatTime(r.clk.Now()))
		r.exec(`UPDATE batches SET state = 'finished', finished_at = ? WHERE id = ?`, store.FormatTime(r.clk.Now().Add(2*time.Minute)), s.Batch)
		ids = append(ids, s.Batch)
		r.advance(20 * time.Minute)
	}
	rec := r.recon()
	r.mustPass(rec)
	if n := r.count(`SELECT COUNT(*) FROM batches WHERE reconciled_at IS NOT NULL`); n != 0 {
		t.Fatalf("reconciled %d before the billing lag", n)
	}
	r.advance(8 * time.Hour) // past BillingLag
	r.mustPass(rec)
	if n := r.count(`SELECT COUNT(*) FROM batches WHERE reconciled_at IS NOT NULL AND id IN (?, ?, ?)`, ids[0], ids[1], ids[2]); n != 3 {
		t.Errorf("reconciled %d of 3 finished batches", n)
	}
}
