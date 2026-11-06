package judgments_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestSystemTransitions_Table covers every (writer, from, to) cell of C255.3, and update_comment (Transition) keeps refusing
// the M5-only pairs.
func TestSystemTransitions_Table(t *testing.T) {
	t.Parallel()
	states := []string{"open", "addressed", "resolved", "regressed"}
	allowed := map[judgments.SystemWriter]map[[2]string]bool{
		judgments.SystemJob: {{"open", "addressed"}: true, {"regressed", "addressed"}: true},
		judgments.SystemHumanCallback: {
			{"open", "resolved"}: true, {"addressed", "resolved"}: true, {"regressed", "resolved"}: true,
			{"open", "open"}: true, {"addressed", "open"}: true, {"regressed", "open"}: true,
			{"open", "regressed"}: true, {"addressed", "regressed"}: true,
		},
	}
	for w, ok := range allowed {
		for _, from := range states {
			for _, to := range states {
				want := ok[[2]string{from, to}] || from == to
				err := judgments.SystemTransition(w, from, to)
				if (err == nil) != want {
					t.Errorf("%s %s -> %s: err %v, want allowed=%v", w, from, to, err, want)
				}
				if err != nil && !errors.Is(err, judgments.ErrInvalidTransition) {
					t.Errorf("%s %s -> %s: %v is not ErrInvalidTransition", w, from, to, err)
				}
			}
		}
	}
	if judgments.SystemTransition("nobody", "open", "addressed") == nil || judgments.SystemTransition(judgments.SystemJob, "open", "bogus") == nil {
		t.Error("unknown writer or state must be refused")
	}
	for _, from := range states {
		if err := judgments.Transition(from, "addressed"); from != "addressed" && err == nil {
			t.Errorf("update_comment may not move %s -> addressed", from)
		}
		if err := judgments.Transition(from, "regressed"); from != "regressed" && err == nil {
			t.Errorf("update_comment may not move %s -> regressed", from)
		}
	}
}

func inTx(t *testing.T, e *env, f func(tx *sql.Tx) error) error {
	t.Helper()
	tx, err := e.st.W.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func TestSystemMoveTx(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	tr := addTake(t, e, 91, 100, "")
	c, err := e.svc.AddComment(ctx, human, "web", tr, judgments.NewComment{Body: "late", Kind: "fix", AtSecond: new(float64)})
	if err != nil {
		t.Fatal(err)
	}
	var mv judgments.Move
	if err := inTx(t, e, func(tx *sql.Tx) error {
		var err error
		mv, err = judgments.SystemMoveTx(ctx, tx, judgments.SystemJob, c.ID, "addressed", "system:job", e.now, nil)
		return err
	}); err != nil || !mv.Moved || mv.From != "open" {
		t.Fatalf("move: %+v %v", mv, err)
	}
	got, _ := e.svc.Comments(ctx, tr, judgments.CommentFilter{})
	if got.Items[0].State != "addressed" || got.Items[0].StateChangedAt == nil {
		t.Fatalf("comment after move: %+v", got.Items[0])
	}
	// a second job move is refused (addressed -> addressed is a no-op, not a move)
	if err := inTx(t, e, func(tx *sql.Tx) error {
		mv, err = judgments.SystemMoveTx(ctx, tx, judgments.SystemJob, c.ID, "addressed", "system:job", e.now, nil)
		return err
	}); err != nil || mv.Moved {
		t.Fatalf("repeat: %+v %v", mv, err)
	}
	// resolved is never moved by a callback
	if _, err := e.svc.UpdateComment(ctx, human, c.ID, judgments.CommentPatch{State: sp("resolved")}); err != nil {
		t.Fatal(err)
	}
	err = inTx(t, e, func(tx *sql.Tx) error {
		_, err := judgments.SystemMoveTx(ctx, tx, judgments.SystemHumanCallback, c.ID, "open", "human:alice", e.now, nil)
		return err
	})
	if !errors.Is(err, judgments.ErrInvalidTransition) {
		t.Fatalf("resolved -> open by a callback: %v", err)
	}
	err = inTx(t, e, func(tx *sql.Tx) error {
		_, err := judgments.SystemMoveTx(ctx, tx, judgments.SystemJob, "no-such", "addressed", "system:job", e.now, nil)
		return err
	})
	if !errors.Is(err, library.ErrNotFound) {
		t.Fatalf("unknown comment: %v", err)
	}
	// a fence whose lease is not held fails the write
	f := store.LeaseFence{Owner: "ghost", Now: e.now}
	c2, _ := e.svc.AddComment(ctx, human, "web", tr, judgments.NewComment{Body: "again", Kind: "fix"})
	err = inTx(t, e, func(tx *sql.Tx) error {
		_, err := judgments.SystemMoveTx(ctx, tx, judgments.SystemJob, c2.ID, "addressed", "system:job", e.now, &f)
		return err
	})
	if !errors.Is(err, store.ErrLeaseLost) {
		t.Fatalf("lost lease: %v", err)
	}
}

func TestAddCommentTx(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	tr := addTake(t, e, 92, 100, "")
	at := 12.34
	err := inTx(t, e, func(tx *sql.Tx) error {
		return judgments.AddCommentTx(ctx, tx, judgments.NewCommentTx{ID: "01900000-0000-7000-8000-00000000c001", TrackID: tr, Body: "Lost from parent: " + strings.Repeat("x", 5000),
			Kind: "fix", Source: "callback", AtSecond: &at, Author: human, Now: e.now})
	})
	if err != nil {
		t.Fatal(err)
	}
	pg, err := e.svc.Comments(ctx, tr, judgments.CommentFilter{})
	if err != nil || len(pg.Items) != 1 {
		t.Fatalf("%v %+v", err, pg)
	}
	c := pg.Items[0]
	if c.Source != "callback" || c.State != "open" || c.Kind != "fix" || len(c.Body) != 4000 || *c.AtSecond != 12.3 || c.Author.ID != human.Key() {
		t.Errorf("comment %+v", c)
	}
	// bad input
	for name, in := range map[string]judgments.NewCommentTx{
		"source": {ID: "x1", TrackID: tr, Body: "b", Kind: "fix", Source: "nope", Author: human, Now: e.now},
		"kind":   {ID: "x2", TrackID: tr, Body: "b", Kind: "nope", Source: "callback", Author: human, Now: e.now},
		"body":   {ID: "x3", TrackID: tr, Body: "  ", Kind: "fix", Source: "callback", Author: human, Now: e.now},
		"track":  {ID: "x4", TrackID: "nope", Body: "b", Kind: "fix", Source: "callback", Author: human, Now: e.now},
	} {
		if err := inTx(t, e, func(tx *sql.Tx) error { return judgments.AddCommentTx(ctx, tx, in) }); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
