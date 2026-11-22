package judgments_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

func inWriteTx(t *testing.T, e *env, fn func(tx *sql.Tx) error) error {
	t.Helper()
	tx, err := e.st.W.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// TestJudgmentsTx_SameBehaviour: AddCommentTx and RateTx write what AddComment and Rate write, refuse what they refuse, and leave
// nothing behind when the caller's transaction rolls back (C381.4).
func TestJudgmentsTx_SameBehaviour(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	at := 4.04
	in := judgments.NewComment{Body: "  a keep  ", Kind: "keep", AtSecond: &at}

	want, err := e.svc.AddComment(ctx, human, "headset", lt.TrackLuna, in)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := inWriteTx(t, e, func(tx *sql.Tx) (err error) {
		id, err = e.svc.AddCommentTx(ctx, tx, human, "headset", lt.TrackLuna, in)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.CommentByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if d := cmp.Diff(want, got, cmpopts.IgnoreFields(judgments.Comment{}, "ID")); d != "" {
		t.Errorf("AddCommentTx differs from AddComment (-want +got):\n%s", d)
	}

	// the same refusals
	long := 9999.0
	neg := -1.0
	for name, tc := range map[string]struct {
		src, track string
		in         judgments.NewComment
	}{
		"unknown source": {"nope", lt.TrackLuna, in},
		"empty body":     {"headset", lt.TrackLuna, judgments.NewComment{Body: " "}},
		"bad kind":       {"headset", lt.TrackLuna, judgments.NewComment{Body: "x", Kind: "shout"}},
		"unknown track":  {"headset", "01900000-0000-7000-8000-0000000000ff", in},
		"past the end":   {"headset", lt.TrackLuna, judgments.NewComment{Body: "x", AtSecond: &long}},
		"negative":       {"headset", lt.TrackLuna, judgments.NewComment{Body: "x", AtSecond: &neg}},
	} {
		_, wantErr := e.svc.AddComment(ctx, human, tc.src, tc.track, tc.in)
		gotErr := inWriteTx(t, e, func(tx *sql.Tx) error {
			_, err := e.svc.AddCommentTx(ctx, tx, human, tc.src, tc.track, tc.in)
			return err
		})
		if wantErr == nil || gotErr == nil || wantErr.Error() != gotErr.Error() {
			t.Errorf("%s: AddComment %v, AddCommentTx %v", name, wantErr, gotErr)
		}
	}
	if errors.Is(inWriteTx(t, e, func(tx *sql.Tx) error {
		_, err := e.svc.AddCommentTx(ctx, tx, human, "headset", "01900000-0000-7000-8000-0000000000ff", in)
		return err
	}), library.ErrNotFound) == false {
		t.Error("an unknown track is not library.ErrNotFound")
	}

	// ratings: the same row, the same refusals
	if _, err := e.svc.Rate(ctx, human, lt.TrackCitta, ip(3), sp("words")); err != nil {
		t.Fatal(err)
	}
	if err := inWriteTx(t, e, func(tx *sql.Tx) error { return e.svc.RateTx(ctx, tx, human, lt.TrackTwinA, ip(3), sp("words")) }); err != nil {
		t.Fatal(err)
	}
	row := func(track string) string {
		var s string
		if err := e.st.R.QueryRow(`SELECT stars || '/' || verdict || '/' || source || '/' || rater FROM ratings WHERE track_id = ? AND rater = 'owner'`, track).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if a, b := row(lt.TrackCitta), row(lt.TrackTwinA); a != b {
		t.Errorf("Rate wrote %s, RateTx %s", a, b)
	}
	for name, tc := range map[string]struct {
		track string
		stars *int
	}{"stars range": {lt.TrackLuna, ip(6)}, "unknown track": {"01900000-0000-7000-8000-0000000000ff", ip(3)}} {
		_, wantErr := e.svc.Rate(ctx, human, tc.track, tc.stars, nil)
		gotErr := inWriteTx(t, e, func(tx *sql.Tx) error { return e.svc.RateTx(ctx, tx, human, tc.track, tc.stars, nil) })
		if wantErr == nil || gotErr == nil || wantErr.Error() != gotErr.Error() {
			t.Errorf("%s: Rate %v, RateTx %v", name, wantErr, gotErr)
		}
	}
	if gotErr := inWriteTx(t, e, func(tx *sql.Tx) error { return e.svc.RateTx(ctx, tx, auth.Principal{}, lt.TrackLuna, ip(3), nil) }); gotErr == nil {
		t.Error("an anonymous principal rated")
	}

	// a rollback takes the comment and the rating with it
	count := func() string {
		var s string
		if err := e.st.R.QueryRow(`SELECT (SELECT COUNT(*) FROM comments) || '/' || (SELECT COUNT(*) FROM ratings)`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := count()
	boom := fmt.Errorf("boom")
	err = inWriteTx(t, e, func(tx *sql.Tx) error {
		if _, err := e.svc.AddCommentTx(ctx, tx, human, "headset", lt.TrackLuna, in); err != nil {
			return err
		}
		if err := e.svc.RateTx(ctx, tx, human, lt.TrackLuna, ip(5), nil); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) || count() != before {
		t.Errorf("rollback: %v, rows %s -> %s", err, before, count())
	}
}

// TestComments_SourceHeadset: headset is a comment source (the Morning Crate's), through AddComment too, and the list reports it.
func TestComments_SourceHeadset(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	c, err := e.svc.AddComment(ctx, human, "headset", lt.TrackLuna, judgments.NewComment{Body: "Headset marker", Kind: "marker", AtSecond: new(float64)})
	if err != nil {
		t.Fatal(err)
	}
	if c.Source != "headset" || c.Kind != "marker" {
		t.Fatalf("comment %+v", c)
	}
	if _, err := e.svc.AddComment(ctx, human, "headsetx", lt.TrackLuna, judgments.NewComment{Body: "x"}); err == nil {
		t.Fatal("an unknown source was accepted")
	}
}
