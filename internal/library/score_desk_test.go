package library_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// TestLibrary_ScoreABC: the reader returns the stored score.abc text with its recorded hash, answers "no score" for a take without
// a scores row, and refuses a file that is missing or no longer matches its hash.
func TestLibrary_ScoreABC(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	var id string
	if err := e.st.R.QueryRow(`SELECT id FROM tracks WHERE id NOT IN (SELECT track_id FROM scores) ORDER BY id LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := e.svc.ScoreABC(ctx, id); ok || err != nil {
		t.Fatalf("a take without a score: ok=%v err=%v", ok, err)
	}
	if _, _, ok, err := e.svc.ScoreABC(ctx, "no-such-take"); ok || err != nil {
		t.Fatalf("an unknown take: ok=%v err=%v", ok, err)
	}

	text := []byte("X:1\nT:synthetic\nM:4/4\nL:1/4\nK:C\nCDEF|\n")
	sum := sha256.Sum256(text)
	want := hex.EncodeToString(sum[:])
	rel := "takes/" + id + "/score.abc"
	if err := os.MkdirAll(filepath.Join(e.dir, "takes", id), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.dir, rel), text, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.W.Exec(`INSERT INTO scores(track_id, abc_path, abc_sha256, bars, meter, q, bpm, seconds_per_bar, aligned, source, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`, id, rel, want, 1, "4/4", nil, nil, nil, 0, "job", "2026-01-01T00:00:00.000Z"); err != nil {
		t.Fatal(err)
	}
	got, sha, ok, err := e.svc.ScoreABC(ctx, id)
	if err != nil || !ok || string(got) != string(text) || sha != want {
		t.Fatalf("ScoreABC = %q %s %v %v", got, sha, ok, err)
	}

	// a file that changed under the recorded hash is corruption, never stale data
	if err := os.WriteFile(filepath.Join(e.dir, rel), []byte("X:1\nK:C\nD|\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := e.svc.ScoreABC(ctx, id); ok || err == nil {
		t.Errorf("a changed file: ok=%v err=%v", ok, err)
	}
	if err := os.Remove(filepath.Join(e.dir, rel)); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := e.svc.ScoreABC(ctx, id); ok || err == nil {
		t.Errorf("a missing file: ok=%v err=%v", ok, err)
	}
}
