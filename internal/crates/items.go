package crates

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

type querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// shape checks that m names exactly one id and that it matches the subject, and returns the id.
func shape(subject Subject, m Member) (string, error) {
	if (m.SongID == "") == (m.TrackID == "") {
		return "", invalidField("items", "a member names exactly one of song_id and track_id")
	}
	if (subject == SubjectSong) != (m.SongID != "") {
		return "", problem.E(http.StatusUnprocessableEntity, "SUBJECT_MISMATCH", "the member does not match the Crate's subject", map[string]any{"subject": string(subject)})
	}
	if m.SongID != "" {
		return m.SongID, nil
	}
	return m.TrackID, nil
}

// checkMembers validates members for adding (exist, Song not retired) and returns them without duplicates, in order.
func (s *Service) checkMembers(ctx context.Context, q querier, subject Subject, in []Member, exist bool) ([]Member, error) {
	seen := map[string]bool{}
	var out []Member
	for _, m := range in {
		id, err := shape(subject, m)
		if err != nil {
			return nil, err
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		if exist {
			if err := s.checkExists(ctx, q, subject, id); err != nil {
				return nil, err
			}
		}
		out = append(out, m)
	}
	return out, nil
}

func (s *Service) checkExists(ctx context.Context, q querier, subject Subject, id string) error {
	if subject == SubjectSong {
		var retired sql.NullString
		err := q.QueryRowContext(ctx, `SELECT retired_at FROM songs WHERE id = ?`, id).Scan(&retired)
		if errors.Is(err, sql.ErrNoRows) {
			return problem.E(http.StatusUnprocessableEntity, "UNKNOWN_SONG", "no such song", map[string]any{"song_id": id})
		}
		if err != nil {
			return fmt.Errorf("crates: check song: %w", err)
		}
		if retired.Valid {
			return problem.E(http.StatusConflict, "SONG_RETIRED", "the song was merged into another; add that one", map[string]any{"song_id": id})
		}
		return nil
	}
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM tracks WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return problem.E(http.StatusUnprocessableEntity, "UNKNOWN_TRACK", "no such track", map[string]any{"track_id": id})
	}
	if err != nil {
		return fmt.Errorf("crates: check track: %w", err)
	}
	return nil
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s *Service) insertItem(ctx context.Context, x execer, crateID string, m Member, p auth.Principal, now string) error {
	var song, track any
	if m.SongID != "" {
		song = m.SongID
	} else {
		track = m.TrackID
	}
	if _, err := x.ExecContext(ctx, `INSERT INTO crate_items(id,crate_id,song_id,track_id,added_by,added_at) VALUES(?,?,?,?,?,?)`, s.id(), crateID, song, track, p.Key(), now); err != nil {
		return fmt.Errorf("crates: add item: %w", err)
	}
	return nil
}

// liveClause selects the live row of a member of crate ?.
func liveClause(m Member) (string, string) {
	if m.SongID != "" {
		return `song_id = ?`, m.SongID
	}
	return `track_id = ?`, m.TrackID
}

// EditItems adds and removes members of a manual Crate; item edits do not bump the revision. Removals run first, so a member
// named in both lists ends up present as a new row.
func (s *Service) EditItems(ctx context.Context, p auth.Principal, id string, e ItemsEdit) (ItemsResult, error) {
	if len(e.Add) > MaxEditBatch || len(e.Remove) > MaxEditBatch {
		return ItemsResult{}, invalidField("items", fmt.Sprintf("at most %d adds and %d removes per call", MaxEditBatch, MaxEditBatch))
	}
	if len(e.Add) == 0 && len(e.Remove) == 0 {
		return ItemsResult{}, invalidField("body", "nothing to change")
	}
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return ItemsResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	c, err := s.load(ctx, tx, id)
	if err != nil {
		return ItemsResult{}, err
	}
	if c.Kind != KindManual {
		return ItemsResult{}, problem.E(http.StatusConflict, "CRATE_IS_SMART", "a smart Crate has no items; edit its filter", nil)
	}
	add, err := s.checkMembers(ctx, tx, c.Subject, e.Add, true)
	if err != nil {
		return ItemsResult{}, err
	}
	remove, err := s.checkMembers(ctx, tx, c.Subject, e.Remove, false)
	if err != nil {
		return ItemsResult{}, err
	}
	var res ItemsResult
	now := s.stamp()
	for _, m := range remove {
		cond, val := liveClause(m)
		r, err := tx.ExecContext(ctx, `UPDATE crate_items SET removed_at = ?, removed_by = ? WHERE crate_id = ? AND `+cond+` AND removed_at IS NULL`, now, p.Key(), id, val) //nolint:gosec // G202: cond is one of two fixed column predicates
		if err != nil {
			return ItemsResult{}, fmt.Errorf("crates: remove item: %w", err)
		}
		if n, _ := r.RowsAffected(); n > 0 {
			res.Removed++
		} else {
			res.NotMember++
		}
	}
	var live int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM crate_items WHERE crate_id = ? AND removed_at IS NULL`, id).Scan(&live); err != nil {
		return ItemsResult{}, fmt.Errorf("crates: count items: %w", err)
	}
	var fresh []Member
	for _, m := range add {
		cond, val := liveClause(m)
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM crate_items WHERE crate_id = ? AND `+cond+` AND removed_at IS NULL`, id, val).Scan(&one)
		switch {
		case err == nil:
			res.Already++
		case errors.Is(err, sql.ErrNoRows):
			fresh = append(fresh, m)
		default:
			return ItemsResult{}, fmt.Errorf("crates: probe item: %w", err)
		}
	}
	if live+len(fresh) > MaxManualItems {
		return ItemsResult{}, problem.E(http.StatusConflict, "CRATE_FULL", fmt.Sprintf("a Crate holds at most %d members", MaxManualItems), map[string]any{"count": live, "max": MaxManualItems})
	}
	for _, m := range fresh {
		if err := s.insertItem(ctx, tx, id, m, p, now); err != nil {
			return ItemsResult{}, err
		}
	}
	res.Added, res.Count = len(fresh), live+len(fresh)
	if _, err := tx.ExecContext(ctx, `UPDATE crates SET updated_by = ?, updated_at = ? WHERE id = ?`, p.Key(), now, id); err != nil {
		return ItemsResult{}, fmt.Errorf("crates: touch: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ItemsResult{}, err
	}
	return res, nil
}
