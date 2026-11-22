package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

func putFile(t *testing.T, data, rel, content string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	if err := os.MkdirAll(filepath.Dir(filepath.Join(data, rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, rel), []byte(content), 0o444); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(sum[:])
}

// deskPlanRow inserts an edit plan whose score file is plans/<id>/score.abc (the plan of the take is its parent).
func deskPlanRows(t *testing.T, e *env, planID, sha string, bytes int) {
	t.Helper()
	const q = `INSERT INTO desk_plans(id, source, track_id, engine, request_json, seed, cot, score_path, score_sha256, score_bytes, dialect, bars, meter,
		aligned, sections_json, expected_audio_s, title, principal_id, principal_kind, principal_name, created_at)
		VALUES (?, 'take', ?, 'yue2', '{"id":"r","seed":1}', 1, 'full', ?, ?, ?, 'yue2', 6, '4/4', 1, '[]', 12, 'Plan', 'human:alice', 'human', 'alice', '2026-01-01T00:00:00Z')`
	if _, err := e.st.W.Exec(q, planID, lt.TrackLuna, "plans/"+planID+"/score.abc", sha, bytes); err != nil {
		t.Fatal(err)
	}
}

// TestBackup_IncludesPlans (C357.3): the files of a plan (desk_plan_files) are part of a snapshot, linked and hashed, and come back from
// a restore with mode 0444. The score text is synthetic.
func TestBackup_IncludesPlans(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	const text = "X:1\nT:synthetic plan\nK:C\nCDEF|\n"
	rel := "plans/p1/score.abc"
	sha := putFile(t, e.data, rel, text)
	deskPlanRows(t, e, "p1", sha, len(text))
	if _, err := e.st.W.Exec(`INSERT INTO desk_plan_files(plan_id, name, sha256, bytes, path, created_at) VALUES ('p1', 'score.abc', ?, ?, ?, '2026-01-01T00:00:00Z')`, sha, len(text), rel); err != nil {
		t.Fatal(err)
	}
	_, m, dir := e.run(t)
	var got *ManifestFile
	for i := range m.Files {
		if m.Files[i].Path == rel {
			got = &m.Files[i]
		}
	}
	if got == nil || got.Kind != "artifact" || got.SHA256 != sha || !got.Linked {
		t.Fatalf("plan file in manifest: %+v", got)
	}
	into := filepath.Join(t.TempDir(), "restored")
	if _, err := Restore(context.Background(), dir, into, restoreOpts(e)); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(into, rel))
	if err != nil || fi.Mode().Perm() != 0o444 {
		t.Errorf("%s restored: %v %v", rel, fi, err)
	}
	if b, _ := os.ReadFile(filepath.Join(into, rel)); string(b) != text {
		t.Errorf("restored bytes %q", b)
	}
}

// TestBackup_SkipsAuditions (C357.3): an audition MP3 is derived and rebuilt on request, so no snapshot holds it and a restore
// without it is complete, even when the file is missing.
func TestBackup_SkipsAuditions(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	sha := putFile(t, e.data, "plans/p1/score.abc", "X:1\nK:C\nC|\n")
	deskPlanRows(t, e, "p1", sha, 12)
	mp3 := "derived/auditions/ab/" + sha + ".mp3"
	mp3sha := putFile(t, e.data, mp3, "synthetic mp3 bytes")
	insert := func(id, path, h string) {
		t.Helper()
		if _, err := e.st.W.Exec(`INSERT INTO desk_auditions(id, plan_id, spec_hash, soundfont, soundfont_sha256, synth_version, chords, mp3_path, mp3_sha256, mp3_bytes,
			pcm_sha256, seconds, cpu_ms, report_json, principal_id, created_at) VALUES (?, 'p1', ?, 'gm', ?, '2.3', 'block', ?, ?, 19, ?, 12.5, 5, '{}', 'human:alice', '2026-01-01T00:00:00Z')`,
			id, h, sha, path, h, sha); err != nil {
			t.Fatal(err)
		}
	}
	insert("a1", mp3, mp3sha)
	insert("a2", "derived/auditions/cd/gone.mp3", hex.EncodeToString(make([]byte, 32))) // a row whose file verify removed
	_, m, dir := e.run(t)
	for _, f := range m.Files {
		if f.Path == mp3 || f.Path == "derived/auditions/cd/gone.mp3" {
			t.Errorf("snapshot holds %s", f.Path)
		}
	}
	into := filepath.Join(t.TempDir(), "restored")
	if _, err := Restore(context.Background(), dir, into, restoreOpts(e)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(into, mp3)); err == nil {
		t.Error("a restore brought back an audition MP3")
	}
}
