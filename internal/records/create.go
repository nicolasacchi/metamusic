package records

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// This file holds Create and the helpers every lane-A file shares: the error constructors, the text rules (the playlist
// rules of M2a, D-L2-27: trim, collapse, refuse control characters), the write-transaction runner with its bounded
// SQLITE_BUSY retry (C119.1 note 10) and the changelog writer for the rows the Service owns (C119.2).

// PositionEnd is SlotOp.Position for an add that appends: the position is 1..n+1 or this value, and 0 is INVALID_POSITION.
const PositionEnd = -1

func errNotFound(id string) error {
	return problem.E(404, "NOT_FOUND", "record not found", map[string]any{"record_id": id})
}

func errInvalid(field, msg string) error {
	return problem.E(422, "VALIDATION_FAILED", msg, map[string]any{"field": field})
}

func errStale(rec Record) error {
	return problem.E(409, "STALE_REVISION", "the record changed; re-read it and retry",
		map[string]any{"revision": rec.Revision, "status": string(rec.Status)})
}

func errHumanOnly(hint string) error {
	return problem.E(403, "HUMAN_ONLY", "only the operator can do this", map[string]any{"hint": hint})
}

func errState(rec Record, msg string) error {
	return problem.E(409, "INVALID_RECORD_STATE", msg, map[string]any{"status": string(rec.Status)})
}

// cleanLine trims s, refuses control characters, collapses runs of spaces and bounds the length in characters.
func cleanLine(field, s string, minLen, maxLen int) (string, error) {
	s = strings.TrimSpace(s)
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", errInvalid(field, "must not contain control characters")
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	if n := utf8.RuneCountInString(s); n < minLen || n > maxLen {
		if minLen == 0 {
			return "", errInvalid(field, fmt.Sprintf("at most %d characters", maxLen))
		}
		return "", errInvalid(field, fmt.Sprintf("%d to %d characters", minLen, maxLen))
	}
	return s, nil
}

// cleanNote is cleanLine for the multi-line operator text: line breaks and tabs stay.
func cleanNote(field, s string, maxLen int) (string, error) {
	s = strings.TrimSpace(s)
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return "", errInvalid(field, "must not contain control characters")
		}
	}
	if utf8.RuneCountInString(s) > maxLen {
		return "", errInvalid(field, fmt.Sprintf("at most %d characters", maxLen))
	}
	return s, nil
}

func (s *Service) stamp() string { return store.FormatTime(s.now()) }

func actorKind(p auth.Principal) string {
	switch p.Kind {
	case auth.KindHuman, auth.KindDevice:
		return "human"
	case auth.KindAgent:
		return "agent"
	}
	return "system"
}

func actorName(p auth.Principal) string {
	n := p.Name
	if n == "" {
		n = p.ID
	}
	if n == "" {
		n = string(p.Kind)
	}
	if n == "" {
		n = "unknown"
	}
	if r := []rune(n); len(r) > 120 {
		n = string(r[:120])
	}
	return n
}

// isBusy reports a cross-process SQLITE_BUSY or SQLITE_LOCKED.
func isBusy(err error) bool {
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		if c := coded.Code() & 0xff; c == 5 || c == 6 {
			return true
		}
	}
	m := err.Error()
	return strings.Contains(m, "SQLITE_BUSY") || strings.Contains(m, "database is locked")
}

// writeTx runs fn in one write transaction on the writer pool (BEGIN IMMEDIATE through _txlock) and retries the whole
// call up to SyncRetries times, 20 ms x 2^n apart, when it met SQLITE_BUSY. fn must therefore be repeatable.
func (s *Service) writeTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	for n := 0; ; n++ {
		err := s.writeOnce(ctx, fn)
		if err == nil || !isBusy(err) || n >= SyncRetries {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond << n):
		}
	}
}

