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

// TestGenerateArchive_SevenMothers (heavy): 7 backlog drafts whose mothers are 7 distinct lineage roots.
func TestGenerateArchive_SevenMothers(t *testing.T) {
	t.Parallel()
	a := archivetest.GenerateArchive(t)
	if b := archivetest.GenerateArchive(t); a != b {
		t.Errorf("two calls returned different templates")
	}
	d1, d2 := a.Copy(t), a.Copy(t)
	if d1 == d2 {
		t.Fatal("copies share a directory")
	}
	if n := count(t, d1, `SELECT COUNT(*) FROM recipe_drafts WHERE status = 'backlog' AND parent_track_id IS NOT NULL`); n != 7 {
		t.Errorf("backlog drafts with a mother = %d, want 7", n)
	}
	if n := count(t, d1, `SELECT COUNT(DISTINCT parent_track_id) FROM recipe_drafts WHERE status = 'backlog' AND parent_track_id IS NOT NULL`); n != 7 {
		t.Errorf("distinct mothers = %d, want 7", n)
	}
	if n := count(t, d1, `SELECT COUNT(*) FROM recipe_drafts d JOIN tracks t ON t.id = d.parent_track_id WHERE d.status = 'backlog' AND t.id = t.root_track_id`); n != 7 {
		t.Errorf("mothers that are lineage roots = %d, want 7", n)
	}
	// the base acceptance template is untouched
	base := archivetest.SyntheticArchive(t, "acceptance").Copy(t)
	if n := count(t, base, `SELECT COUNT(*) FROM recipe_drafts WHERE parent_track_id IS NOT NULL`); n != 1 {
		t.Errorf("acceptance template drafts with a mother = %d, want 1 (the varianti draft)", n)
	}
	// a copy is private
	st, err := store.Open(context.Background(), filepath.Join(d1, "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.Exec(`UPDATE recipe_drafts SET status = 'archived' WHERE status = 'backlog'`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if n := count(t, d2, `SELECT COUNT(*) FROM recipe_drafts WHERE status = 'backlog'`); n < 7 {
		t.Errorf("a copy changed another copy: %d backlog drafts", n)
	}
}
