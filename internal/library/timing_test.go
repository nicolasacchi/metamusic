package library_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestTiming_RankMatchesSelectSections: library.Timing and Sections (which applies selectSections) pick the same source and
// the same rows for every combination of sources a take can hold; lyrics-only and section-less takes are untimed.
func TestTiming_RankMatchesSelectSections(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fx := lt.FixtureL3(t, st, lt.FixedNow)
	svc := library.New(st, nil, func() time.Time { return lt.FixedNow })
	add := func(track, source string, idx int, kind, label string, from, to float64) {
		t.Helper()
		if _, err := st.W.Exec(`INSERT INTO sections(track_id, source, idx, kind, label, raw_label, start_s, end_s, confidence) VALUES (?,?,?,?,?,?,?,?,0.9)`,
			track, source, idx, kind, label, "["+label+"]", from, to); err != nil {
			t.Fatal(err)
		}
	}
	// T1: plan only; T2: plan + manual; T3: measured + plan; T4: all three; T5: left with its lyrics rows only
	plan := func(track string) {
		add(track, "plan", 0, "verse", "verse 1", 0, 50)
		add(track, "plan", 1, "chorus", "chorus 1", 50, 80)
		add(track, "plan", 2, "verse", "verse 2", 80, 120)
	}
	manual := func(track string) {
		add(track, "manual", 0, "verse", "verse", 0, 40)
		add(track, "manual", 1, "chorus", "chorus", 40, 100)
	}
	measured := func(track string) {
		add(track, "measured", 0, "intro", "intro", 0, 10)
		add(track, "measured", 1, "verse", "verse", 10, 60)
	}
	plan(fx.Takes["T1"])
	plan(fx.Takes["T2"])
	manual(fx.Takes["T2"])
	plan(fx.Takes["T3"])
	measured(fx.Takes["T3"])
	plan(fx.Takes["T4"])
	measured(fx.Takes["T4"])
	manual(fx.Takes["T4"])
	want := map[string]string{"T1": "plan", "T2": "manual", "T3": "measured", "T4": "manual", "T5": "", "T6": ""}
	for label, src := range want {
		id := fx.Takes[label]
		sec, err := svc.Sections(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		tm, err := library.Timing(ctx, st.R, id)
		if err != nil {
			t.Fatal(err)
		}
		if tm.Source != src || sec.Source != src || len(tm.Sections) != len(sec.Timeline) {
			t.Errorf("%s: Timing %q (%d rows), Sections %q (%d rows), want %q", label, tm.Source, len(tm.Sections), sec.Source, len(sec.Timeline), src)
			continue
		}
		for i, s := range tm.Sections {
			r := sec.Timeline[i]
			if s.Kind != r.Kind || s.Label != r.Label || s.StartS != r.StartS || s.EndS != r.EndS {
				t.Errorf("%s row %d: %+v vs %+v", label, i, s, r)
			}
		}
		if tm.DurationS <= 0 {
			t.Errorf("%s: duration %v", label, tm.DurationS)
		}
	}
	// ordinals count the occurrences of a kind
	tm, _ := library.Timing(ctx, st.R, fx.Takes["T1"])
	if got := []int{tm.Sections[0].Ordinal, tm.Sections[1].Ordinal, tm.Sections[2].Ordinal}; got[0] != 1 || got[1] != 1 || got[2] != 2 {
		t.Errorf("ordinals = %v", got)
	}
	// a transaction works too; an unknown track is ErrNotFound
	tx, err := st.R.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if tm, err := library.Timing(ctx, tx, fx.Takes["T2"]); err != nil || tm.Source != "manual" {
		t.Errorf("tx: %+v %v", tm, err)
	}
	if _, err := library.Timing(ctx, st.R, "nope"); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown track: %v", err)
	}
}