func (s *Service) writeOnce(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// insertChangeRow appends one changelog row of the Service to recordID's chain, at the next seq, and returns it. Kind-specific
// fields come in c; the id, seq, at and actor columns are filled here.
func (s *Service) insertChangeRow(ctx context.Context, tx *sql.Tx, recordID string, p auth.Principal, at string, c Change) (Change, error) {
	var seq int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM record_changes WHERE record_id = ?`, recordID).Scan(&seq); err != nil {
		return Change{}, fmt.Errorf("records: next seq: %w", err)
	}
	c.ID, c.Seq, c.At = s.newID(), seq, at
	c.Actor, c.ActorKind, c.ActorName = p.Key(), actorKind(p), actorName(p)
	var detail any
	if len(c.Detail) > 0 {
		detail = string(c.Detail)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO record_changes(id, record_id, seq, at, kind, slot_id, position, from_take_id, to_take_id, reason, detail_json,
		actor, actor_kind, actor_name) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, recordID, c.Seq, c.At, c.Kind, c.SlotID, c.Position, c.FromTakeID, c.ToTakeID, c.Reason, detail, c.Actor, c.ActorKind, c.ActorName); err != nil {
		return Change{}, fmt.Errorf("records: write %s row: %w", c.Kind, err)
	}
	return c, nil
}

func strp(s string) *string { return &s }
func intp(n int) *int       { return &n }

// Create makes a draft Record, with floating slots for SongIDs in order (C120.1).
func (s *Service) Create(ctx context.Context, p auth.Principal, in NewRecord) (Record, error) {
	title, err := cleanLine("title", in.Title, 1, MaxTitle)
	if err != nil {
		return Record{}, err
	}
	sub, err := cleanLine("subtitle", in.Subtitle, 0, MaxSubtitle)
	if err != nil {
		return Record{}, err
	}
	note, err := cleanNote("liner_note", in.LinerNote, MaxLinerNote)
	if err != nil {
		return Record{}, err
	}
	if len(in.SongIDs) > MaxSlots {
		return Record{}, problem.E(422, "TOO_MANY_SLOTS", "a record holds at most 60 slots", map[string]any{"limit": MaxSlots, "slots": len(in.SongIDs)})
	}
	seen := map[string]bool{}
	for _, id := range in.SongIDs {
		if seen[id] {
			return Record{}, errInvalid("song_ids", "song ids must be distinct")
		}
		seen[id] = true
	}
	var out Record
	err = s.writeTx(ctx, func(tx *sql.Tx) error {
		id, at := s.newID(), s.stamp()
		if _, err := tx.ExecContext(ctx, `INSERT INTO records(id, title, subtitle, liner_note, cover_seed, cover_rerolls, status, revision, created_by, created_at, updated_by, updated_at)
			VALUES (?,?,?,?,?,0,'draft',1,?,?,?,?)`, id, title, sub, note, CoverSeed(id, 0), p.Key(), at, p.Key(), at); err != nil {
			return fmt.Errorf("records: create: %w", err)
		}
		if _, err := s.insertChangeRow(ctx, tx, id, p, at, Change{Kind: KindCreated, Reason: strp(ReasonInitial)}); err != nil {
			return err
		}
		order := make([]string, 0, len(in.SongIDs))
		for i, songID := range in.SongIDs {
			if err := requireLiveSong(ctx, tx, songID); err != nil {
				return err
			}
			sl, err := s.insertSlot(ctx, tx, id, p, at, songID, nil, nil)
			if err != nil {
				return err
			}
			order = append(order, sl.ID)
			pick, err := songPickTake(ctx, tx, songID)
			if err != nil {
				return err
			}
			if _, err := s.insertChangeRow(ctx, tx, id, p, at, Change{Kind: KindSlotAdded, SlotID: &sl.ID, Position: intp(i + 1), ToTakeID: pick, Reason: strp(ReasonInitial)}); err != nil {
				return err
			}
		}
		if len(order) > 0 {
			if err := writeOrder(ctx, tx, id, order); err != nil {
				return err
			}
		}
		out, err = loadRecord(ctx, tx, id)
		return err
	})
	return out, err
}
