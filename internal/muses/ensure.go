package muses

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/textfold"
)

// collapse trims s and collapses every run of whitespace to one space; the case and the diacritics stay (display text).
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// EnsureReferenceTx gets or creates the reference Muse for (title, artist) inside the caller's transaction (C89, C95).
// A Muse whose normalised key exists is returned unchanged with created = false, hidden or not; a missing one is
// created with source 'recipe_refs'. No error message carries Muse text (C99.4).
func EnsureReferenceTx(ctx context.Context, tx *sql.Tx, now time.Time, newID func() string, title string, artist *string, createdBy string) (id string, created bool, err error) {
	t := collapse(title)
	if t == "" || utf8.RuneCountInString(t) > MaxTitleRunes {
		return "", false, problem.E(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "a Muse title must be 1 to 200 characters", map[string]any{"field": "title"})
	}
	var art *string
	if artist != nil {
		if a := collapse(*artist); a != "" {
			if utf8.RuneCountInString(a) > MaxArtistRunes {
				return "", false, problem.E(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "a Muse artist must be 1 to 200 characters", map[string]any{"field": "artist"})
			}
			art = &a
		}
	}
	key := Key(t, art)
	switch err := tx.QueryRowContext(ctx, `SELECT id FROM muses WHERE key = ?`, key).Scan(&id); {
	case err == nil:
		return id, false, nil
	case err != sql.ErrNoRows:
		return "", false, fmt.Errorf("muses: ensure: %w", err)
	}
	id = newID()
	ts := store.FormatTime(now)
	if _, err := tx.ExecContext(ctx, `INSERT INTO muses(id, kind, title, title_fold, artist, brief, key, source, created_by, created_at, updated_by, updated_at)
		VALUES (?, 'reference', ?, ?, ?, NULL, ?, 'recipe_refs', ?, ?, ?, ?)`,
		id, t, textfold.Norm(t), art, key, createdBy, ts, createdBy, ts); err != nil {
		return "", false, fmt.Errorf("muses: ensure: insert: %w", err)
	}
	return id, true, nil
}

// Querier is the read side shared by *sql.DB, *sql.Tx and the Reconciler's read transaction.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Faults are the Muse invariants Verify (I6, C95.5) checks; the ids are Song and Muse ids, never Muse text. provenance
// calls it because only this package may read the muses tables (C99.2).
type Faults struct {
	// KeyMismatch lists reference Muses whose key differs from Key(title, artist).
	KeyMismatch []string
	// DanglingSongs lists live Songs whose muse_id names no Muse.
	DanglingSongs []string
}

// VerifyMuses checks I6: every reference Muse's key equals Key(title, artist) and every Muse a live Song references exists.
func VerifyMuses(ctx context.Context, q Querier) (Faults, error) {
	var f Faults
	rows, err := q.QueryContext(ctx, `SELECT id, title, artist, key FROM muses WHERE kind = 'reference'`)
	if err != nil {
		return f, fmt.Errorf("muses: verify: %w", err)
	}
	for rows.Next() {
		var id, title, key string
		var artist *string
		if err := rows.Scan(&id, &title, &artist, &key); err != nil {
			_ = rows.Close()
			return f, err
		}
		if Key(title, artist) != key {
			f.KeyMismatch = append(f.KeyMismatch, id)
		}
	}
	if err := rows.Close(); err != nil {
		return f, err
	}
	rows, err = q.QueryContext(ctx, `SELECT s.id FROM songs s WHERE s.retired_at IS NULL AND s.muse_id IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM muses m WHERE m.id = s.muse_id)`)
	if err != nil {
		return f, fmt.Errorf("muses: verify: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return f, err
		}
		f.DanglingSongs = append(f.DanglingSongs, id)
	}
	return f, rows.Err()
}
