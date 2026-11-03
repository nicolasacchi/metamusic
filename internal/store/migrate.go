package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"regexp"
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
	return applyMigrations(ctx, db, ms[cur:n])
}

// fkOffDirective, as the exact first line of a migration file, runs that file
// with foreign key enforcement off (SQLite table rebuilds), guarded by a
// foreign_key_check before commit. Files without it are unaffected.
const fkOffDirective = "-- mm:foreign_keys=off"

func hasFKOffDirective(sqlText string) bool {
	first, _, _ := strings.Cut(sqlText, "\n")
	return strings.TrimSuffix(first, "\r") == fkOffDirective
}

func applyMigrations(ctx context.Context, db *sql.DB, ms []migration) error {
	for _, m := range ms {
		if hasFKOffDirective(m.sql) {
			if err := applyFKOff(ctx, db, m); err != nil {
				return err
			}
			continue
		}
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

// staleNewRef matches SQL that still references a "<name>_new" table, the
// symptom of a rebuild that renamed in the wrong order.
var staleNewRef = regexp.MustCompile("(?i)\\b(references|on|from|into|table|join|update)\\s+[\"'`\\[]?\\w+_new\\b")

// applyFKOff runs one directive migration on the single writer connection:
// foreign_keys=OFF outside the transaction, script, user_version,
// foreign_key_check (must be empty) and a rename guard before COMMIT, then
// foreign_keys=ON again whatever happened.
func applyFKOff(ctx context.Context, db *sql.DB, m migration) (err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return fmt.Errorf("store: migration %s: foreign_keys=off: %w", m.name, err)
	}
	defer func() {
		// WithoutCancel: enforcement must come back even if ctx is done.
		if _, perr := conn.ExecContext(context.WithoutCancel(ctx), `PRAGMA foreign_keys=ON`); perr != nil && err == nil {
			err = fmt.Errorf("store: migration %s: restore foreign_keys=on: %w", m.name, perr)
		}
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	fail := func(e error) error {
		tx.Rollback()
		return fmt.Errorf("store: migration %s: %w", m.name, e)
	}
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fail(err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.n)); err != nil {
		return fail(fmt.Errorf("set version: %w", err))
	}
	bad, err := queryStrings(ctx, tx, `PRAGMA foreign_key_check`, func(r *sql.Rows) (string, error) {
		var tbl, parent string
		var rowid sql.NullInt64
		var fkid int
		err := r.Scan(&tbl, &rowid, &parent, &fkid)
		return tbl + " -> " + parent, err
	})
	if err != nil {
		return fail(fmt.Errorf("foreign_key_check: %w", err))
	}
	if len(bad) > 0 {
		return fail(fmt.Errorf("foreign_key_check found violations (table -> parent): %s", strings.Join(bad, ", ")))
	}
	stale, err := queryStrings(ctx, tx, `SELECT name, sql FROM sqlite_master WHERE sql IS NOT NULL`, func(r *sql.Rows) (string, error) {
		var name, text string
		if err := r.Scan(&name, &text); err != nil {
			return "", err
		}
		if staleNewRef.MatchString(text) {
			return name, nil
		}
		return "", nil
	})
	if err != nil {
		return fail(fmt.Errorf("rename guard: %w", err))
	}
	if len(stale) > 0 {
		return fail(fmt.Errorf("rename guard: schema objects still reference a _new table: %s", strings.Join(stale, ", ")))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: migration %s: commit: %w", m.name, err)
	}
	return nil
}

// queryStrings collects the non-empty strings produced by scan for each row.
func queryStrings(ctx context.Context, tx *sql.Tx, q string, scan func(*sql.Rows) (string, error)) ([]string, error) {
	rows, err := tx.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		s, err := scan(rows)
		if err != nil {
			return nil, err
		}
		if s != "" {
			out = append(out, s)
		}
	}
	return out, rows.Err()
}
