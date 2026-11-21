package library_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func measuredRig(t *testing.T) (*store.Store, *library.Service) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	lt.Fixture(t, st, lt.FixedNow)
	return st, library.New(st, nil, func() time.Time { return lt.FixedNow })
}

func addMeasured(t *testing.T, st *store.Store, svc *library.Service, id string, secs []library.NewSection) int {
	t.Helper()
	ctx := context.Background()
	tx, err := st.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	n, err := svc.AddMeasuredSections(ctx, tx, id, secs)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return n
}

var measuredSecs = []library.NewSection{
	{Idx: 0, Kind: "verse", Label: "verse 1", Raw: "[Verse 1]", Lines: []string{"la la luna"}, StartS: 1.5, EndS: 5, Confidence: 0.8},
	{Idx: 1, Kind: "chorus", Label: "chorus", Raw: "[Chorus]", Lines: []string{"sing it loud"}, StartS: 5, EndS: 9, Confidence: 0.7},
	{Idx: 2, Kind: "outro", Label: "outro", Raw: "[Outro]", StartS: 9, EndS: 12, Confidence: 0.5},
}

// TestAddMeasuredSections_OnceOnly: the first call inserts the rows, a second call is a no-op (rows are immutable) and an empty
// list inserts nothing.
func TestAddMeasuredSections_OnceOnly(t *testing.T) {
	t.Parallel()
	st, svc := measuredRig(t)
	id := lt.TrackLuna
	if n := addMeasured(t, st, svc, id, nil); n != 0 {
		t.Fatalf("empty list inserted %d rows", n)
	}
	if n := addMeasured(t, st, svc, id, measuredSecs); n != 3 {
		t.Fatalf("first call inserted %d rows, want 3", n)
	}
	other := []library.NewSection{{Idx: 0, Kind: "verse", Label: "x", Raw: "[X]", StartS: 0, EndS: 3, Confidence: 0.5}}
	if n := addMeasured(t, st, svc, id, other); n != 0 {
		t.Fatalf("second call inserted %d rows, want 0", n)
	}
	var cnt int
	var lines string
	if err := st.R.QueryRow(`SELECT count(*) FROM sections WHERE track_id = ? AND source = 'measured'`, id).Scan(&cnt); err != nil || cnt != 3 {
		t.Fatalf("measured rows = %d (%v), want 3", cnt, err)
	}
	if err := st.R.QueryRow(`SELECT lines_json FROM sections WHERE track_id = ? AND source = 'measured' AND idx = 2`, id).Scan(&lines); err != nil || lines != "[]" {
		t.Fatalf("a section without lines keeps lines_json = [] (got %q, %v)", lines, err)
	}
}

// TestAddMeasuredSections_BadSpanFails: a section the table's CHECKs refuse fails the call (the caller's transaction rolls back).
func TestAddMeasuredSections_BadSpanFails(t *testing.T) {
	t.Parallel()
	st, svc := measuredRig(t)
	ctx := context.Background()
	tx, err := st.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := svc.AddMeasuredSections(ctx, tx, lt.TrackLuna, []library.NewSection{{Idx: 0, Kind: "verse", Label: "v", Raw: "[V]", StartS: 4, EndS: 4, Confidence: 0.5}}); err == nil {
		t.Fatal("a zero-length section was accepted")
	}
}

// TestMeasuredSections_TakeOverPlanTiming: a take with plan sections reads the measured rows from library.Sections afterwards,
// with the measured confidence and the lines of the lyric sections.
func TestMeasuredSections_TakeOverPlanTiming(t *testing.T) {
	t.Parallel()
	st, svc := measuredRig(t)
	ctx := context.Background()
	id := lt.TrackLuna
	before, err := svc.Sections(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if before.Source != "plan" {
		t.Fatalf("fixture track has source %q before measuring; want plan", before.Source)
	}
	addMeasured(t, st, svc, id, measuredSecs)
	after, err := svc.Sections(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Source != "measured" || !after.Timed || len(after.Timeline) != 3 {
		t.Fatalf("after: source %q timed %v timeline %d, want measured, timed, 3", after.Source, after.Timed, len(after.Timeline))
	}
	if diff := cmp.Diff([]string{"la la luna"}, after.Timeline[0].Lines); diff != "" {
		t.Errorf("lines (-want +got):\n%s", diff)
	}
	if after.Timeline[0].StartS != 1.5 || after.Confidence == nil || *after.Confidence != 0.8 {
		t.Errorf("start %v confidence %v, want 1.5 and 0.8", after.Timeline[0].StartS, after.Confidence)
	}
}
