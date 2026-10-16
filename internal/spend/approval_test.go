package spend

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var (
	humanP = auth.Principal{Kind: auth.KindHuman, ID: "h1", Name: "h1"}
	agentP = auth.Principal{Kind: auth.KindAgent, ID: "a1", Name: "a1"}
)

func TestNeedsApproval_Table(t *testing.T) {
	t.Parallel()
	c := DefaultCaps
	ten, zero, tenMicro := 10, 0, money.MicroUSD(10)
	for _, tc := range []struct {
		name    string
		p       auth.Principal
		via     string
		takes   int
		ceiling money.MicroUSD
		tok     TokenLimits
		needed  bool
		reasons []string
	}{
		{"human create", humanP, ViaHumanCreate, 50, 4_000_000, TokenLimits{}, false, nil},
		{"human api path", humanP, ViaAPI, 1, 1, TokenLimits{}, true, []string{"human_api_path"}},
		{"agent 7 takes", agentP, ViaAPI, 7, 691_731, TokenLimits{}, true, []string{"takes_over_limit", "cost_over_limit"}},
		{"agent 1 take", agentP, ViaAPI, 1, 153_718, TokenLimits{}, false, nil},
		{"agent 5 takes at the limit", agentP, ViaAPI, 5, 500_000, TokenLimits{}, false, nil},
		{"token zero takes", agentP, ViaAPI, 1, 153_718, TokenLimits{AutoApproveTakes: &zero}, true, []string{"takes_over_limit"}},
		{"token ten micro", agentP, ViaAPI, 1, 153_718, TokenLimits{AutoApproveMicro: &tenMicro}, true, []string{"cost_over_limit"}},
		{"token ten takes", agentP, ViaAPI, 7, 400_000, TokenLimits{AutoApproveTakes: &ten}, false, nil},
	} {
		got := NeedsApproval(tc.p, tc.via, tc.takes, tc.ceiling, tc.tok, c)
		if got.Needed != tc.needed || !slices.Equal(got.Reasons, append([]string{}, tc.reasons...)) {
			t.Errorf("%s: %+v, want needed=%v reasons=%v", tc.name, got, tc.needed, tc.reasons)
		}
		if got.Reasons == nil {
			t.Errorf("%s: reasons must be a non-nil slice for JSON", tc.name)
		}
	}
}

func TestInsertApproval_States(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name, state, via, decidedBy string
		p                           auth.Principal
		via0                        string
		need                        ApprovalNeed
	}{
		{"hc", "approved", "human_create", "human:h1", humanP, ViaHumanCreate, ApprovalNeed{Reasons: []string{}}},
		{"auto", "auto", "auto", "agent:a1", agentP, ViaAPI, ApprovalNeed{}},
		{"pend", "pending", "", "", agentP, ViaAPI, ApprovalNeed{Needed: true, Reasons: []string{"takes_over_limit"}}},
	} {
		sd := r.seedBatch(seedSpec{name: tc.name, noApproval: true, principal: tc.p.Key()})
		var state string
		r.must(func(tx *sql.Tx) (err error) {
			state, err = r.s.InsertApproval(ctx, tx, sd.Batch, tc.p, tc.via0, tc.need)
			return
		})
		if state != tc.state || r.str(`SELECT state FROM approvals WHERE batch_id=?`, sd.Batch) != tc.state {
			t.Errorf("%s: state %q", tc.name, state)
		}
		if got := r.str(`SELECT COALESCE(via,'') FROM approvals WHERE batch_id=?`, sd.Batch); got != tc.via {
			t.Errorf("%s: via %q", tc.name, got)
		}
		if got := r.str(`SELECT COALESCE(decided_by,'') FROM approvals WHERE batch_id=?`, sd.Batch); got != tc.decidedBy {
			t.Errorf("%s: decided_by %q", tc.name, got)
		}
		if (tc.state == "pending") != (r.str(`SELECT decided_at FROM approvals WHERE batch_id=?`, sd.Batch) == "") {
			t.Errorf("%s: decided_at", tc.name)
		}
		if got := r.str(`SELECT expires_at FROM approvals WHERE batch_id=?`, sd.Batch); got != store.FormatTime(t0.Add(ApprovalTTL)) {
			t.Errorf("%s: expires_at %s", tc.name, got)
		}
	}
	if r.str(`SELECT reasons FROM approvals WHERE batch_id='bat-pend'`) != `["takes_over_limit"]` || r.str(`SELECT reasons FROM approvals WHERE batch_id='bat-auto'`) != `[]` {
		t.Error("reasons json")
	}
	// a duplicate approval row for a batch is refused by the schema
	if err := r.tx(func(tx *sql.Tx) error {
		_, err := r.s.InsertApproval(ctx, tx, "bat-pend", agentP, ViaAPI, ApprovalNeed{})
		return err
	}); err == nil {
		t.Error("duplicate approval accepted")
	}
}

