package desk_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/desk"
)

// TestVerdict_OwnerAndCritic: people write the owner row, agents the critic row; null clears and keeps the row; nothing is deleted.
func TestVerdict_OwnerAndCritic(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	pl := e.plan()
	ctx := context.Background()
	got, err := e.svc.SetVerdict(ctx, alice, pl.ID, desk.VerdictIn{Verdict: "liked", Note: "the chorus lifts"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Verdicts.Owner == nil || *got.Verdicts.Owner.Verdict != "liked" || *got.Verdicts.Owner.Note != "the chorus lifts" || got.Verdicts.Critic != nil {
		t.Fatalf("after owner: %+v", got.Verdicts)
	}
	if got.Verdict == nil || *got.Verdict != "liked" {
		t.Errorf("summary verdict %v", got.Verdict)
	}
	e.advance(60e9)
	got, err = e.svc.SetVerdict(ctx, robot, pl.ID, desk.VerdictIn{Verdict: "rejected"})
	if err != nil || got.Verdicts.Critic == nil || *got.Verdicts.Critic.Verdict != "rejected" || got.Verdicts.Critic.By.ID != "agent:tok1" || got.Verdicts.Critic.Note != nil {
		t.Fatalf("after critic: %v %+v", err, got.Verdicts.Critic)
	}
	if *got.Verdicts.Owner.Verdict != "liked" {
		t.Error("the critic changed the owner's row")
	}
	// a device of the owner is a person: the owner row; a grant or the system: the critic row
	dev := auth.Principal{Kind: auth.KindDevice, ID: "d1", Name: "phone", Role: auth.RoleOwner}
	if desk.Rater(dev) != "owner" || desk.Rater(auth.System("runner")) != "critic" || desk.Rater(bob) != "owner" {
		t.Error("rater rule")
	}
	// the owner changes their mind: the row is updated, then cleared but kept
	if got, err = e.svc.SetVerdict(ctx, alice, pl.ID, desk.VerdictIn{Verdict: "rejected"}); err != nil || *got.Verdicts.Owner.Verdict != "rejected" || got.Verdicts.Owner.Note != nil {
		t.Fatalf("update: %v %+v", err, got.Verdicts.Owner)
	}
	got, err = e.svc.SetVerdict(ctx, alice, pl.ID, desk.VerdictIn{})
	if err != nil || got.Verdicts.Owner == nil || got.Verdicts.Owner.Verdict != nil || got.Verdict != nil {
		t.Fatalf("clear: %v %+v", err, got.Verdicts.Owner)
	}
	if n := e.scalar(`SELECT COUNT(*) FROM desk_plan_verdicts WHERE plan_id = ?`, pl.ID); n != "2" {
		t.Errorf("%s verdict rows, want 2", n)
	}
	for _, bad := range []desk.VerdictIn{{Verdict: "meh"}, {Verdict: "liked", Note: strings.Repeat("n", desk.MaxNote+1)}} {
		if _, err := e.svc.SetVerdict(ctx, alice, pl.ID, bad); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
	_, err = e.svc.SetVerdict(ctx, alice, "no-such-plan", desk.VerdictIn{Verdict: "liked"})
	wantProblem(t, err, 404, "NOT_FOUND")
}

// TestVerdict_ClearNeverJudged: clearing a plan nobody judged records nothing.
func TestVerdict_ClearNeverJudged(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	pl := e.plan()
	got, err := e.svc.SetVerdict(context.Background(), alice, pl.ID, desk.VerdictIn{})
	if err != nil || got.Verdicts.Owner != nil || got.Verdicts.Critic != nil {
		t.Fatalf("%v %+v", err, got.Verdicts)
	}
	if n := e.scalar(`SELECT COUNT(*) FROM desk_plan_verdicts`); n != "0" {
		t.Errorf("%s verdict rows", n)
	}
}
