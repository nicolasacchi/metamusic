package drafts

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestDecorate_OneKnownHashQueryPerPage: a page of drafts costs one duplicate-check query.
func TestDecorate_OneKnownHashQueryPerPage(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	lt.Fixture(t, st, lt.FixedNow)
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	lib := library.New(st, root, func() time.Time { return lt.FixedNow })
	svc := New(st, lib, func() time.Time { return lt.FixedNow }, func() string { return fmt.Sprint(time.Now().UnixNano()) })
	page, err := lib.ListDrafts(context.Background(), library.DraftParams{Limit: 100})
	if err != nil || len(page.Items) < 6 {
		t.Fatalf("%d %v", len(page.Items), err)
	}
	if err := svc.Decorate(context.Background(), page.Items); err != nil {
		t.Fatal(err)
	}
	if n := svc.hashQueries.Load(); n != 1 {
		t.Errorf("KnownHash queries for %d drafts: %d, want 1", len(page.Items), n)
	}
}