func (r *rig) awaiting(name string) seeded {
	r.t.Helper()
	sd := r.seedBatch(seedSpec{name: name, state: "awaiting_confirmation", principal: "agent:a1", jobs: 2})
	r.must(func(tx *sql.Tx) error {
		return r.s.Ledger().Append(ctx0, tx,
			Entry{Kind: KindReserve, MicroUSD: 300, BatchID: &sd.Batch, JobID: &sd.Jobs[0], PrincipalID: "agent:a1", Reason: "take"},
			Entry{Kind: KindReserve, MicroUSD: 300, BatchID: &sd.Batch, JobID: &sd.Jobs[1], PrincipalID: "agent:a1", Reason: "take"})
	})
	return sd
}

// advance moves the clock and renews the runner lease around it.
func (r *rig) advance(d time.Duration) {
	r.t.Helper()
	r.clk.Set(r.clk.Now().Add(d))
	r.exec(`UPDATE runner_lease SET expires_at = ?`, store.FormatTime(r.clk.Now().Add(24*time.Hour)))
	r.lease = store.LeaseFence{Owner: "o1", Now: r.clk.Now()}
}

func TestBatchTransitions_Writers(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	used := map[string]bool{}
	for _, tr := range BatchTransitions {
		used[tr.Writer] = true
	}
	for _, w := range []string{"create", "ApproveBatch", "RejectBatch", "ExpireApproval", "FinishBatch", "ReconcileBatch"} {
		if !used[w] {
			t.Errorf("writer %s missing from BatchTransitions", w)
		}
	}
	state := func(id string) string { return r.str(`SELECT state FROM batches WHERE id=?`, id) }

	// awaiting -> active (approve): the batch stays open, no finished_at
	a := r.awaiting("ap")
	r.must(func(tx *sql.Tx) error { _, err := r.s.ApproveBatch(ctx, tx, a.Batch, humanP, "ok"); return err })
	if state(a.Batch) != "active" || r.str(`SELECT finished_at FROM batches WHERE id=?`, a.Batch) != "" || r.str(`SELECT reconciled_at FROM batches WHERE id=?`, a.Batch) != "" {
		t.Error("approve")
	}
	if r.str(`SELECT via||'/'||decided_by||'/'||reason FROM approvals WHERE batch_id=?`, a.Batch) != "web/human:h1/ok" {
		t.Error("approval columns")
	}
	if r.count(`SELECT COUNT(*) FROM jobs WHERE batch_id=? AND state='awaiting_confirmation'`, a.Batch) != 2 {
		t.Error("approve must leave job states to the runner")
	}

	// awaiting -> rejected (reject): finished + reconciled, outstanding released, usage back
	j := r.awaiting("rj")
	r.must(func(tx *sql.Tx) error { _, err := r.s.RejectBatch(ctx, tx, j.Batch, humanP, ""); return err })
	if state(j.Batch) != "rejected" || r.str(`SELECT finished_at FROM batches WHERE id=?`, j.Batch) == "" || r.str(`SELECT reconciled_at FROM batches WHERE id=?`, j.Batch) == "" {
		t.Error("reject columns")
	}
	if r.count(`SELECT COUNT(*) FROM ledger WHERE batch_id=? AND kind='release' AND reason='rejected' AND micro_usd=600`, j.Batch) != 1 {
		t.Error("one release 'rejected' equal to the outstanding")
	}
	if out, _ := r.s.Usage(ctx, r.st.R, "agent:a1", r.clk.Now()); out.Outstanding != 600-0 { // only the approved batch's 600 remains
		t.Errorf("usage after reject: outstanding %d", out.Outstanding)
	}

	// awaiting -> expired (reconciler): after the 24 h window
	e := r.awaiting("ex")
	r.advance(25 * time.Hour)
	r.must(func(tx *sql.Tx) error { return r.s.ExpireApproval(ctx, tx, e.Batch, r.lease) })
	if state(e.Batch) != "expired" || r.str(`SELECT reconciled_at FROM batches WHERE id=?`, e.Batch) == "" || r.str(`SELECT finished_at FROM batches WHERE id=?`, e.Batch) == "" {
		t.Error("expiry columns")
	}
	if r.str(`SELECT state||'/'||via||'/'||decided_by FROM approvals WHERE batch_id=?`, e.Batch) != "expired/expiry/system:reconciler" {
		t.Error("expiry approval columns")
	}
	if r.count(`SELECT COUNT(*) FROM ledger WHERE batch_id=? AND reason='expired' AND micro_usd=600`, e.Batch) != 1 {
		t.Error("expiry release")
	}

	// active -> finished -> reconciled
	f := r.seedBatch(seedSpec{name: "fin"})
	r.takeReserve(f, "human:h1", 500)
	var fr FinishResult
	r.must(func(tx *sql.Tx) (err error) {
		fr, err = r.s.FinishBatch(ctx, tx, FinishInput{BatchID: f.Batch, OverheadMicro: 0, Lease: r.lease})
		return
	})
	if state(f.Batch) != "finished" || r.str(`SELECT finished_at FROM batches WHERE id=?`, f.Batch) == "" || fr.Held != 500 {
		t.Errorf("finish: %+v", fr)
	}
	var rel money.MicroUSD
	r.must(func(tx *sql.Tx) (err error) {
		rel, err = r.s.ReconcileBatch(ctx, tx, f.Batch, "reconciled", r.lease)
		return
	})
	if rel != 500 || state(f.Batch) != "finished" || r.str(`SELECT reconciled_at FROM batches WHERE id=?`, f.Batch) == "" {
		t.Errorf("reconcile: released %d", rel)
	}
	// illegal moves are refused (by the Go writers and, behind them, the triggers)
	for name, err := range map[string]error{
		"approve a rejected batch": r.tx(func(tx *sql.Tx) error { _, err := r.s.ApproveBatch(ctx, tx, j.Batch, humanP, ""); return err }),
		"reject an approved batch": r.tx(func(tx *sql.Tx) error { _, err := r.s.RejectBatch(ctx, tx, a.Batch, humanP, ""); return err }),
		"finish a finished batch": r.tx(func(tx *sql.Tx) error {
			_, err := r.s.FinishBatch(ctx, tx, FinishInput{BatchID: f.Batch, Lease: r.lease})
			return err
		}),
		"reconcile twice": r.tx(func(tx *sql.Tx) error {
			_, err := r.s.ReconcileBatch(ctx, tx, f.Batch, "reconciled", r.lease)
			return err
		}),
		"finish an active-less": r.tx(func(tx *sql.Tx) error {
			_, err := r.s.FinishBatch(ctx, tx, FinishInput{BatchID: e.Batch, Lease: r.lease})
			return err
		}),
	} {
		if !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := r.s.ReconcileBatch(ctx, nil, "x", "bogus", r.lease); !errors.Is(err, ErrBadEntry) {
		t.Errorf("bogus reconcile reason: %v", err)
	}
}

func TestApprovals_Transitions(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	approve := func(b string) (bool, error) {
		var ch bool
		err := r.tx(func(tx *sql.Tx) (err error) { ch, err = r.s.ApproveBatch(ctx, tx, b, humanP, ""); return })
		return ch, err
	}
	reject := func(b string) (bool, error) {
		var ch bool
		err := r.tx(func(tx *sql.Tx) (err error) { ch, err = r.s.RejectBatch(ctx, tx, b, humanP, "no"); return })
		return ch, err
	}
	a := r.awaiting("t1")
	if ch, err := approve(a.Batch); !ch || err != nil {
		t.Fatalf("approve: %v %v", ch, err)
	}
	if ch, err := approve(a.Batch); ch || err != nil {
		t.Errorf("approve twice must be unchanged: %v %v", ch, err)
	}
	if _, err := reject(a.Batch); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("reject after approve: %v", err)
	}
	// decided approvals are immutable even to raw SQL
	if _, err := r.st.W.Exec(`UPDATE approvals SET state='rejected' WHERE batch_id=?`, a.Batch); err == nil {
		t.Error("decided approval mutated")
	}
	// auto approvals count as approved
	au := r.seedBatch(seedSpec{name: "auto"})
	if ch, err := approve(au.Batch); ch || err != nil {
		t.Errorf("auto approve: %v %v", ch, err)
	}
	rj := r.awaiting("t2")
	if ch, err := reject(rj.Batch); !ch || err != nil {
		t.Fatalf("reject: %v %v", ch, err)
	}
	if ch, err := reject(rj.Batch); ch || err != nil {
		t.Errorf("reject twice: %v %v", ch, err)
	}
	if _, err := approve(rj.Batch); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("approve after reject: %v", err)
	}
	if _, err := approve("ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("approve unknown: %v", err)
	}
	if _, err := reject("ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("reject unknown: %v", err)
	}

	// expiry: approve after the window is INVALID_TRANSITION even before the reconciler ran
	ex := r.awaiting("t3")
	if err := r.tx(func(tx *sql.Tx) error { return r.s.ExpireApproval(ctx, tx, ex.Batch, r.lease) }); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("expire before the window: %v", err)
	}
	if ids, err := r.s.PendingExpired(ctx, r.st.R, 10); err != nil || len(ids) != 0 {
		t.Errorf("pending expired early: %v %v", ids, err)
	}
	r.advance(25 * time.Hour)
	if _, err := approve(ex.Batch); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("approve after expiry: %v", err)
	}
	if ids, err := r.s.PendingExpired(ctx, r.st.R, 10); err != nil || len(ids) != 1 || ids[0] != ex.Batch {
		t.Errorf("pending expired: %v %v", ids, err)
	}
	lost := r.lease
	lost.Owner = "intruder"
	if err := r.tx(func(tx *sql.Tx) error { return r.s.ExpireApproval(ctx, tx, ex.Batch, lost) }); !errors.Is(err, store.ErrLeaseLost) {
		t.Errorf("expire without the lease: %v", err)
	}
	if err := r.tx(func(tx *sql.Tx) error { return r.s.ExpireApproval(ctx, tx, ex.Batch, r.lease) }); err != nil {
		t.Fatal(err)
	}
	if _, err := approve(ex.Batch); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("approve expired: %v", err)
	}
	if err := r.tx(func(tx *sql.Tx) error { return r.s.ExpireApproval(ctx, tx, ex.Batch, r.lease) }); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("expire twice: %v", err)
	}
}

