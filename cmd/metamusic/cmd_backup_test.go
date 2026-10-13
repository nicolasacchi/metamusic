package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// backupEnv is a data directory holding the M2a fixture library with its files and a separate backup directory.
func backupEnv(t *testing.T) map[string]string {
	t.Helper()
	env := testEnv(t, "proxy")
	st, err := store.Open(context.Background(), filepath.Join(env["MM_DATA_DIR"], "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	lt.Fixture(t, st, lt.FixedNow)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	lt.WriteFiles(t, env["MM_DATA_DIR"])
	env["MM_BACKUP_DIR"] = filepath.Join(t.TempDir(), "backup")
	return env
}

func snapshotsIn(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func TestCLI_BackupList(t *testing.T) {
	t.Parallel()
	env := backupEnv(t)
	code, out, errs := do(t, env, "", "backup", "list")
	if code != 0 || !strings.Contains(out, "0 snapshot(s)") || !strings.Contains(out, "1 recent run(s)") {
		t.Fatalf("empty list: %d %q %q", code, out, errs)
	}
	if code, out, errs := do(t, env, "", "backup"); code != 0 || !strings.Contains(out, "snapshot ") || !strings.Contains(out, "linked") {
		t.Fatalf("backup: %d %q %q", code, out, errs)
	}
	snaps := snapshotsIn(t, env["MM_BACKUP_DIR"])
	if len(snaps) != 1 {
		t.Fatalf("snapshots on disk: %v", snaps)
	}
	code, out, errs = do(t, env, "", "backup", "list")
	if code != 0 || !strings.Contains(out, "1 snapshot(s)") || !strings.Contains(out, snaps[0]) || !strings.Contains(out, "completed") || !strings.Contains(out, "cli") {
		t.Fatalf("list: %d %q %q", code, out, errs)
	}
	if strings.Contains(out, env["MM_BACKUP_DIR"]) {
		t.Error("list prints the backup directory")
	}
}

func TestCLI_BackupTo(t *testing.T) {
	t.Parallel()
	env := backupEnv(t)
	to := filepath.Join(t.TempDir(), "elsewhere")
	if code, out, errs := do(t, env, "", "backup", "--to", to); code != 0 {
		t.Fatalf("%d %q %q", code, out, errs)
	}
	if len(snapshotsIn(t, to)) != 1 {
		t.Fatal("no snapshot in --to")
	}
	if _, err := os.Stat(env["MM_BACKUP_DIR"]); err == nil {
		t.Error("the default backup directory was used")
	}
	inside := filepath.Join(env["MM_DATA_DIR"], "bk")
	if code, _, errs := do(t, env, "", "backup", "--to", inside); code != 2 || !strings.Contains(errs, "outside the data directory") {
		t.Fatalf("--to inside the data dir: %d %q", code, errs)
	}
	if code, _, _ := do(t, env, "", "backup", "extra"); code != 2 {
		t.Fatalf("extra argument: %d", code)
	}
}

func TestCLI_BackupNoDatabase(t *testing.T) {
	t.Parallel()
	env := testEnv(t, "proxy")
	if code, _, errs := do(t, env, "", "backup"); code != 2 || !strings.Contains(errs, "no database") {
		t.Fatalf("%d %q", code, errs)
	}
}

func TestCLI_Restore(t *testing.T) {
	t.Parallel()
	env := backupEnv(t)
	if code, _, errs := do(t, env, "", "backup"); code != 0 {
		t.Fatalf("backup: %d %q", code, errs)
	}
	snap := filepath.Join(env["MM_BACKUP_DIR"], snapshotsIn(t, env["MM_BACKUP_DIR"])[0])
	into := filepath.Join(t.TempDir(), "restored")

	if code, _, errs := do(t, nil, "", "restore", "--from", snap); code != 2 || !strings.Contains(errs, "--into") {
		t.Fatalf("missing flag: %d %q", code, errs)
	}
	code, out, errs := do(t, nil, "", "restore", "--from", snap, "--into", into)
	if code != 0 || !strings.Contains(out, "MM_DATA_DIR="+into) || !strings.Contains(out, "tracks") {
		t.Fatalf("restore: %d %q %q", code, out, errs)
	}
	// a second restore into the now non-empty directory is refused with exit 2 and changes nothing
	before := snapshotsIn(t, into)
	if code, _, errs := do(t, nil, "", "restore", "--from", snap, "--into", into); code != 2 || !strings.Contains(errs, "not empty") {
		t.Fatalf("non-empty: %d %q", code, errs)
	}
	if after := snapshotsIn(t, into); strings.Join(before, ",") != strings.Join(after, ",") {
		t.Errorf("target changed: %v -> %v", before, after)
	}
	// a damaged snapshot file: exit 1 naming the path
	fresh := filepath.Join(t.TempDir(), "again")
	m := filepath.Join(snap, "db", "metamusic.db")
	if err := os.Chmod(m, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errs = do(t, nil, "", "restore", "--from", snap, "--into", fresh)
	if code != 1 || !strings.Contains(errs, "db/metamusic.db") {
		t.Fatalf("tampered: %d %q", code, errs)
	}
	if _, err := os.Stat(fresh); err == nil {
		t.Error("tampered restore wrote the target")
	}
}
