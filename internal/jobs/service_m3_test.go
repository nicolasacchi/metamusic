package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestService_UnwiredAnswersUnsupported(t *testing.T) {
	t.Parallel()
	svc := New(Options{})
	if svc.Hub() == nil {
		t.Fatal("a private hub is built")
	}
	ctx := context.Background()
	if _, err := svc.ListJobs(ctx, humanP, JobQuery{}); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("ListJobs: %v", err)
	}
	if _, err := svc.Job(ctx, humanP, "x"); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Job: %v", err)
	}
	if _, err := svc.ListBatches(ctx, humanP, BatchQuery{}); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("ListBatches: %v", err)
	}
	if _, err := svc.Batch(ctx, humanP, "x"); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Batch: %v", err)
	}
	if _, err := svc.CancelJob(ctx, humanP, "x", ""); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("CancelJob: %v", err)
	}
	if _, err := svc.CancelBatch(ctx, humanP, "x", ""); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("CancelBatch: %v", err)
	}
	if _, err := svc.Wait(ctx, humanP, "x", "", 0); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Wait: %v", err)
	}
	adm := NewProviderAdmin(ProviderOptions{})
	yes := true
	if _, err := adm.Status(ctx, humanP); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Status: %v", err)
	}
	if _, err := adm.Update(ctx, humanP, UpdateIn{Armed: &yes, Reason: "x"}); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Update: %v", err)
	}
	adm.UseSweep(func() SweepSnapshot { return SweepSnapshot{} })
	adm.UseCapabilities(func() provider.Capabilities { return provider.Capabilities{} })
}