func TestRequestApproval_RateLimited(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	b := r.awaiting("rq")
	req := func(p auth.Principal) (res ApprovalRequest, err error) {
		err = r.tx(func(tx *sql.Tx) (e error) { res, e = r.s.RequestApproval(ctx, tx, b.Batch, p); return })
		return
	}
	first, err := req(agentP)
	if err != nil || !first.Notified || first.State != "pending" || first.CeilingMicro != 1500 || first.Takes != 2 || first.ExpiresAt.IsZero() {
		t.Fatalf("first: %+v %v", first, err)
	}
	r.advance(3 * time.Minute)
	second, err := req(agentP)
	if err != nil || second.Notified || !second.NextRequestAllowedAt.Equal(t0.Add(ApprovalRequestGap)) {
		t.Fatalf("second: %+v %v", second, err)
	}
	if r.val(`SELECT requested_count FROM approvals WHERE batch_id=?`, b.Batch) != 1 {
		t.Error("a suppressed request bumped the count")
	}
	r.advance(7 * time.Minute) // exactly 10 minutes after the first
	third, err := req(agentP)
	if err != nil || !third.Notified || r.val(`SELECT requested_count FROM approvals WHERE batch_id=?`, b.Batch) != 2 {
		t.Fatalf("third: %+v %v", third, err)
	}
	if _, err := req(auth.Principal{Kind: auth.KindAgent, ID: "other"}); !errors.Is(err, ErrNotBatchOwner) {
		t.Errorf("other agent: %v", err)
	}
	if err := r.tx(func(tx *sql.Tx) error { _, err := r.s.RequestApproval(ctx, tx, "ghost", agentP); return err }); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown batch: %v", err)
	}
	r.must(func(tx *sql.Tx) error { _, err := r.s.ApproveBatch(ctx, tx, b.Batch, humanP, ""); return err })
	done, err := req(agentP)
	if err != nil || done.Notified || done.State != "approved" {
		t.Errorf("after approval: %+v %v", done, err)
	}
}

