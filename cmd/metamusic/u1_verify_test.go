package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/media/flacmeta/flactest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestCLI_LibraryVerifyRenditions: `library verify` checks the Listener renditions (C308.8). A missing file is a warning and
// the exit stays 0; a file with an MM_* tag is an error (exit 1) and is removed so it is never served; the master is untouched.
func TestCLI_LibraryVerifyRenditions(t *testing.T) {
	t.Parallel()
	env, fx := l2Env(t)
	if code, out, errs := do(t, env, "", "library", "reconcile"); code != 0 {
		t.Fatalf("reconcile: %d %q %q", code, out, errs)
	}
	st, err := store.Open(context.Background(), filepath.Join(env["MM_DATA_DIR"], "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	var master, a, b string
	rows, err := st.R.Query(`SELECT id, master_path FROM tracks ORDER BY id LIMIT 2`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, p string
		if err := rows.Scan(&id, &p); err != nil {
			t.Fatal(err)
		}
		if a == "" {
			a, master = id, p
		} else {
			b = id
		}
	}
	_ = rows.Close()
	_ = st.Close()
	if a == "" || b == "" || len(fx.Records) == 0 {
		t.Fatalf("fixture tracks: %q %q", a, b)
	}
	leaky := flactest.Build(flactest.Spec{TotalSamples: 96000, Vendor: "metamusic mm-clean/1", Comments: []string{"TITLE=x", "MM_SEED=9"}, Frames: []byte{1, 2, 3}})
	h := sha256.Sum256(leaky)
	sum := hex.EncodeToString(h[:])
	rel := "derived/" + sum[:2] + "/" + sum + ".flac"
	full := filepath.Join(env["MM_DATA_DIR"], filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, leaky, 0o444); err != nil {
		t.Fatal(err)
	}
	masterFull := filepath.Join(env["MM_DATA_DIR"], filepath.FromSlash(master))
	before, _ := os.ReadFile(masterFull)
	ins := `INSERT INTO renditions(track_id, kind, sha256, bytes, path, params_json, created_at) VALUES (?, ?, ?, ?, ?, '{}', '2026-10-06T00:00:00Z')`
	exec := func(q string, args ...any) {
		st, err := store.Open(context.Background(), filepath.Join(env["MM_DATA_DIR"], "metamusic.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		if _, err := st.W.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	// a row without a file is a warning
	exec(ins, b, "mp3_320_clean", strings.Repeat("0", 64), 1, "derived/00/missing.mp3")
	code, out, errs := do(t, env, "", "library", "verify")
	if code != 0 || !strings.Contains(out, "warning rendition_missing "+b+"/mp3_320_clean") {
		t.Fatalf("missing file: %d %q %q", code, out, errs)
	}
	exec(ins, a, "flac_clean", sum, len(leaky), rel)
	code, out, errs = do(t, env, "", "library", "verify")
	if code != 1 || !strings.Contains(out, "error rendition_tag_leak "+a+"/flac_clean") || strings.Contains(out, "MM_SEED=9") {
		t.Fatalf("leak: %d %q %q", code, out, errs)
	}
	if _, err := os.Stat(full); err == nil {
		t.Errorf("the leaking file was not removed")
	}
	if after, _ := os.ReadFile(masterFull); string(after) != string(before) {
		t.Errorf("a master changed")
	}
	// the file is gone, so the next verify only warns
	if code, out, errs = do(t, env, "", "library", "verify"); code != 0 || !strings.Contains(out, "warning rendition_missing "+a+"/flac_clean") {
		t.Fatalf("after removal: %d %q %q", code, out, errs)
	}
}
