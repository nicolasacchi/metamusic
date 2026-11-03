package librarytest

import (
	"database/sql"
	"strings"
	"testing"
)

func t1Scalar(t *testing.T, db interface {
	QueryRow(string, ...any) *sql.Row
}, q string, args ...any) string {
	t.Helper()
	var v sql.NullString
	if err := db.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v.String
}

// TestFixtureT1_Shape: the rows of the T1 world by SQL, including every real-data shape L3-X2 found missing: draft-only Songs,
// a reference Take, a lossy import, a multi-part import and Takes with and without features.
func TestFixtureT1_Shape(t *testing.T) {
	t.Parallel()
	st := openL1(t)
	fx := FixtureT1(t, st, FixedNow)
	if len(fx.Tx) != 10 || len(fx.Parts) != 3 {
		t.Fatalf("takes %d parts %d", len(fx.Tx), len(fx.Parts))
	}
	atLeast := func(q string, minRows int) {
		t.Helper()
		var n int
		if err := st.R.QueryRow(q).Scan(&n); err != nil || n < minRows {
			t.Errorf("%s = %d (%v), want at least %d", q, n, err, minRows)
		}
	}
	atLeast(`SELECT COUNT(*) FROM songs WHERE origin = 'draft'`, 5)                           // draft-only Songs (L3: d1-d4, u2)
	atLeast(`SELECT COUNT(*) FROM tracks WHERE import_rule = 'reference' AND lossy = 0`, 1)   // a reference FLAC
	atLeast(`SELECT COUNT(*) FROM tracks WHERE import_rule = 'reference' AND lossy = 1`, 1+3) // the lossy import and the trio
	// L3's world is intact: draft-only Songs, the reference FLAC and the lossy reference
	for q, want := range map[string]string{
		`SELECT COUNT(*) FROM tracks WHERE title LIKE 'Synthetic Take X%'`:                                    "10",
		`SELECT COUNT(*) FROM tracks WHERE transform_id IS NOT NULL`:                                          "0",
		`SELECT COUNT(*) FROM transforms`:                                                                     "0",
		`SELECT COUNT(*) FROM transform_inputs`:                                                               "0",
		`SELECT COUNT(*) FROM tracks WHERE title LIKE 'Synthetic Trio X p_'`:                                  "3",
		`SELECT COUNT(*) FROM tracks WHERE lossy = 1 AND generation = 1 AND title LIKE 'Synthetic Trio X p_'`: "2",
	} {
		if got := t1Scalar(t, st.R, q); got != want {
			t.Errorf("%s = %s, want %s", q, got, want)
		}
	}
	// A and B have equal lyrics and different styles; I and WS are the instrumental shapes
	a := t1Scalar(t, st.R, `SELECT r.lyrics FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE t.id = ?`, fx.Tx["A"])
	b := t1Scalar(t, st.R, `SELECT r.lyrics FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE t.id = ?`, fx.Tx["B"])
	if a != T1Lyrics || a != b {
		t.Errorf("lyrics of A and B: %q and %q", a, b)
	}
	if got := t1Scalar(t, st.R, `SELECT r.style FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE t.id = ?`, fx.Tx["A"]); got != "reggae, 92 bpm" {
		t.Errorf("style of A = %q", got)
	}
	if got := t1Scalar(t, st.R, `SELECT bpm_declared FROM tracks WHERE id = ?`, fx.Tx["A"]); got != "92" {
		t.Errorf("bpm_declared of A = %q", got)
	}
	if got := t1Scalar(t, st.R, `SELECT r.lyrics FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE t.id = ?`, fx.Tx["I"]); got != "" {
		t.Errorf("instrumental lyrics = %q", got)
	}
	if got := t1Scalar(t, st.R, `SELECT r.lyrics FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE t.id = ?`, fx.Tx["WS"]); strings.TrimSpace(got) != "" || got == "" {
		t.Errorf("whitespace-only lyrics = %q", got)
	}
	// B has a stored tempo analysis of 128, confidence 0.8, not octave ambiguous
	m := t1Scalar(t, st.R, `SELECT metrics_json FROM analyses WHERE track_id = ? AND analyzer = 'tempo'`, fx.Tx["B"])
	if !strings.Contains(m, `"bpm":128.0`) || !strings.Contains(m, `"confidence":0.8`) || !strings.Contains(m, `"octave_ambiguous":false`) {
		t.Errorf("tempo analysis of B = %s", m)
	}
	// S has a score and an aligned scores row with a bar grid
	if got := t1Scalar(t, st.R, `SELECT aligned || ':' || (seconds_per_bar IS NOT NULL) FROM scores WHERE track_id = ?`, fx.Tx["S"]); got != "1:1" {
		t.Errorf("scores row of S = %q, want aligned with seconds_per_bar", got)
	}
	// features: A declared, OCT with bpm_octave set, and every other T1 take (NF included) has no row
	if got := t1Scalar(t, st.R, `SELECT bpm_basis FROM take_features WHERE track_id = ?`, fx.Tx["A"]); got != "declared" {
		t.Errorf("A basis = %q", got)
	}
	if got := t1Scalar(t, st.R, `SELECT bpm_octave || ':' || bpm_measured_x100 FROM take_features WHERE track_id = ?`, fx.Tx["OCT"]); got != "1:6400" {
		t.Errorf("OCT feature row = %q", got)
	}
	for _, label := range []string{"B", "I", "WS", "S", "NF", "MONO", "WIDE", "OOP"} {
		if got := t1Scalar(t, st.R, `SELECT COUNT(*) FROM take_features WHERE track_id = ?`, fx.Tx[label]); got != "0" {
			t.Errorf("%s has %s feature rows, want none", label, got)
		}
	}
	// every lossless T1 Take is a valid transform input (24-bit stereo, lossless, a known recipe); the trio is not
	if got := t1Scalar(t, st.R, `SELECT COUNT(*) FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE t.title LIKE 'Synthetic Take X%' AND t.lossy = 0 AND t.import_rule <> 'reference' AND r.engine = 'yue2' AND t.bits_per_sample = 24 AND t.channels = 2`); got != "10" {
		t.Errorf("valid inputs = %s, want 10", got)
	}
	// the migration's input trigger accepts a lossless T1 Take and refuses the lossy and the reference ones
	for _, c := range []struct {
		id string
		ok bool
	}{{fx.Tx["A"], true}, {fx.Parts[0], false}, {TrackRefFlac, false}, {TrackLossy, false}} {
		var sha string
		if err := st.R.QueryRow(`SELECT master_sha256 FROM tracks WHERE id = ?`, c.id).Scan(&sha); err != nil {
			t.Fatal(err)
		}
		tx := begin(t, st)
		_, err := tx.Exec(`INSERT INTO recipes(id,engine,request_id,request_json,style,lyrics,seed,cot,sampling_json,hash,core_hash,created_by,created_at)
			VALUES('r-fx','dsp','dsp-x','{"id":"dsp-x","seed":0,"v":1,"kind":"glacier","dsp_version":"mm-dsp/1"}','','',0,'',NULL,?,?,'agent:x',?)`, Sha("fx-hash"), Sha("fx-hash"), "2026-11-03T00:00:00.000Z")
		if err == nil {
			_, err = tx.Exec(`INSERT INTO transforms(id,kind,recipe_id,spec_hash,title,principal_id,principal_kind,principal_name,state,expected_cpu_ms,max_cpu_ms,out_seconds_est,mem_mb_est,created_at)
				VALUES('x-fx','glacier','r-fx',?,'T','agent:x','agent','x','queued',1,1,1.0,1,?)`, Sha("fx-spec"), "2026-11-03T00:00:00.000Z")
		}
		if err == nil {
			_, err = tx.Exec(`INSERT INTO transform_inputs(transform_id,ord,role,track_id,master_sha256) VALUES('x-fx',0,'source',?,?)`, c.id, sha)
		}
		if (err == nil) != c.ok {
			t.Errorf("transform input %s: err=%v, want ok=%v", c.id, err, c.ok)
		}
		_ = tx.Rollback()
	}
}
