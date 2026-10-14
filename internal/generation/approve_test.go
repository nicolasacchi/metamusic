package generation_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/generation"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// pending creates an agent batch awaiting the operator (6 takes exceed the auto-approve limit).
func (r *rig) pending() generation.Created {
	r.t.Helper()
	c := r.mustCreate(agent, r.est(agent, newSeedItem(lt.TrackCitta, 6)), spend.ViaAPI)
	if c.State != "awaiting_confirmation" {
		r.t.Fatalf("batch is %s", c.State)
	}
	return c
}

func TestApproveBatch_Ops(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	c := r.pending()
	before := r.wakes.Load()
	ctx := audit.WithPending(context.Background(), audit.Entry{At: r.clk.Now(), Principal: human, Transport: reqinfo.REST, Op: "approve_batch", Method: "POST", ArgsSHA256: "x"})
	got, err := r.svc.Approve(ctx, human, c.BatchID, "looks fine")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "active" || got.ApprovalState != "approved" || got.Approval.Via != "web" || got.Approval.DecidedBy != human.Key() || got.Approval.Reason != "looks fine" || got.Approval.DecidedAt == nil {
		t.Fatalf("approved = %+v / %+v", got, got.Approval)
	}
	// the jobs are the runner's: they stay as they are until its next step; the runner is woken
	if got.Counts.Awaiting != 6 || r.wakes.Load() != before+1 {
		t.Errorf("counts %+v wakes %d (was %d)", got.Counts, r.wakes.Load(), before)
	}
	// the audit row was written in the same transaction, with the batch ceiling as its cost
	var cost int64
	var status int
	if err := r.st.R.QueryRow(`SELECT cost_micro_usd, status FROM audit_log WHERE op = 'approve_batch'`).Scan(&cost, &status); err != nil || cost != int64(c.Ceiling) || status != 200 {
		t.Errorf("audit = %d %d %v", cost, status, err)
	}
	// approving again changes nothing and does not wake the runner
	again, err := r.svc.Approve(context.Background(), human, c.BatchID, "")
	if err != nil || again.State != "active" || again.Approval.Reason != "looks fine" || r.wakes.Load() != before+1 {
		t.Errorf("second approve: %+v %v wakes %d", again.Approval, err, r.wakes.Load())
	}
	// a rejected batch cannot be approved
	c2 := r.pending()
	if _, err := r.svc.Reject(context.Background(), human, c2.BatchID, "no"); err != nil {
		t.Fatal(err)
	}
	_, err = r.svc.Approve(context.Background(), human, c2.BatchID, "")
	pe := wantProblem(t, err, 409, "INVALID_TRANSITION")
	if pe.Details["from"] != "rejected" || pe.Details["to"] != "approved" {
		t.Errorf("details = %v", pe.Details)
	}
	_, err = r.svc.Approve(context.Background(), human, "no-such-batch", "")
	wantProblem(t, err, 404, "NOT_FOUND")
	// a lapsed (24 h) pending approval cannot be approved either
	c3 := r.pending()
	r.clk.Advance(25 * time.Hour)
	_, err = r.svc.Approve(context.Background(), human, c3.BatchID, "")
	wantProblem(t, err, 409, "INVALID_TRANSITION")
}

