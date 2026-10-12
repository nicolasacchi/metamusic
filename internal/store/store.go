// Package store owns the SQLite database: connection pools and migrations.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite" // registers driver "sqlite"
)

// Store holds a single-connection writer pool and a reader pool.
type Store struct{ W, R *sql.DB }

func dsn(path string, writer bool) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	if writer {
		q.Set("_txlock", "immediate")
	}
	return "file:" + (&url.URL{Path: path}).EscapedPath() + "?" + q.Encode()
}

// Open opens (creating if needed) the database at path and applies migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	w, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		return nil, fmt.Errorf("store: open writer: %w", err)
	}
	w.SetMaxOpenConns(1)
	if err := w.PingContext(ctx); err != nil {
		w.Close()
		return nil, fmt.Errorf("store: ping writer: %w", err)
	}
	if err := Migrate(ctx, w); err != nil {
		w.Close()
		return nil, err
	}
	r, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		w.Close()
		return nil, fmt.Errorf("store: open reader: %w", err)
	}
	if err := r.PingContext(ctx); err != nil {
		w.Close()
		r.Close()
		return nil, fmt.Errorf("store: ping reader: %w", err)
	}
	return &Store{W: w, R: r}, nil
}

// Close closes both pools.
func (s *Store) Close() error {
	e1, e2 := s.R.Close(), s.W.Close()
	if e1 != nil {
		return e1
	}
	return e2
}

// UserVersion returns PRAGMA user_version (the applied migration count).
func (s *Store) UserVersion(ctx context.Context) (int, error) {
	var v int
	err := s.R.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v)
	return v, err
}
