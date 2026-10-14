package jobs

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var (
	humanP  = auth.Principal{Kind: auth.KindHuman, ID: "h1", Name: "h1"}
	deviceP = auth.Principal{Kind: auth.KindDevice, ID: "d1", Name: "phone"}
	agentP  = auth.Principal{Kind: auth.KindAgent, ID: "tok1", Name: "claude"}
	agent2P = auth.Principal{Kind: auth.KindAgent, ID: "tok2", Name: "other"}
)

func TestService_ListJobsAndBatches(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	svc := r.service(t.Context())
	a := r.seedBatch(batchSpec{jobs: 3, principal: "agent:tok1"})
	r.advance(time.Minute)
	b := r.seedBatch(batchSpec{jobs: 2, state: "awaiting_confirmation"})
	r.advance(time.Minute)
	c := r.seedBatch(batchSpec{jobs: 1, source: ""})
	ctx := t.Context()

	page, err := svc.ListJobs(ctx, humanP, JobQuery{})
	if err != nil || len(page.Items) != 6 || page.NextCursor != "" {
		t.Fatalf("all jobs: %d %q %v", len(page.Items), page.NextCursor, err)
	}
	if page.Items[0].BatchID != c.Batch {
		t.Error("newest first")
	}
	// pagination is stable and exhaustive
	var seen []string
	cursor := ""
	for range 10 {
		p, err := svc.ListJobs(ctx, humanP, JobQuery{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, j := range p.Items {
			seen = append(seen, j.ID)
		}
		if cursor = p.NextCursor; cursor == "" {
			break
		}
	}
	if len(seen) != 6 {
		t.Errorf("paged %d jobs", len(seen))
	}
	if p, _ := svc.ListJobs(ctx, humanP, JobQuery{States: []string{StateAwaiting}}); len(p.Items) != 2 {
		t.Errorf("awaiting: %d", len(p.Items))
	}
	if p, _ := svc.ListJobs(ctx, humanP, JobQuery{BatchID: a.Batch}); len(p.Items) != 3 {
		t.Errorf("batch filter: %d", len(p.Items))
	}
	if p, _ := svc.ListJobs(ctx, humanP, JobQuery{Since: t0.Add(90 * time.Second)}); len(p.Items) != 1 {
		t.Errorf("since: %d", len(p.Items))
	}
	if _, err := svc.ListJobs(ctx, humanP, JobQuery{Cursor: "garbage"}); !errors.Is(err, ErrBadCursor) {
		t.Errorf("bad cursor: %v", err)
	}
	// track filter: as parent or as child
	r.exec(`INSERT INTO tracks(id, slug, title, recipe_id, root_track_id, generation, status, status_set_by, origin, duration_s, sample_rate, bits_per_sample, channels, total_samples,
		master_path, master_sha256, master_bytes, source_sha256, frames_sha256, audio_md5, import_rule, recorded_at, created_at)
		VALUES('trk-parent', 'p', 'P', ?, 'trk-parent', 0, 'draft', 'import', 'import', 4, 48000, 24, 2, 192000, 'audio/masters/p.flac', 'm', 1, 's', 'f', 'a', 'folder', ?, ?)`, testRecipe, store.FormatTime(t0), store.FormatTime(t0))
	d := r.seedBatch(batchSpec{source: "trk-parent"})
	if p, _ := svc.ListJobs(ctx, humanP, JobQuery{TrackID: "trk-parent"}); len(p.Items) != 1 || p.Items[0].ID != d.Jobs[0] || p.Items[0].ParentTrackID == nil {
		t.Errorf("track filter: %+v", p.Items)
	}

	bp, err := svc.ListBatches(ctx, humanP, BatchQuery{})
	if err != nil || len(bp.Items) != 4 || bp.Items[0].ID != d.Batch {
		t.Fatalf("batches: %d %v", len(bp.Items), err)
	}
	if bp, _ := svc.ListBatches(ctx, humanP, BatchQuery{PrincipalKinds: []string{"agent"}}); len(bp.Items) != 1 || bp.Items[0].ID != a.Batch {
		t.Errorf("principal kind filter: %d", len(bp.Items))
	}
	if bp, _ := svc.ListBatches(ctx, humanP, BatchQuery{States: []string{"awaiting_confirmation"}}); len(bp.Items) != 1 || bp.Items[0].ID != b.Batch {
		t.Errorf("state filter")
	}
	if bp, _ := svc.ListBatches(ctx, humanP, BatchQuery{Limit: 2}); len(bp.Items) != 2 || bp.NextCursor == "" {
		t.Errorf("batch paging: %d %q", len(bp.Items), bp.NextCursor)
	}
	if bp, _ := svc.ListBatches(ctx, humanP, BatchQuery{Until: t0.Add(30 * time.Second)}); len(bp.Items) != 1 {
		t.Errorf("until: %d", len(bp.Items))
	}
}

func TestService_BatchView(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	svc := r.service(t.Context())
	s := r.seedBatch(batchSpec{jobs: 3, principal: "agent:tok1"})
	r.cancelIntent(s.Jobs[2])
	r.step()
	b, err := svc.Batch(t.Context(), humanP, s.Batch)
	if err != nil {
		t.Fatal(err)
	}
	if b.Takes != 3 || len(b.Jobs) != 3 || b.Jobs[0].TakeIndex != 0 || b.CreatedByKind != "agent" || b.CreatedByKey != "agent:tok1" {
		t.Fatalf("batch %+v", b)
	}
	if b.Counts.Cancelled != 1 || b.Counts.Running != 2 {
		t.Errorf("counts %+v", b.Counts)
	}
	if b.Cost.Reserved == 0 || b.Cost.Outstanding == 0 || b.Cost.Reconciled || b.Cost.Ceiling == 0 {
		t.Errorf("cost %+v", b.Cost)
	}
	if b.Approval.State != "auto" || b.Approval.BatchID != s.Batch || b.Approval.DecidedByKey == nil {
		t.Errorf("approval %+v", b.Approval)
	}
	if _, err := svc.Batch(t.Context(), humanP, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	// pending approval: the next request time follows the last request
	p := r.seedBatch(batchSpec{state: "awaiting_confirmation"})
	r.exec(`UPDATE approvals SET last_requested_at = ?, requested_count = 1 WHERE batch_id = ?`, store.FormatTime(r.clk.Now()), p.Batch)
	pb, _ := svc.Batch(t.Context(), humanP, p.Batch)
	if pb.Approval.State != "pending" || pb.Approval.NextRequestAllowedAt == nil || !pb.Approval.NextRequestAllowedAt.Equal(r.clk.Now().Add(10*time.Minute)) {
		t.Errorf("pending approval %+v", pb.Approval)
	}
}

func TestService_JobView(t *testing.T) {
	t.Parallel()
	r := newRig(t, withOpts(func(o *RunnerOptions) { o.MeterKey = meterKey }))
	svc := r.service(t.Context())
	job := r.seedBatch(batchSpec{}).Jobs[0]
	spec := r.submitOne(job)
	r.running(job, "w1")
	out := r.deliver(spec, deliverOpts{})
	st := completed(out, 60000)
	st.WorkerID = "w1"
	r.setStatus(r.refOf(job, 1), st)
	for range 4 {
		r.advance(11 * time.Second)
		r.step()
	}
	j, err := svc.Job(t.Context(), agentP, job)
	if err != nil {
		t.Fatal(err)
	}
	if j.State != StateCompleted || j.Outcome == nil || j.TrackID == nil || j.Engine != "yue2" || j.Backend != "fake" || j.Attempts != 1 || j.TimeoutS != 186 {
		t.Fatalf("job %+v", j)
	}
	if j.RequestID != "syn-a" || j.Seed != 1 || j.Operator != "reroll_seed" {
		t.Errorf("recipe facts %+v", j)
	}
	if j.Timings.ExecutionMS == nil || *j.Timings.ExecutionMS != 60000 {
		t.Errorf("timings %+v", j.Timings)
	}
	if len(j.Events) == 0 || j.Events[0].Event != "claim" || j.Events[len(j.Events)-1].Event != "ingested" {
		t.Errorf("events %+v", j.Events)
	}
	if len(j.Artifacts) == 0 {
		t.Error("no artifacts listed")
	}
	if j.Cost.Basis != "provisional" || j.Cost.Display == nil || j.Cost.Expected == 0 {
		t.Errorf("cost %+v", j.Cost)
	}
	raw, _ := json.Marshal(j)
	for _, bad := range []string{"ref-", "https://", "exp=", "sig=", "runner-1", spec.StartURL} {
		if bad != "" && strings.Contains(string(raw), bad) {
			t.Errorf("the job view leaks %q", bad)
		}
	}
	if _, err := svc.Job(t.Context(), agentP, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	// a failed job carries a fixed message, never provider text
	f := r.seedBatch(batchSpec{}).Jobs[0]
	r.exec(`UPDATE jobs SET state = 'queued' WHERE id = ?`, f)
	r.cancelIntent(f)
	r.step()
	cj, _ := svc.Job(t.Context(), agentP, f)
	if cj.State != StateCancelled || cj.ErrorMessage == nil || *cj.ErrorMessage != "The job was cancelled." {
		t.Errorf("cancelled job %+v", cj)
	}
}

func TestErrorMessage_Fixed(t *testing.T) {
	t.Parallel()
	for _, code := range []string{CodeCancelledByUser, CodeRejected, CodeApprovalExpired, CodeNoCapacity, CodeBudgetExceeded, CodePriceChanged, CodeProviderAuth, CodeEdgeBlocked,
		CodeSubmitRejected, CodeWorkerError, CodeWorkerLost, CodeDeadline, CodeArtifactMissing, CodeArtifactMismatch, CodeWorkerContract, CodeIngestFailed, CodeDuplicateAudio,
		CodeNotSubmitted, CodeProviderTripped, CodeProviderCanceled} {
		if m := ErrorMessage(code); m == "" || strings.ContainsAny(m, "?{}") || !strings.HasSuffix(m, ".") {
			t.Errorf("%s: %q", code, m)
		}
	}
	if ErrorMessage("MADE_UP") != "" {
		t.Error("unknown codes have no message")
	}
}

func TestCancel_AgentNotOwner403(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	svc := r.service(t.Context())
	s := r.seedBatch(batchSpec{principal: "agent:tok1", jobs: 2})
	if _, err := svc.CancelJob(t.Context(), agent2P, s.Jobs[0], ""); !errors.Is(err, ErrNotBatchOwner) {
		t.Errorf("job: %v", err)
	}
	if _, err := svc.CancelBatch(t.Context(), agent2P, s.Batch, ""); !errors.Is(err, ErrNotBatchOwner) {
		t.Errorf("batch: %v", err)
	}
	if r.count(`SELECT COUNT(*) FROM jobs WHERE cancel_requested_at IS NOT NULL`) != 0 {
		t.Error("a refused cancel wrote an intent")
	}
	if _, err := svc.CancelJob(t.Context(), agentP, s.Jobs[0], "mine"); err != nil {
		t.Errorf("the owner: %v", err)
	}
	if _, err := svc.CancelJob(t.Context(), humanP, "nope", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if _, err := svc.CancelBatch(t.Context(), humanP, "nope", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing batch: %v", err)
	}
}

func TestCancel_DeviceAllowed(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	svc := r.service(t.Context())
	woken := 0
	svc.UseWake(func() { woken++ })
	s := r.seedBatch(batchSpec{principal: "agent:tok1", jobs: 2})
	j, err := svc.CancelJob(t.Context(), deviceP, s.Jobs[0], "")
	if err != nil || j.CancelReqAt == nil {
		t.Fatalf("%+v %v", j, err)
	}
	if got := r.str(`SELECT cancel_requested_by FROM jobs WHERE id = ?`, s.Jobs[0]); got != deviceP.Key() {
		t.Errorf("cancelled by %q", got)
	}
	b, err := svc.CancelBatch(t.Context(), humanP, s.Batch, "")
	if err != nil || woken != 2 {
		t.Fatalf("%v woken %d", err, woken)
	}
	for _, jb := range b.Jobs {
		if jb.CancelReqAt == nil {
			t.Errorf("job %s has no intent", jb.ID)
		}
	}
	r.step()
	for _, id := range s.Jobs {
		r.wantState(id, StateCancelled)
	}
}

func TestCancel_TerminalIdempotent(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	svc := r.service(t.Context())
	s := r.seedBatch(batchSpec{})
	r.cancelIntent(s.Jobs[0])
	r.step()
	r.wantState(s.Jobs[0], StateCancelled)
	before := r.str(`SELECT updated_at FROM jobs WHERE id = ?`, s.Jobs[0])
	r.advance(time.Minute)
	j, err := svc.CancelJob(t.Context(), humanP, s.Jobs[0], "again")
	if err != nil || j.State != StateCancelled {
		t.Fatalf("%+v %v", j, err)
	}
	if r.str(`SELECT updated_at FROM jobs WHERE id = ?`, s.Jobs[0]) != before {
		t.Error("a terminal job changed")
	}
	b, err := svc.CancelBatch(t.Context(), humanP, s.Batch, "")
	if err != nil || b.Counts.Cancelled != 1 {
		t.Errorf("%+v %v", b.Counts, err)
	}
}
