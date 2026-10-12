package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
)

// ErrNoDatabase is returned by OpenReadOnly when the database file is absent.
var ErrNoDatabase = errors.New("store: database does not exist")

// ErrSchemaMismatch is returned (wrapped with have/want) when user_version differs
// from the number of embedded migrations.
var ErrSchemaMismatch = errors.New("store: schema version mismatch")

// OpenReadOnly opens an existing database for reading only (import dry-run).
// It never creates, migrates or checkpoints anything. W and R are the same pool.
func OpenReadOnly(ctx context.Context, path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNoDatabase
		}
		return nil, fmt.Errorf("store: stat database: %w", err)
	}
	q := url.Values{}
	q.Set("mode", "ro")
	if _, err := os.Stat(path + "-wal"); err != nil {
		// Cleanly closed: immutable=1 creates no -wal/-shm files. With a -wal
		// present a live writer owns the sidecars, so join it read-only.
		q.Set("immutable", "1")
	}
	q.Add("_pragma", "query_only(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	db, err := sql.Open("sqlite", "file:"+(&url.URL{Path: path}).EscapedPath()+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("store: open read-only: %w", err)
	}
	var have int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&have); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: read user_version: %w", err)
	}
	ms, err := loadMigrations()
	if err != nil {
		db.Close()
		return nil, err
	}
	if have != len(ms) {
		db.Close()
		return nil, fmt.Errorf("%w: database is at version %d, this binary expects %d; start this binary's serve once (it migrates on open) or use the binary that matches the database", ErrSchemaMismatch, have, len(ms))
	}
	return &Store{W: db, R: db}, nil
}
