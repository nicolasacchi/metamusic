package listening_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/listening"
)

// TestSession_Summary: fixture events and ours group per track; agent events and diagnostics are excluded (C112.3).
func TestSession_Summary(t *testing.T) {
	t.Parallel()
	s, _ := newSvc(t)
	ctx := context.Background()
	at := func(d time.Duration) time.Time { return lt.FixedNow.Add(d) }
	mk := func(n int, track, kind string, from, to *int64, when time.Duration) listening.Event {
		return listening.Event{ID: id(100 + n), TrackID: track, SessionID: "session-2", Kind: kind, FromMS: from, ToMS: to, ClientAt: at(when)}
	}
	humanEvs := []listening.Event{
		mk(1, lt.TrackCitta, "start", nil, nil, -10*time.Minute),
		mk(2, lt.TrackCitta, "segment", i64(0), i64(3000), -9*time.Minute),
		mk(3, lt.TrackCitta, "segment", i64(4000), i64(6000), -8*time.Minute),
		mk(4, lt.TrackCitta, "skip", nil, nil, -7*time.Minute),
		mk(5, lt.TrackCitta, "advance", nil, nil, -7*time.Minute), // diagnostic: not a play
		mk(6, lt.TrackShort, "start", nil, nil, -3*time.Hour),     // before since
	}
	if _, _, rej, err := s.Log(ctx, human, "web", humanEvs); err != nil || len(rej) != 0 {
		t.Fatalf("log: %v %v", err, rej)
	}
	if _, _, rej, err := s.Log(ctx, dev, "subsonic", []listening.Event{mk(7, lt.TrackCitta, "start", nil, nil, -5*time.Minute)}); err != nil || len(rej) != 0 {
		t.Fatalf("device log: %v %v", err, rej)
	}
	agentEv := []listening.Event{mk(8, lt.TrackCitta, "start", nil, nil, -4*time.Minute), mk(9, lt.TrackShort, "start", nil, nil, -4*time.Minute)}
	if _, _, rej, err := s.Log(ctx, agent, "api", agentEv); err != nil || len(rej) != 0 {
		t.Fatalf("agent log: %v %v", err, rej)
	}

	got, err := s.Session(ctx, at(-time.Hour), 20)
	if err != nil {
		t.Fatal(err)
	}
	three := 3
	want := listening.Session{
		Since: at(-time.Hour), Until: lt.FixedNow,
		Tracks: []listening.SessionTrack{
			// newest last play first: Citta's device start at -5 min, then Luna's fixture events at -20 min
			{TrackID: lt.TrackCitta, Title: "Città lenta", Plays: 2, Completed: 0, Skipped: 1, SecondsHeard: 5, LastPlayedAt: at(-5 * time.Minute), Stars: &three, OpenComments: got.Tracks[0].OpenComments},
			{TrackID: lt.TrackLuna, Title: "Luna Park", Plays: 2, Completed: 1, Skipped: 0, SecondsHeard: 0, LastPlayedAt: at(-19 * time.Minute), Stars: got.Tracks[1].Stars, OpenComments: got.Tracks[1].OpenComments},
		},
		Totals: listening.SessionTotals{Tracks: 2, Plays: 4, SecondsHeard: 5},
	}
	if len(got.Tracks) != 2 {
		t.Fatalf("tracks = %+v", got.Tracks)
	}
	if got.Tracks[1].Stars == nil || *got.Tracks[1].Stars != 5 {
		t.Errorf("Luna stars = %v, want 5 (the human's rating, not the agent's 2)", got.Tracks[1].Stars)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("session (-want +got):\n%s", diff)
	}

	// limit trims the list but the totals describe the listed tracks only
	one, err := s.Session(ctx, at(-time.Hour), 1)
	if err != nil || len(one.Tracks) != 1 || one.Totals.Tracks != 1 || one.Totals.Plays != 2 {
		t.Errorf("limit 1: %+v %v", one, err)
	}
	// empty window
	none, err := s.Session(ctx, at(time.Minute), 20)
	if err != nil || len(none.Tracks) != 0 || none.Tracks == nil || none.Totals != (listening.SessionTotals{}) {
		t.Errorf("empty: %+v %v", none, err)
	}
}