func TestCursor_RoundTripAndRefusals(t *testing.T) {
	t.Parallel()
	at := t0.Add(90 * time.Second)
	c := CursorFor(at, "job-1")
	gotAt, gotID, err := decodeCursor(c)
	if err != nil || gotAt != store.FormatTime(at) || gotID != "job-1" {
		t.Fatalf("%q %q %v", gotAt, gotID, err)
	}
	for _, bad := range []string{"@@@", "bm9wZQ", "MjAyNi0xMC0xNHxpZA", "MjAyNi0xMC0xNFQxMjowMDowMC4wMDBafA"} {
		if _, _, err := decodeCursor(bad); !errors.Is(err, ErrBadCursor) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if clampLimit(500, 50, 100) != 100 || clampLimit(0, 50, 100) != 50 || clampLimit(7, 50, 100) != 7 {
		t.Error("clampLimit")
	}
}

// batchWithEveryState has a completed (truncated), failed, cancelled and expired job and a running one.
func TestService_BatchCountsEveryState(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	svc := r.service(t.Context())
	s := r.seedBatch(batchSpec{jobs: 5, workersMax: 1})
	// job 0 completes truncated
	spec := r.submitOne(s.Jobs[0])
	out := r.deliver(spec, deliverOpts{truncated: [2]bool{true, false}})
	r.setStatus(r.refOf(s.Jobs[0], 1), completed(out, 60000))
	for range 4 {
		r.advance(11 * time.Second)
		r.step()
	}
	r.wantState(s.Jobs[0], StateCompleted)
	// job 1 fails, job 2 is cancelled, job 3 expires (queued -> failed and queued -> cancelled are legal; expired needs an awaiting job)
	r.exec(`UPDATE jobs SET state = 'failed', error_code = 'DEADLINE', finished_at = ?, updated_at = ? WHERE id = ?`, store.FormatTime(r.clk.Now()), store.FormatTime(r.clk.Now()), s.Jobs[3])
	r.cancelIntent(s.Jobs[2])
	r.step()
	r.wantState(s.Jobs[2], StateCancelled)
	b, err := svc.Batch(t.Context(), humanP, s.Batch)
	if err != nil {
		t.Fatal(err)
	}
	c := b.Counts
	if c.Completed != 1 || c.Truncated != 1 || c.Failed != 1 || c.Cancelled != 1 || c.Queued != 1 || c.Running != 1 {
		t.Errorf("counts %+v", c)
	}
	w := r.seedBatch(batchSpec{state: "awaiting_confirmation", rationale: "because"})
	r.exec(`UPDATE jobs SET state = 'expired', error_code = 'APPROVAL_EXPIRED', finished_at = ?, updated_at = ? WHERE id = ?`, store.FormatTime(r.clk.Now()), store.FormatTime(r.clk.Now()), w.Jobs[0])
	wb, _ := svc.Batch(t.Context(), humanP, w.Batch)
	if wb.Counts.Expired != 1 || wb.Rationale == nil || *wb.Rationale != "because" {
		t.Errorf("expired batch %+v %v", wb.Counts, wb.Rationale)
	}
	// list filters: since / until / cursor over batches
	r.advance(time.Minute)
	page, err := svc.ListBatches(t.Context(), humanP, BatchQuery{Since: t0.Add(time.Second), Limit: 1})
	if err != nil || len(page.Items) > 1 {
		t.Fatal(err)
	}
	all, _ := svc.ListBatches(t.Context(), humanP, BatchQuery{Limit: 1})
	if all.NextCursor == "" {
		t.Fatal("a second page exists")
	}
	next, err := svc.ListBatches(t.Context(), humanP, BatchQuery{Cursor: all.NextCursor})
	if err != nil || len(next.Items) != 1 {
		t.Errorf("second page %v %v", next.Items, err)
	}
	if _, err := svc.ListBatches(t.Context(), humanP, BatchQuery{Cursor: "!"}); !errors.Is(err, ErrBadCursor) {
		t.Errorf("bad cursor %v", err)
	}
}

// TestCancel_AuditedInTheSameTransaction: with the gate's pending entry in the context the audit row is
// written by the operation's own transaction (D-M3-21).
func TestCancel_AuditedInTheSameTransaction(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	svc := r.service(t.Context())
	s := r.seedBatch(batchSpec{jobs: 2})
	ctx := audit.WithPending(t.Context(), audit.Entry{At: t0, Principal: humanP, Transport: reqinfo.REST, Client: "test", Op: "cancel_job", Method: "POST",
		ArgsSHA256: "abc", Idempotency: audit.IdemNone})
	if _, err := svc.CancelJob(ctx, humanP, s.Jobs[0], "r"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CancelBatch(audit.WithPending(t.Context(), audit.Entry{At: t0, Principal: humanP, Transport: reqinfo.REST, Op: "cancel_batch", Method: "POST", ArgsSHA256: "abc"}), humanP, s.Batch, ""); err != nil {
		t.Fatal(err)
	}
	if n := r.count(`SELECT COUNT(*) FROM audit_log WHERE op IN ('cancel_job','cancel_batch') AND status = 200 AND transport = 'rest'`); n != 2 {
		t.Errorf("audit rows %d", n)
	}
	// a refused cancel writes no audit row of its own (the gate audits the refusal)
	_, err := svc.CancelJob(audit.WithPending(t.Context(), audit.Entry{At: t0, Principal: agent2P, Op: "cancel_job", Method: "POST", ArgsSHA256: "abc"}), agent2P, s.Jobs[1], "")
	if err == nil && r.count(`SELECT COUNT(*) FROM audit_log WHERE principal_id = ?`, agent2P.Key()) != 0 {
		t.Error("unexpected audit row")
	}
}

func TestProviderStatus_SweepSnapshotFields(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.recon()
	idle := 20
	ep := providertestEndpoint(&idle)
	skew := 1500 * time.Millisecond
	r.mu.Lock()
	r.sweepFn = func() (provider.SweepReport, error) {
		return provider.SweepReport{Workers: []provider.Worker{worker("w1", "RUNNING", upt(10), t0)}, Endpoint: &ep, Skew: &skew,
			Drift: []provider.Drift{{Field: "flashboot", Want: "FLASHBOOT", Got: "NONE"}}, ObservedAt: r.clk.Now()}, nil
	}
	r.mu.Unlock()
	r.mustPass(rec)
	a := r.admin(provider.ModeFake, true)
	a.UseSweep(rec.Snapshot)
	a.UseCapabilities(r.be.Capabilities)
	st, err := a.Status(t.Context(), humanP)
	if err != nil {
		t.Fatal(err)
	}
	if st.SkewMS == nil || *st.SkewMS != 1500 || st.Endpoint == nil || len(st.Endpoint.Drift) != 1 || st.Endpoint.Drift[0].Field != "flashboot" || st.Workers == nil || st.Workers.Running != 1 {
		t.Errorf("status %+v %+v", st.Endpoint, st.Workers)
	}
	// without a swept endpoint, the cached quote's endpoint carries the drift of the snapshot
	b := r.admin(provider.ModeFake, true, func(o *ProviderOptions) {
		o.Sweep = func() SweepSnapshot {
			return SweepSnapshot{ObservedAt: t0, Drift: []provider.Drift{{Field: "type", Want: "a", Got: "b"}}, WorkerCounts: map[string]int{}}
		}
	})
	st, err = b.Status(t.Context(), humanP)
	if err != nil || st.Endpoint == nil || len(st.Endpoint.Drift) != 1 || st.Endpoint.Drift[0].Field != "type" {
		t.Errorf("quote endpoint with drift: %+v %v", st.Endpoint, err)
	}
}

func TestService_JobColdStart(t *testing.T) {
	t.Parallel()
	r := newRig(t, withOpts(func(o *RunnerOptions) { o.MeterKey = meterKey }))
	svc := r.service(t.Context())
	job := r.seedBatch(batchSpec{}).Jobs[0]
	r.submitOne(job)
	r.running(job, "w1")
	started := r.clk.Now().Add(-95 * time.Second)
	r.exec(`INSERT INTO worker_meter(worker_key, started_at, first_seen_at, last_seen_at, uptime_s, status_last) VALUES(?,?,?,?,95,'RUNNING')`,
		workerKey(meterKey, "w1"), store.FormatTime(started), store.FormatTime(started), store.FormatTime(r.clk.Now()))
	r.exec(`UPDATE job_attempts SET announced_at = ? WHERE job_id = ?`, store.FormatTime(r.clk.Now()), job)
	j, err := svc.Job(t.Context(), humanP, job)
	if err != nil || j.Timings.ColdStartS == nil || *j.Timings.ColdStartS != 95 {
		t.Fatalf("cold start %v %v", j.Timings.ColdStartS, err)
	}
	if j.Timings.ExecutionMS != nil {
		t.Error("a live attempt has no execution time")
	}
}
