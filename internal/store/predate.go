package store

import (
	"context"
	"database/sql"
	"fmt"
)

// CreateBefore creates a brand-new database at path migrated only up to the migration just BEFORE the embedded file migration
// (for example "0023_upload_fallback.sql"), then closes it. It is the supported way for a test outside this package to get an
// older-schema database: seed what the test needs, close it, then store.Open migrates it forward. Never fake an older schema by
// opening the latest one, dropping objects and lowering PRAGMA user_version: every migration added later re-runs on objects that
// still exist (TestNoUserVersionLiteralOutsideStore guards this). Test-oriented; production code has no use for it.
func CreateBefore(ctx context.Context, path, migration string) error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	n := 0
	for _, m := range ms {
		if m.name == migration {
			n = m.n
		}
	}
	if n == 0 {
		return fmt.Errorf("store: no embedded migration %q", migration)
	}
	w, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		return fmt.Errorf("store: open writer: %w", err)
	}
	defer w.Close()
	w.SetMaxOpenConns(1)
	return migrateTo(ctx, w, n-1)
}
