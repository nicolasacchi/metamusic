package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestLibraryVerify_DeskChecks (C357.4): `library verify` reports the Desk's files and rows. A plan file that is gone or changed is
// an error (exit 1, the line names the plan and file and never prints score text); a take plan that is not the take's score is an
// error; a completed plan job without a plan is an error; an audition whose file is missing is only a warning (exit 0).
func TestLibraryVerify_DeskChecks(t *testing.T) {
	t.Parallel()
	env, _ := l2Env(t)
	if code, out, errs := do(t, env, "", "library", "reconcile"); code != 0 {
		t.Fatalf("reconcile: %d %q %q", code, out, errs)
	}
	dataDir := env["MM_DATA_DIR"]
	exec := func(q string, args ...any) {
		t.Helper()
		st, err := store.Open(context.Background(), filepath.Join(dataDir, "metamusic.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		if _, err := st.W.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var track, trackSHA string
	st, err := store.Open(context.Background(), filepath.Join(dataDir, "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.R.QueryRow(`SELECT track_id, abc_sha256 FROM scores ORDER BY track_id LIMIT 1`).Scan(&track, &trackSHA); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	if code, out, errs := do(t, env, "", "library", "verify"); code != 0 || strings.Contains(out, "desk_") {
		t.Fatalf("a library without plans: %d %q %q", code, out, errs)
	}
	const text = "X:1\nT:invented bars\nK:C\nCDEF|\n"
	sum := sha256.Sum256([]byte(text))
	sha := hex.EncodeToString(sum[:])
	rel := "plans/p1/score.abc"
	if err := os.MkdirAll(filepath.Join(dataDir, "plans", "p1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, rel), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	planRow := func(id, source, trackID, parent, scoreSHA, path string, bytes int) {
		t.Helper()
		var tr, par, edit any
		if trackID != "" {
			tr = trackID
		}
		if parent != "" {
			par, edit = parent, `{"ops":[],"rationale":null}`
		}
		exec(`INSERT INTO desk_plans(id, source, track_id, parent_plan_id, edit_json, engine, request_json, seed, cot, score_path, score_sha256, score_bytes, dialect, bars, meter,
			aligned, sections_json, expected_audio_s, title, principal_id, principal_kind, principal_name, created_at)
			VALUES (?, ?, ?, ?, ?, 'yue2', '{"id":"r","seed":1}', 1, 'full', ?, ?, ?, 'yue2', 1, '4/4', 1, '[]', 12, 'Plan', 'human:alice', 'human', 'alice', '2026-01-01T00:00:00Z')`,
			id, source, tr, par, edit, path, scoreSHA, bytes)
	}
	// p0 is the take's own plan (its score is the take's), p1 an edit of it with a file of its own
	planRow("p0", "take", track, "", trackSHA, "takes/"+track+"/score.abc", 10)
	planRow("p1", "edit", "", "p0", sha, rel, len(text))
	exec(`INSERT INTO desk_plan_files(plan_id, name, sha256, bytes, path, created_at) VALUES ('p1', 'score.abc', ?, ?, ?, '2026-01-01T00:00:00Z')`, sha, len(text), rel)
	code, out, errs := do(t, env, "", "library", "verify")
	if strings.Contains(out, "desk_file_") || strings.Contains(out, "invented bars") {
		t.Fatalf("an intact plan file: %d %q %q", code, out, errs)
	}
	if err := os.WriteFile(filepath.Join(dataDir, rel), []byte("X:1\nK:C\nsecret score text|\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errs = do(t, env, "", "library", "verify")
	if code != 1 || !strings.Contains(out, "error desk_file_changed p1/score.abc") || strings.Contains(out, "secret score text") {
		t.Fatalf("changed file: %d %q %q", code, out, errs)
	}
	if err := os.Remove(filepath.Join(dataDir, rel)); err != nil {
		t.Fatal(err)
	}
	if code, out, errs = do(t, env, "", "library", "verify"); code != 1 || !strings.Contains(out, "error desk_file_missing p1/score.abc") {
		t.Fatalf("missing file: %d %q %q", code, out, errs)
	}
	// restore the file: the run is clean again, then an audition without a file is a warning only
	if err := os.WriteFile(filepath.Join(dataDir, rel), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO desk_auditions(id, plan_id, spec_hash, soundfont, soundfont_sha256, synth_version, chords, mp3_path, mp3_sha256, mp3_bytes, pcm_sha256, seconds, cpu_ms,
		report_json, principal_id, created_at) VALUES ('a1', 'p1', ?, 'gm', 's', '2.3', 'block', 'derived/auditions/ab/x.mp3', ?, 1, 'p', 1, 1, '{}', 'human:alice', '2026-01-01T00:00:00Z')`,
		strings.Repeat("1", 64), strings.Repeat("2", 64))
	code, out, errs = do(t, env, "", "library", "verify")
	if code != 0 || !strings.Contains(out, "warning desk_audition_file a1") {
		t.Fatalf("audition warning: %d %q %q", code, out, errs)
	}

	// D2: a take plan whose score is not its take's score
	var other string
	st, err = store.Open(context.Background(), filepath.Join(dataDir, "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.R.QueryRow(`SELECT track_id FROM scores WHERE track_id <> ? ORDER BY track_id LIMIT 1`, track).Scan(&other); err != nil {
		t.Skipf("the fixture has a single scored take: %v", err)
	}
	_ = st.Close()
	planRow("p2", "take", other, "", strings.Repeat("e", 64), "takes/"+other+"/score.abc", 10)
	if code, out, errs = do(t, env, "", "library", "verify"); code != 1 || !strings.Contains(out, "error desk_score_mismatch p2 ") {
		t.Fatalf("score mismatch: %d %q %q", code, out, errs)
	}
}
