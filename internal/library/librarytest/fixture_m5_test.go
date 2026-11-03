package librarytest

import (
	"context"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library"
)

// TestFixtureM5_Shape: the C264 world by SQL and through library.Timing; the base layers keep their shapes.
func TestFixtureM5_Shape(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openL1(t)
	fx := FixtureM5(t, st, FixedNow)
	// real-world shapes from the lower layers: draft-only Songs, reference and lossy imports, multi-part groups, Songs with and without Picks
	if n := count(t, st, `SELECT COUNT(*) FROM songs WHERE origin = 'draft'`); n < 5 {
		t.Errorf("draft-only Songs = %d", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM tracks WHERE lossy = 1`); n < 3 {
		t.Errorf("lossy references = %d", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM tracks WHERE lossy = 1 AND parent_track_id IS NOT NULL`); n < 2 {
		t.Errorf("multi-part imports (p2/p3 of a lossy group) = %d", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM song_picks`); n < 8 {
		t.Errorf("Songs with Picks = %d", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM songs s WHERE NOT EXISTS (SELECT 1 FROM song_picks p WHERE p.song_id = s.id)`); n < 5 {
		t.Errorf("Songs without Picks = %d", n)
	}
	// the lineage: three generations of known recipes, comments on the grandparent
	for g, id := range fx.Lineage {
		if n := count(t, st, `SELECT COUNT(*) FROM tracks WHERE id = ? AND generation = ? AND root_track_id = ?`, id, g, fx.Lineage[0]); n != 1 {
			t.Errorf("G%d: %d rows", g, n)
		}
	}
	if len(fx.Comments) != 6 {
		t.Fatalf("comments = %v", fx.Comments)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM comments WHERE track_id = ? AND hidden_at IS NOT NULL`, fx.Lineage[0]); n != 1 {
		t.Errorf("hidden comments = %d", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM comments WHERE track_id = ? AND kind = 'fix' AND state = 'open' AND hidden_at IS NULL AND at_second IS NOT NULL`, fx.Lineage[0]); n != 2 {
		t.Errorf("open timestamped fixes = %d", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM comments WHERE track_id IN (?, ?)`, fx.Lineage[1], fx.Lineage[2]); n != 0 {
		t.Errorf("descendants carry %d comments", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM listen_events WHERE track_id = ? AND source = 'subsonic'`, fx.Lineage[0]); n != 2 {
		t.Errorf("subsonic events = %d", n)
	}
	// section shapes through library.Timing
	svc := library.New(st, nil, func() time.Time { return FixedNow })
	_ = svc
	for _, tc := range []struct {
		name, id, source string
		rows             int
	}{
		{"G0 plan", fx.Lineage[0], "plan", 5}, {"G1 manual beats plan", fx.Lineage[1], "manual", 5}, {"G2 lyrics only", fx.Lineage[2], "", 0},
		{"T1 plan", fx.Takes["T1"], "plan", 5}, {"T2 manual", fx.Takes["T2"], "manual", 2}, {"T3 measured", fx.Takes["T3"], "measured", 2},
		{"T5 lyrics only", fx.Takes["T5"], "", 0}, {"reference original", TrackRefFlac, "", 0},
	} {
		tm, err := library.Timing(ctx, st.R, tc.id)
		if err != nil || tm.Source != tc.source || len(tm.Sections) != tc.rows {
			t.Errorf("%s: %q %d rows (%v), want %q %d", tc.name, tm.Source, len(tm.Sections), err, tc.source, tc.rows)
		}
	}
	// the recipes of the lineage are known and the lossy reference's is a stub
	if n := count(t, st, `SELECT COUNT(*) FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE t.id = ? AND r.engine = 'yue2'`, fx.Lineage[2]); n != 1 {
		t.Errorf("G2 recipe engine rows = %d", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE t.id = ? AND r.engine <> 'yue2'`, TrackLossy); n != 1 {
		t.Errorf("lossy reference recipe engine rows = %d", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Errorf("foreign_key_check rows = %d", n)
	}
	for _, tb := range []string{"patches", "patch_comments", "callbacks", "callback_verdicts", "voice_notes"} {
		if n := count(t, st, `SELECT COUNT(*) FROM `+tb); n != 0 {
			t.Errorf("the fixture wrote %d rows to %s", n, tb)
		}
	}
}

// TestApplyM5_MatchesFixture: ApplyM5 on a bare store builds the same M5 world on top of ApplyL3.
func TestApplyM5_MatchesFixture(t *testing.T) {
	t.Parallel()
	st := openL1(t)
	FixtureM3(t, st, FixedNow)
	ApplyM5(t, st, FixedNow)
	if n := count(t, st, `SELECT COUNT(*) FROM tracks WHERE title LIKE 'Synthetic Lineage G_'`); n != 3 {
		t.Errorf("lineage takes = %d", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM sections WHERE source = 'plan' AND track_id IN (SELECT id FROM tracks WHERE title = 'Synthetic Take T1')`); n != 5 {
		t.Errorf("T1 plan sections = %d", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM comments WHERE body = 'the chorus lands late'`); n != 1 {
		t.Errorf("comments = %d", n)
	}
}
