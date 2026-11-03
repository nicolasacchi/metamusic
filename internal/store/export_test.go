package store

import (
	"context"
	"database/sql"
)

// MigrateTo is the test hook for partial migration (upgrade tests).
var MigrateTo = migrateTo

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