func TestRejectBatch_ReleasesReservation(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	c := r.pending()
	usageBefore, err := r.sp.Usage(context.Background(), r.st.R, agent.Key(), r.clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if usageBefore.PrincipalOutstanding <= 0 {
		t.Fatal("the pending batch reserved nothing")
	}
	got, err := r.svc.Reject(context.Background(), human, c.BatchID, "too expensive")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "rejected" || got.ApprovalState != "rejected" || got.FinishedAt == nil || !got.Reconciled || got.Outstanding != 0 || got.Approval.Reason != "too expensive" {
		t.Errorf("rejected = %+v", got)
	}
	// one release 'rejected' equal to the whole outstanding, reconciled_at set in the same transaction
	if n := r.count(`SELECT COUNT(*) FROM ledger WHERE batch_id = ? AND kind = 'release' AND reason = 'rejected'`, c.BatchID); n != 1 {
		t.Errorf("%d rejected releases", n)
	}
	if v := r.val(`SELECT micro_usd FROM ledger WHERE batch_id = ? AND kind = 'release'`, c.BatchID); v != int64(c.Reserved) {
		t.Errorf("released %d, reserved %d", v, c.Reserved)
	}
	if r.str(`SELECT reconciled_at FROM batches WHERE id = ?`, c.BatchID) == "" || r.str(`SELECT finished_at FROM batches WHERE id = ?`, c.BatchID) == "" {
		t.Error("reconciled_at/finished_at missing")
	}
	// the usage is back; jobs are cancelled by the runner later
	u, _ := r.sp.Usage(context.Background(), r.st.R, agent.Key(), r.clk.Now())
	if u.PrincipalOutstanding != usageBefore.PrincipalOutstanding-c.Reserved {
		t.Errorf("outstanding %d, want %d", u.PrincipalOutstanding, usageBefore.PrincipalOutstanding-c.Reserved)
	}
	// rejecting twice is idempotent; an approved batch cannot be rejected
	if _, err := r.svc.Reject(context.Background(), human, c.BatchID, "again"); err != nil {
		t.Errorf("second reject: %v", err)
	}
	ok := r.mustCreate(human, r.est(human, newSeedItem(lt.TrackLuna, 1)), spend.ViaHumanCreate)
	_, err = r.svc.Reject(context.Background(), human, ok.BatchID, "late")
	wantProblem(t, err, 409, "INVALID_TRANSITION")
}

func TestRequestApproval_NotOwner403(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	c := r.pending()
	_, err := r.svc.RequestApproval(context.Background(), other, c.BatchID)
	wantProblem(t, err, 403, "NOT_BATCH_OWNER")
	_, err = r.svc.RequestApproval(context.Background(), human, c.BatchID)
	wantProblem(t, err, 403, "NOT_BATCH_OWNER")
	_, err = r.svc.RequestApproval(context.Background(), agent, "no-such-batch")
	wantProblem(t, err, 404, "NOT_FOUND")
	if len(r.notes.all()) != 0 {
		t.Error("a refused request pushed a notification")
	}
}

func TestRequestApproval_PushesOnce(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	c := r.pending()
	v, err := r.svc.RequestApproval(context.Background(), agent, c.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Notified || v.State != "pending" || v.LastRequestedAt == nil {
		t.Errorf("view = %+v", v)
	}
	msgs := r.notes.all()
	if len(msgs) != 1 {
		t.Fatalf("%d pushes", len(msgs))
	}
	m := msgs[0]
	want := "Agent fixture-agent asks to spend up to " + c.Ceiling.DisplayCeil() + " for 6 takes"
	if m.Title != "Approval needed" || m.Priority != 4 || m.Body != want {
		t.Errorf("message = %+v, want body %q", m, want)
	}
	if len(m.Actions) != 1 || m.Actions[0].Label != "Review" || !strings.HasSuffix(m.Actions[0].URL, "/human/confirm/"+c.BatchID) || !strings.HasPrefix(m.Actions[0].URL, "http://mm.example.com/") {
		t.Errorf("actions = %+v", m.Actions)
	}
	for _, s := range []string{"mmc_", "?", "token"} {
		if strings.Contains(m.Body+m.Actions[0].URL, s) {
			t.Errorf("the notification carries %q", s)
		}
	}
	// a second request within 10 minutes pushes nothing and says when the next one is allowed
	v2, err := r.svc.RequestApproval(context.Background(), agent, c.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if v2.Notified || v2.NextRequestAllowedAt == nil || !v2.NextRequestAllowedAt.After(r.clk.Now()) || len(r.notes.all()) != 1 {
		t.Errorf("second view = %+v pushes %d", v2, len(r.notes.all()))
	}
	r.clk.Advance(11 * time.Minute)
	v3, err := r.svc.RequestApproval(context.Background(), agent, c.BatchID)
	if err != nil || !v3.Notified || len(r.notes.all()) != 2 {
		t.Errorf("third: %+v %v pushes %d", v3, err, len(r.notes.all()))
	}
	// a decided batch pushes nothing
	if _, err := r.svc.Reject(context.Background(), human, c.BatchID, "no"); err != nil {
		t.Fatal(err)
	}
	v4, err := r.svc.RequestApproval(context.Background(), agent, c.BatchID)
	if err != nil || v4.Notified || v4.State != "rejected" || len(r.notes.all()) != 2 {
		t.Errorf("decided: %+v %v", v4, err)
	}
}

func TestApproval_View(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	v, err := r.svc.Approval(context.Background(), agent, lt.BatchAwaiting)
	if err != nil || v.State != "pending" || v.BatchID != lt.BatchAwaiting || len(v.Reasons) != 1 || v.Reasons[0] != "takes_over_limit" || v.DecidedAt != nil || v.Via != "" {
		t.Errorf("pending = %+v %v", v, err)
	}
	v, err = r.svc.Approval(context.Background(), agent, lt.BatchDone)
	if err != nil || v.State != "approved" || v.Via != "human_create" || v.DecidedAt == nil {
		t.Errorf("approved = %+v %v", v, err)
	}
	_, err = r.svc.Approval(context.Background(), agent, "nope")
	wantProblem(t, err, 404, "NOT_FOUND")
}