// TestWithdrawBatch: cancelling a batch that awaits confirmation closes approval and batch together,
// releases the whole reservation and makes approve and reject refuse with ErrBatchNotPending.
func TestWithdrawBatch(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	a := r.awaiting("w1")
	withdraw := func(b string, lease *store.LeaseFence) (bool, error) {
		var ch bool
		err := r.tx(func(tx *sql.Tx) (err error) {
			ch, err = r.s.WithdrawBatch(ctx, tx, b, "agent:a1", "changed my mind", lease)
			return
		})
		return ch, err
	}
	if ids, err := r.s.StuckWithdrawn(ctx, r.st.R, 10); err != nil || len(ids) != 0 {
		t.Fatalf("a live awaiting batch is not stuck: %v %v", ids, err)
	}
	if ch, err := withdraw(a.Batch, nil); !ch || err != nil {
		t.Fatalf("withdraw: %v %v", ch, err)
	}
	if got := r.str(`SELECT state || '|' || via || '|' || decided_by || '|' || reason FROM approvals WHERE batch_id = ?`, a.Batch); got != "rejected|cancel|agent:a1|changed my mind" {
		t.Errorf("approval %q", got)
	}
	if got := r.str(`SELECT state FROM batches WHERE id = ?`, a.Batch); got != "rejected" {
		t.Errorf("batch %q", got)
	}
	if r.str(`SELECT finished_at || reconciled_at FROM batches WHERE id = ?`, a.Batch) == "" {
		t.Error("finished_at and reconciled_at must be set")
	}
	if got := r.val(`SELECT COALESCE(SUM(micro_usd),0) FROM ledger WHERE batch_id = ? AND kind = 'release' AND reason = 'rejected'`, a.Batch); got != 600 {
		t.Errorf("released %d, want 600", got)
	}
	if out, err := func() (money.MicroUSD, error) {
		var o money.MicroUSD
		err := r.tx(func(tx *sql.Tx) (err error) { o, err = BatchOutstanding(ctx, tx, a.Batch); return })
		return o, err
	}(); err != nil || out != 0 {
		t.Errorf("outstanding %d %v", out, err)
	}
	// idempotent
	if ch, err := withdraw(a.Batch, nil); ch || err != nil {
		t.Errorf("second withdraw: %v %v", ch, err)
	}
	// neither approve nor reject (not even the idempotent reject) is possible any more
	for name, err := range map[string]error{
		"approve": r.tx(func(tx *sql.Tx) error { _, err := r.s.ApproveBatch(ctx, tx, a.Batch, humanP, ""); return err }),
		"reject":  r.tx(func(tx *sql.Tx) error { _, err := r.s.RejectBatch(ctx, tx, a.Batch, humanP, "no"); return err }),
	} {
		if !errors.Is(err, ErrBatchNotPending) {
			t.Errorf("%s after cancel: %v", name, err)
		}
	}
	// a plain human rejection is still idempotent and still INVALID_TRANSITION for approve
	rj := r.awaiting("w2")
	if err := r.tx(func(tx *sql.Tx) error { _, err := r.s.RejectBatch(ctx, tx, rj.Batch, humanP, "no"); return err }); err != nil {
		t.Fatal(err)
	}
	if ch, err := withdraw(rj.Batch, nil); ch || err != nil {
		t.Errorf("withdraw of a rejected batch: %v %v", ch, err)
	}
	// the lease predicate is honoured
	lost := r.lease
	lost.Owner = "intruder"
	l := r.awaiting("w3")
	if ch, err := withdraw(l.Batch, &lost); ch || err != nil {
		t.Errorf("withdraw without the lease: %v %v", ch, err)
	}
	if got := r.str(`SELECT state FROM approvals WHERE batch_id = ?`, l.Batch); got != "pending" {
		t.Errorf("approval %q after a fenced-out withdraw", got)
	}
}
