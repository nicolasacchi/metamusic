package archivetest_test

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	code := m.Run()
	archivetest.Cleanup()
	os.Exit(code)
}

func count(t *testing.T, dir, q string) int {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(dir, "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var n int
	if err := st.R.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSyntheticArchive_ImportedOnce(t *testing.T) {
	t.Parallel()
	a := archivetest.SyntheticArchive(t, "acceptance")
	b := archivetest.SyntheticArchive(t, "acceptance")
	if a != b {
		t.Errorf("two calls returned different templates: %+v %+v", a, b)
	}
	if n := archivetest.ImportCount("acceptance"); n != 1 {
		t.Fatalf("imported %d times, want 1", n)
	}
	d1, d2 := a.Copy(t), b.Copy(t)
	if d1 == d2 {
		t.Fatal("copies share a directory")
	}
	tracks := count(t, d1, `SELECT COUNT(*) FROM tracks`)
	if tracks == 0 || count(t, d1, `SELECT COUNT(*) FROM recipe_drafts`) == 0 {
		t.Fatalf("empty import: %d tracks", tracks)
	}
	// each copy owns its database ...
	st, err := store.Open(context.Background(), filepath.Join(d1, "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.Exec(`UPDATE tracks SET title = 'Changed in copy one' WHERE id = (SELECT id FROM tracks LIMIT 1)`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if n := count(t, d2, `SELECT COUNT(*) FROM tracks WHERE title = 'Changed in copy one'`); n != 0 {
		t.Error("a write in one copy reached the other")
	}
	if n := count(t, a.Copy(t), `SELECT COUNT(*) FROM tracks WHERE title = 'Changed in copy one'`); n != 0 {
		t.Error("a write reached the template")
	}
	// ... and shares the frozen masters by hardlink
	st2, err := store.Open(context.Background(), filepath.Join(d2, "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	var master string
	if err := st2.R.QueryRow(`SELECT master_path FROM tracks LIMIT 1`).Scan(&master); err != nil {
		t.Fatal(err)
	}
	st2.Close()
	fi, err := os.Stat(filepath.Join(d2, master))
	if err != nil {
		t.Fatal(err)
	}
	if nl := fi.Sys().(*syscall.Stat_t).Nlink; nl < 2 {
		t.Errorf("master has %d links, want a hardlink", nl)
	}
	if n := archivetest.ImportCount("e2e"); n != 0 {
		t.Errorf("e2e imported without being asked: %d", n)
	}
}
