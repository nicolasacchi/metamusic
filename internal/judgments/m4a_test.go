package judgments_test

import (
	"context"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/judgments"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

func TestJudgments_CommentSourceSubsonic(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	c, err := e.svc.AddComment(ctx, device, "subsonic", lt.TrackLuna, judgments.NewComment{Body: "from a native app"})
	if err != nil || c.Source != "subsonic" {
		t.Fatalf("subsonic source: %+v %v", c, err)
	}
	if _, err := e.svc.AddComment(ctx, device, "carrier-pigeon", lt.TrackLuna, judgments.NewComment{Body: "x"}); err == nil {
		t.Fatal("unknown source accepted")
	}
}

func TestUpdateTrack_StatusSetAt(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	read := func() string {
		var s string
		if err := e.st.R.QueryRow(`SELECT status_set_at FROM tracks WHERE id=?`, lt.TrackLuna).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := read()
	e.now = e.now.Add(time.Hour)
	if err := e.svc.UpdateTrack(ctx, human, lt.TrackLuna, sp("Another Title"), nil); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != before {
		t.Fatalf("title-only change moved status_set_at: %q -> %q", before, got)
	}
	if err := e.svc.UpdateTrack(ctx, human, lt.TrackLuna, nil, sp("final")); err != nil {
		t.Fatal(err)
	}
	if got, want := read(), e.now.UTC().Format(time.RFC3339); got != want {
		t.Fatalf("status change: status_set_at = %q, want %q", got, want)
	}
}
