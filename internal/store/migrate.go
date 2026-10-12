package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type migration struct {
	n    int
	name string
	sql  string
}

func loadMigrations() ([]migration, error) {
	ents, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, e := range ents {
		numStr, _, ok := strings.Cut(e.Name(), "_")
		n, perr := strconv.Atoi(numStr)
		if !ok || perr != nil || !strings.HasSuffix(e.Name(), ".sql") {
			return nil, fmt.Errorf("store: bad migration filename %q", e.Name())
		}
		b, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{n, e.Name(), string(b)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].n < ms[j].n })
	for i, m := range ms {
		if m.n != i+1 {
			return nil, fmt.Errorf("store: migration numbering gap at %s", m.name)
		}
	}
	return ms, nil
}

// Migrate applies pending migrations, each in its own transaction, bumping
// PRAGMA user_version after each. db must be the single-connection writer.
func Migrate(ctx context.Context, db *sql.DB) error {
	return migrateTo(ctx, db, -1)
}

// migrateTo applies pending migrations up to and including migration n
// (n < 0 means all). A database newer than the binary is always refused.
func migrateTo(ctx context.Context, db *sql.DB, n int) error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	var cur int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&cur); err != nil {
		return fmt.Errorf("store: read user_version: %w", err)
	}
	if cur > len(ms) {
		return fmt.Errorf("store: database schema version %d is newer than this binary (%d)", cur, len(ms))
	}
	if n < 0 || n > len(ms) {
		n = len(ms)
	}
	if cur >= n {
		return nil
	}
	for _, m := range ms[cur:n] {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: migration %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.n)); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: migration %s: set version: %w", m.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: migration %s: commit: %w", m.name, err)
		}
	}
	return nil
}
