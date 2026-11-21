package mandates

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
)

func wantProblem(t *testing.T, err error, status int, code string) *problem.Error {
	t.Helper()
	var pe *problem.Error
	if !errors.As(err, &pe) || pe.Status != status || pe.Code != code {
		t.Fatalf("err = %v, want %d %s", err, status, code)
	}
	return pe
}

func TestCreateMandate_HumanOnlyOwnerOnly(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	m, err := r.svc.Create(ctx, owner, r.input())
	if err != nil {
		t.Fatal(err)
	}
	if m.State != StateApproved || m.HolderKey != "agent:nightshift" || m.CreatedBy != "human:alice" || m.EffectiveState(t0) != EffLive {
		t.Fatalf("%+v", m)
	}
	got, err := r.svc.Get(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Budget != 1_000_000 || got.Scope.MaxBatchCeilingMicroUSD != 943_587 || got.Scope.RootTrackIDs != nil || !got.ValidFrom.Equal(t0) {
		t.Fatalf("stored %+v", got)
	}
	// the stored mandate is the only one: the refusals below leave nothing behind
	for _, tc := range []struct {
		who  auth.Principal
		code string
	}{{editor, "OWNER_ONLY"}, {listener, "OWNER_ONLY"}, {agent, "HUMAN_ONLY"}, {ownerDev, "HUMAN_ONLY"}, {auth.CLI, "HUMAN_ONLY"}} {
		r2 := newRig(t)
		_, err := r2.svc.Create(ctx, tc.who, r2.input())
		wantProblem(t, err, 403, tc.code)
		if n := r2.str(`SELECT COUNT(*) FROM mandates`); n != "0" {
			t.Fatalf("%s: %s rows", tc.who.Key(), n)
		}
	}
}

func TestCreateMandate_OneActivePerHolder(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	first := r.create()
	_, err := r.svc.Create(ctx, owner, r.input())
	pe := wantProblem(t, err, 409, "MANDATE_ACTIVE")
	if pe.Details["mandate_id"] != first.ID {
		t.Fatalf("details %v", pe.Details)
	}
	// a scheduled mandate counts too
	r2 := newRig(t)
	in := r2.input()
	later := t0.Add(2 * time.Hour)
	in.ValidFrom = &later
	sched, err := r2.svc.Create(ctx, owner, in)
	if err != nil {
		t.Fatal(err)
	}
	if sched.EffectiveState(t0) != EffScheduled {
		t.Fatalf("effective %s", sched.EffectiveState(t0))
	}
	_, err = r2.svc.Create(ctx, owner, r2.input())
	wantProblem(t, err, 409, "MANDATE_ACTIVE")
	// revoked and ended ones do not
	if _, err := r.svc.Revoke(ctx, owner, first.ID, "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Create(ctx, owner, r.input()); err != nil {
		t.Fatalf("after revoke: %v", err)
	}
	r3 := newRig(t)
	r3.create()
	r3.clk.Set(t0.Add(8 * 24 * time.Hour)) // past valid_until
	in3 := r3.input()
	in3.ValidUntil = r3.clk.Now().Add(24 * time.Hour)
	if _, err := r3.svc.Create(ctx, owner, in3); err != nil {
		t.Fatalf("after the first ended: %v", err)
	}
}

func TestCreateMandate_EngineNotAdopted(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	in := r.input()
	in.Scope.Engines = []string{"yue2", "acestep15"}
	_, err := r.svc.Create(context.Background(), owner, in)
	pe := wantProblem(t, err, 422, "MANDATE_SCOPE_INVALID")
	if pe.Details["rule"] != "engine_not_adopted" || pe.Details["field"] != "scope.engines" {
		t.Fatalf("details %v", pe.Details)
	}
	if _, err := r.adopt.Adopt(context.Background(), "cli:test", "acestep15", "trial passed"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Create(context.Background(), owner, in); err != nil {
		t.Fatalf("an adopted engine is allowed: %v", err)
	}
}

func TestCreateMandate_CeilingAboveBatchCap(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	caps, err := r.spend.LoadCaps(context.Background(), r.st.R)
	if err != nil {
		t.Fatal(err)
	}
	in := r.input()
	in.Budget = 100_000_000
	in.Scope.MaxBatchCeilingMicroUSD = caps.BatchMicro + 1
	_, err = r.svc.Create(context.Background(), owner, in)
	pe := wantProblem(t, err, 422, "MANDATE_SCOPE_INVALID")
	if pe.Details["rule"] != "ceiling_above_batch_cap" {
		t.Fatalf("details %v", pe.Details)
	}
	in.Scope.MaxBatchCeilingMicroUSD = caps.BatchMicro // the cap itself is allowed
	if _, err := r.svc.Create(context.Background(), owner, in); err != nil {
		t.Fatal(err)
	}
}

func TestCreateMandate_RootsMustExist(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	in := r.input()
	in.Scope.RootTrackIDs = []string{r.rootIDs[0], "no-such-track"}
	_, err := r.svc.Create(context.Background(), owner, in)
	wantProblem(t, err, 422, "MANDATE_SCOPE_INVALID")
	in.Scope.RootTrackIDs = r.rootIDs[:1]
	m, err := r.svc.Create(context.Background(), owner, in)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := r.svc.Get(context.Background(), m.ID)
	if len(got.Scope.RootTrackIDs) != 1 || got.Scope.RootTrackIDs[0] != r.rootIDs[0] {
		t.Fatalf("roots %v", got.Scope.RootTrackIDs)
	}
}

func TestCreateMandate_HeaderRules(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	past := t0.Add(-6 * time.Minute)
	slightly := t0.Add(-4 * time.Minute)
	for _, tc := range []struct {
		name string
		mut  func(*CreateInput)
		rule string
	}{
		{"holder", func(in *CreateInput) { in.Holder = "someone" }, "holder_unknown"},
		{"empty title", func(in *CreateInput) { in.Title = "" }, "title_length"},
		{"long title", func(in *CreateInput) { in.Title = string(make([]rune, 121)) }, "title_length"},
		{"budget 0", func(in *CreateInput) { in.Budget = 0 }, "budget_range"},
		{"budget over", func(in *CreateInput) { in.Budget = 100_000_001 }, "budget_range"},
		{"from past", func(in *CreateInput) { in.ValidFrom = &past }, "valid_from_past"},
		{"until before from", func(in *CreateInput) { in.ValidUntil = t0 }, "valid_until_before_from"},
		{"too far", func(in *CreateInput) { in.ValidUntil = t0.Add(31*24*time.Hour + time.Second) }, "valid_until_too_far"},
	} {
		in := r.input()
		tc.mut(&in)
		_, err := r.svc.Create(ctx, owner, in)
		pe := wantProblem(t, err, 422, "VALIDATION_FAILED")
		if pe.Details["rule"] != tc.rule {
			t.Errorf("%s: rule %v, want %s", tc.name, pe.Details["rule"], tc.rule)
		}
	}
	in := r.input()
	in.ValidFrom = &slightly // inside the 5 minute slack
	in.ValidUntil = t0.Add(31*24*time.Hour - 4*time.Minute)
	if _, err := r.svc.Create(ctx, owner, in); err != nil {
		t.Fatalf("slack and exact 31 days: %v", err)
	}
}

func TestMandateEffectiveState_Table(t *testing.T) {
	t.Parallel()
	from, until := t0, t0.Add(time.Hour)
	revoked := "x"
	for _, tc := range []struct {
		name  string
		m     Mandate
		now   time.Time
		state string
	}{
		{"before", Mandate{State: StateApproved, ValidFrom: from, ValidUntil: until}, from.Add(-time.Nanosecond), EffScheduled},
		{"at from", Mandate{State: StateApproved, ValidFrom: from, ValidUntil: until}, from, EffLive},
		{"inside", Mandate{State: StateApproved, ValidFrom: from, ValidUntil: until}, from.Add(time.Minute), EffLive},
		{"just before until", Mandate{State: StateApproved, ValidFrom: from, ValidUntil: until}, until.Add(-time.Nanosecond), EffLive},
		{"at until", Mandate{State: StateApproved, ValidFrom: from, ValidUntil: until}, until, EffEnded},
		{"after", Mandate{State: StateApproved, ValidFrom: from, ValidUntil: until}, until.Add(time.Hour), EffEnded},
		{"revoked live", Mandate{State: StateRevoked, RevokedBy: &revoked, ValidFrom: from, ValidUntil: until}, from.Add(time.Minute), EffRevoked},
		{"revoked ended", Mandate{State: StateRevoked, ValidFrom: from, ValidUntil: until}, until.Add(time.Hour), EffRevoked},
	} {
		if got := tc.m.EffectiveState(tc.now); got != tc.state {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.state)
		}
	}
}

// TestMandateTransitions_GoEqualsSQL: the trigger refuses exactly the stored-state moves Transitions does not list.
func TestMandateTransitions_GoEqualsSQL(t *testing.T) {
	t.Parallel()
	states := []string{StateApproved, StateRevoked}
	for _, from := range states {
		for _, to := range states {
			if from == to {
				continue
			}
			r := newRig(t)
			m := r.create()
			if from == StateRevoked {
				r.exec(`UPDATE mandates SET state='revoked', revoked_by='human:alice', revoked_at=?, revoke_reason='x' WHERE id=?`, "2026-10-14T12:00:00.000Z", m.ID)
			}
			var err error
			if to == StateApproved {
				_, err = r.st.W.Exec(`UPDATE mandates SET state='approved', revoked_by=NULL, revoked_at=NULL, revoke_reason=NULL WHERE id=?`, m.ID)
			} else {
				_, err = r.st.W.Exec(`UPDATE mandates SET state='revoked', revoked_by='human:alice', revoked_at=?, revoke_reason='x' WHERE id=?`, "2026-10-14T12:00:00.000Z", m.ID)
			}
			allowed := false
			for _, tr := range Transitions {
				allowed = allowed || (tr.From == from && tr.To == to)
			}
			if (err == nil) != allowed {
				t.Errorf("%s -> %s: SQL err %v, Go allowed %v", from, to, err, allowed)
			}
		}
	}
}

func TestRevokeMandate_CancelsInSameTx(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	m := r.create()
	b1, b2 := r.batch(m.ID, 1000), r.batch(m.ID, 2000)
	r.batch("", 3000) // a batch of no mandate is never cancelled
	// a failing canceller rolls the revoke back, audit included
	r.cancel = func(context.Context, *sql.Tx, []string, string) error { return errors.New("boom") }
	if _, err := r.svc.Revoke(ctx, owner, m.ID, "stop"); err == nil {
		t.Fatal("revoke succeeded despite the canceller failing")
	}
	if got := r.str(`SELECT state FROM mandates WHERE id = ?`, m.ID); got != "approved" {
		t.Fatalf("state %s after a failed cancel", got)
	}
	if r.wakes != 0 {
		t.Fatal("woken after a rollback")
	}
	// the real shape: the canceller writes inside the revoke's transaction
	r.cancel = func(ctx context.Context, tx *sql.Tx, ids []string, by string) error {
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE batches SET reconciled_at = NULL WHERE id = ?`, id); err != nil {
				return err
			}
		}
		return nil
	}
	r.calls = nil
	pe := auditCtx(ctx, owner)
	got, err := r.svc.Revoke(pe, owner, m.ID, "stop spending")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateRevoked || got.RevokedBy == nil || *got.RevokedBy != "human:alice" || *got.RevokeReason != "stop spending" || got.EffectiveState(t0) != EffRevoked {
		t.Fatalf("%+v", got)
	}
	if len(r.calls) != 1 || r.calls[0].by != "system:mandate-revoke" || len(r.calls[0].batches) != 2 || r.calls[0].batches[0] != b1 || r.calls[0].batches[1] != b2 {
		t.Fatalf("canceller calls %+v", r.calls)
	}
	if r.wakes != 1 {
		t.Fatalf("wakes %d", r.wakes)
	}
	if n := r.str(`SELECT COUNT(*) FROM audit_log WHERE op = 'revoke_mandate' AND status = 200`); n != "1" {
		t.Fatalf("audit rows %s", n)
	}
	// revoking again, or an unknown id
	_, err = r.svc.Revoke(ctx, owner, m.ID, "again")
	wantProblem(t, err, 409, "INVALID_TRANSITION")
	_, err = r.svc.Revoke(ctx, owner, "nope", "x")
	wantProblem(t, err, 404, "NOT_FOUND")
}

func auditCtx(ctx context.Context, p auth.Principal) context.Context {
	return audit.WithPending(ctx, audit.Entry{At: t0, Principal: p, Transport: reqinfo.REST, Op: "revoke_mandate", Method: "POST", ArgsSHA256: "abc"})
}

func TestRevokeMandate_FromOwnerDevice(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	m := r.create()
	got, err := r.svc.Revoke(context.Background(), ownerDev, m.ID, "phone says stop")
	if err != nil {
		t.Fatal(err)
	}
	if *got.RevokedBy != "device:dev1" {
		t.Fatalf("by %s", *got.RevokedBy)
	}
}

func TestRevokeMandate_AgentWrite(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	m := r.create()
	for _, p := range []auth.Principal{editor, listener} {
		_, err := r.svc.Revoke(context.Background(), p, m.ID, "x")
		wantProblem(t, err, 403, "OWNER_ONLY")
	}
	if _, err := r.svc.Revoke(context.Background(), agent, m.ID, ""); err == nil {
		t.Fatal("an empty reason was accepted")
	}
	got, err := r.svc.Revoke(context.Background(), agent, m.ID, "the agent only reduces spend")
	if err != nil {
		t.Fatal(err)
	}
	if *got.RevokedBy != "agent:ag1" {
		t.Fatalf("by %s", *got.RevokedBy)
	}
}

func TestRevokeMandate_EndedIsInvalid(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	m := r.create()
	r.clk.Set(m.ValidUntil)
	_, err := r.svc.Revoke(context.Background(), owner, m.ID, "late")
	wantProblem(t, err, 409, "INVALID_TRANSITION")
}

func TestMandates_ListFilters(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	// three mandates in sequence: ended, revoked, live
	first := r.create()
	r.clk.Set(first.ValidUntil.Add(time.Minute))
	in := r.input()
	in.ValidUntil = r.clk.Now().Add(24 * time.Hour)
	second, err := r.svc.Create(ctx, owner, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Revoke(ctx, owner, second.ID, "x"); err != nil {
		t.Fatal(err)
	}
	in.ValidUntil = r.clk.Now().Add(48 * time.Hour)
	third, err := r.svc.Create(ctx, owner, in)
	if err != nil {
		t.Fatal(err)
	}
	ids := func(f Filter) []string {
		pg, err := r.svc.List(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, m := range pg.Items {
			out = append(out, m.ID)
		}
		return out
	}
	eq := func(name string, got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: %v, want %v", name, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%s: %v, want %v", name, got, want)
			}
		}
	}
	eq("all newest first", ids(Filter{}), third.ID, second.ID, first.ID)
	eq("live", ids(Filter{EffectiveStates: []string{EffLive}}), third.ID)
	eq("ended", ids(Filter{EffectiveStates: []string{EffEnded}}), first.ID)
	eq("revoked", ids(Filter{EffectiveStates: []string{EffRevoked}}), second.ID)
	eq("live or ended", ids(Filter{EffectiveStates: []string{EffLive, EffEnded}}), third.ID, first.ID)
	eq("stored revoked", ids(Filter{State: StateRevoked}), second.ID)
	eq("holder", ids(Filter{Holder: HolderNightShift}), third.ID, second.ID, first.ID)
	eq("scheduled", ids(Filter{EffectiveStates: []string{EffScheduled}}))
	pg, err := r.svc.List(ctx, Filter{Limit: 2})
	if err != nil || len(pg.Items) != 2 || pg.NextCursor != second.ID {
		t.Fatalf("page 1: %+v %v", pg, err)
	}
	eq("page 2", ids(Filter{Limit: 2, Cursor: pg.NextCursor}), first.ID)
	for _, bad := range []Filter{{Limit: 101}, {State: "x"}, {Holder: "x"}, {EffectiveStates: []string{"x"}}} {
		_, err := r.svc.List(ctx, bad)
		wantProblem(t, err, 422, "VALIDATION_FAILED")
	}
	_, err = r.svc.Get(ctx, "nope")
	wantProblem(t, err, 404, "NOT_FOUND")
}

func TestMandates_GETNeverWrites(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	m := r.create()
	r.batch(m.ID, 1000)
	count := func() string {
		return r.str(`SELECT (SELECT COUNT(*) FROM mandates) || '|' || (SELECT COUNT(*) FROM batches) || '|' || (SELECT COUNT(*) FROM ledger) || '|' ||
			(SELECT COUNT(*) FROM audit_log) || '|' || (SELECT COUNT(*) FROM jobs) || '|' || (SELECT group_concat(state) FROM mandates)`)
	}
	before := count()
	if _, err := r.svc.Get(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.List(ctx, Filter{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Usage(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.svc.Live(ctx, r.st.R, "agent:nightshift"); err != nil {
		t.Fatal(err)
	}
	if after := count(); after != before {
		t.Fatalf("a read wrote: %s -> %s", before, after)
	}
}

func TestMandates_LiveAndActive(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	if m, _, err := r.svc.Live(ctx, r.st.R, "agent:nightshift"); err != nil || m != nil {
		t.Fatalf("empty: %v %v", m, err)
	}
	in := r.input()
	later := t0.Add(time.Hour)
	in.ValidFrom = &later
	m, err := r.svc.Create(ctx, owner, in)
	if err != nil {
		t.Fatal(err)
	}
	if live, _, _ := r.svc.Live(ctx, r.st.R, "agent:nightshift"); live != nil {
		t.Fatal("a scheduled mandate is not live")
	}
	if act, err := r.svc.Active(ctx, r.st.R, "agent:nightshift"); err != nil || act == nil || act.ID != m.ID {
		t.Fatalf("active %v %v", act, err)
	}
	r.clk.Set(later.Add(time.Minute))
	r.batch(m.ID, 1000)
	live, u, err := r.svc.Live(ctx, r.st.R, "agent:nightshift")
	if err != nil || live == nil || live.ID != m.ID || u.Committed != 1000 || u.Batches != 1 {
		t.Fatalf("live %v usage %+v %v", live, u, err)
	}
	if r.svc.Location().String() != "UTC" {
		t.Fatalf("location %s", r.svc.Location())
	}
	if live, _, _ := r.svc.Live(ctx, r.st.R, "agent:other"); live != nil {
		t.Fatal("another holder has no mandate")
	}
}

func TestMandates_NoPhysicalDelete(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.create()
	if _, err := r.st.W.Exec(`DELETE FROM mandates`); err == nil {
		t.Fatal("a mandate was deleted")
	}
}
