package app_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestWireDesk_RegisteredAtInit: a real Build serves the Desk (the build hook is registered at init).
func TestWireDesk_RegisteredAtInit(t *testing.T) {
	t.Parallel()
	s, _ := build(t, app.Options{})
	if s.Deps.Desk == nil {
		t.Fatal("Build left Deps.Desk nil")
	}
}

// TestWireDesk_SweepsOrphansAtStart: a plan directory without a row that is older than the grace period is removed by Build; a
// young one (another process may be about to store its row) and a Build without a data root change nothing.
func TestWireDesk_SweepsOrphansAtStart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, p := range []string{"plans/old-orphan", "plans/young-orphan"} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "plans/old-orphan"), old, old); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), dir+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	cfg := config.Config{AuthMode: "local", PublicURL: "http://mm.example.com", RateScale: 100, DataDir: dir}
	s, err := app.Build(context.Background(), cfg, config.Secrets{Key: key}, st, root, app.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Deps.Desk == nil {
		t.Error("Deps.Desk is nil")
	}
	if _, err := os.Stat(filepath.Join(dir, "plans/old-orphan")); err == nil {
		t.Error("the old orphan survived the start-up sweep")
	}
	if _, err := os.Stat(filepath.Join(dir, "plans/young-orphan")); err != nil {
		t.Errorf("the young orphan was removed: %v", err)
	}
}
