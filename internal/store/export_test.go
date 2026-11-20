package store

import (
	"context"
	"database/sql"
)

// MigrateTo is the test hook for partial migration (upgrade tests). On a brand-new database it restores a per-process image
// of the schema at that version instead of re-applying every migration (see versionImage); on any other database it is
// migrateTo.
func MigrateTo(ctx context.Context, db *sql.DB, n int) error {
	if n > 0 {
		if ok, err := restoreFreshFromImage(ctx, db, n); err != nil {
			return err
		} else if ok {
			return nil
		}
	}
	return migrateTo(ctx, db, n)
}

// OpenRaw opens a bare single-connection writer pool without migrating.
func OpenRaw(path string) (*sql.DB, error) {
	w, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	return w, nil
}

// MigrationCount is the number of embedded migrations (the schema version of a fresh database).
func MigrationCount() int {
	ms, err := loadMigrations()
	if err != nil {
		panic(err)
	}
	return len(ms)
}

// TestMigration is a synthetic migration for directive tests.
type TestMigration struct {
	N         int
	Name, SQL string
}

// ApplyMigrations runs synthetic migrations through the real applier.
func ApplyMigrations(ctx context.Context, db *sql.DB, ms []TestMigration) error {
	var in []migration
	for _, m := range ms {
		in = append(in, migration{m.N, m.Name, m.SQL})
	}
	return applyMigrations(ctx, db, in)
}
