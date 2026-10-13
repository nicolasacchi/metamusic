package store

import (
	"context"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

// Test-only fast path. Applying every migration from zero costs hundreds of milliseconds per
// database under -race, which dominated the suite. UseMigratedTemplateForTests makes Open seed a
// brand-new database file from an image migrated once per process; Open then still runs Migrate
// (a no-op at the current version), so schema, pragmas and pools are identical to production.
// The store package's own migration tests do not opt in and keep exercising migrations from zero.
var (
	templateEnabled atomic.Bool
	templateOnce    sync.Once
	templateImage   []byte
)

// UseMigratedTemplateForTests enables the per-process migrated template for Open.
// Call it from TestMain or a test helper; never from production code.
func UseMigratedTemplateForTests() { templateEnabled.Store(true) }

func seedFromTemplate(ctx context.Context, path string) {
	if path == "" || strings.HasPrefix(path, ":") || strings.HasPrefix(path, "file:") || strings.Contains(path, "mode=memory") {
		return
	}
	if _, err := os.Stat(path); err == nil || !os.IsNotExist(err) {
		return // existing database: let Open migrate it for real
	}
	templateOnce.Do(func() { templateImage = buildTemplate(ctx) })
	if templateImage == nil {
		return // fall back to migrating from zero
	}
	_ = os.WriteFile(path, templateImage, 0o600) // on failure Open migrates from zero
}

func buildTemplate(ctx context.Context) []byte {
	dir, err := os.MkdirTemp("", "mm-store-template-")
	if err != nil {
		return nil
	}
	defer func() { _ = os.RemoveAll(dir) }()
	p := dir + "/t.db"
	st, err := openRaw(ctx, p)
	if err != nil {
		return nil
	}
	if _, err := st.W.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		_ = st.Close()
		return nil
	}
	if err := st.Close(); err != nil {
		return nil
	}
	b, err := os.ReadFile(p) //nolint:gosec // path inside our own temp dir
	if err != nil {
		return nil
	}
	return b
}
