package records

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// querier is what both a reader pool and a write transaction offer.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

const recordCols = `id, title, subtitle, liner_note, cover_seed, cover_rerolls, status, proposal_note, proposed_by, proposed_at,
	published_by, published_at, first_published_at, revision, created_by, created_at, updated_by, updated_at, hidden_at`

func scanRecord(row interface{ Scan(...any) error }) (Record, error) {
	var r Record
	err := row.Scan(&r.ID, &r.Title, &r.Subtitle, &r.LinerNote, &r.CoverSeed, &r.CoverRerolls, &r.Status, &r.ProposalNote, &r.ProposedBy,
		&r.ProposedAt, &r.PublishedBy, &r.PublishedAt, &r.FirstPublishedAt, &r.Revision, &r.CreatedBy, &r.CreatedAt, &r.UpdatedBy, &r.UpdatedAt, &r.HiddenAt)
	return r, err
}

// loadRecord reads one Record row; 404 NOT_FOUND when missing.
func loadRecord(ctx context.Context, q querier, id string) (Record, error) {
	r, err := scanRecord(q.QueryRowContext(ctx, `SELECT `+recordCols+` FROM records WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, errNotFound(id)
	}
	if err != nil {
		return Record{}, fmt.Errorf("records: read record: %w", err)
	}
	return r, nil
}

// Get returns the Record row; views are recordview's.
func (s *Service) Get(ctx context.Context, id string) (Record, error) {
	return loadRecord(ctx, s.st.R, id)
}

const slotCols = `s.id, s.record_id, s.song_id, j.key + 1, s.side, s.pinned_take_id, s.pinned_by, s.pinned_at, s.synced_seq, s.added_by, s.added_at,
	so.retired_at IS NOT NULL`

// loadSlots returns the live slots of a Record in order_json order (one statement).
func loadSlots(ctx context.Context, q querier, recordID string) ([]Slot, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+slotCols+`
		FROM records r, json_each(r.order_json) j
		JOIN record_slots s ON s.id = j.value AND s.record_id = r.id AND s.removed_at IS NULL
		JOIN songs so ON so.id = s.song_id
		WHERE r.id = ? ORDER BY j.key`, recordID)
	if err != nil {
		return nil, fmt.Errorf("records: read slots: %w", err)
	}
	defer rows.Close()
	var out []Slot
	for rows.Next() {
		var sl Slot
		if err := rows.Scan(&sl.ID, &sl.RecordID, &sl.SongID, &sl.Position, &sl.Side, &sl.PinnedTakeID, &sl.PinnedBy, &sl.PinnedAt, &sl.SyncedSeq,
			&sl.AddedBy, &sl.AddedAt, &sl.SongRetired); err != nil {
			return nil, err
		}
		out = append(out, sl)
	}
	return out, rows.Err()
}

func recordExists(ctx context.Context, q querier, id string) error {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM records WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return errNotFound(id)
	}
	return err
}

// Slots returns the live slots in order.
func (s *Service) Slots(ctx context.Context, id string) ([]Slot, error) {
	out, err := loadSlots(ctx, s.st.R, id)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		if err := recordExists(ctx, s.st.R, id); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Resolved returns the live slots in order with their resolved Takes (C121.2): three statements, no N+1.
func (s *Service) Resolved(ctx context.Context, id string) ([]ResolvedSlot, error) {
	return resolveRecord(ctx, s.st.R, id)
}

func resolveRecord(ctx context.Context, q querier, id string) ([]ResolvedSlot, error) {
	slots, err := loadSlots(ctx, q, id)
	if err != nil {
		return nil, err
	}
	if len(slots) == 0 {
		return nil, recordExists(ctx, q, id)
	}
	return resolveSlots(ctx, q, slots)
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

// resolveSlots runs the two remaining statements of C121.2 (the Picks of the Songs, the Takes) and applies ResolveSlot.
func resolveSlots(ctx context.Context, q querier, slots []Slot) ([]ResolvedSlot, error) {
	var songArgs []any
	seenSong := map[string]bool{}
	for _, sl := range slots {
		if !seenSong[sl.SongID] {
			seenSong[sl.SongID] = true
			songArgs = append(songArgs, sl.SongID)
		}
	}
	picks := map[string]PickInput{}
	rows, err := q.QueryContext(ctx, `SELECT song_id, take_id, source FROM song_picks WHERE song_id IN (`+placeholders(len(songArgs))+`)`, songArgs...)
	if err != nil {
		return nil, fmt.Errorf("records: read picks: %w", err)
	}
	for rows.Next() {
		var song string
		var pk PickInput
		if err := rows.Scan(&song, &pk.TakeID, &pk.Source); err != nil {
			rows.Close()
			return nil, err
		}
		picks[song] = pk
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var takeArgs []any
	seenTake := map[string]bool{}
	add := func(id *string) {
		if id != nil && !seenTake[*id] {
			seenTake[*id] = true
			takeArgs = append(takeArgs, *id)
		}
	}
	for _, sl := range slots {
		add(sl.PinnedTakeID)
		if sl.PinnedTakeID == nil {
			add(picks[sl.SongID].TakeID)
		}
	}
	takes := map[string]*TakeInfo{}
	if len(takeArgs) > 0 {
		rows, err = q.QueryContext(ctx, `SELECT id, status, duration_s, lossy FROM tracks WHERE id IN (`+placeholders(len(takeArgs))+`)`, takeArgs...)
		if err != nil {
			return nil, fmt.Errorf("records: read takes: %w", err)
		}
		for rows.Next() {
			var t TakeInfo
			if err := rows.Scan(&t.ID, &t.Status, &t.DurationS, &t.Lossy); err != nil {
				rows.Close()
				return nil, err
			}
			takes[t.ID] = &t
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	out := make([]ResolvedSlot, 0, len(slots))
	for _, sl := range slots {
		pk, ok := picks[sl.SongID]
		if !ok {
			pk = PickInput{Source: "none"}
		}
		var pinned, pickTake *TakeInfo
		if sl.PinnedTakeID != nil {
			pinned = takes[*sl.PinnedTakeID]
		}
		if pk.TakeID != nil {
			pickTake = takes[*pk.TakeID]
		}
		out = append(out, ResolveSlot(sl, pinned, pk, pickTake))
	}
	return out, nil
}

// songPickTake returns the Take of the Song's current Pick, or nil.
func songPickTake(ctx context.Context, q querier, songID string) (*string, error) {
	var t *string
	err := q.QueryRowContext(ctx, `SELECT take_id FROM song_picks WHERE song_id = ?`, songID).Scan(&t)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

// songMaxSeq is the highest pick_events.seq of a Song, 0 when it has none.
func songMaxSeq(ctx context.Context, q querier, songID string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM pick_events WHERE song_id = ?`, songID).Scan(&n)
	return n, err
}

// requireLiveSong is the add rule: 422 UNKNOWN_SONG for an id that is no Song, 409 SONG_RETIRED (with retired_into) for a retired one.
func requireLiveSong(ctx context.Context, q querier, songID string) error {
	var retiredAt, into *string
	err := q.QueryRowContext(ctx, `SELECT retired_at, retired_into FROM songs WHERE id = ?`, songID).Scan(&retiredAt, &into)
	if errors.Is(err, sql.ErrNoRows) {
		return problem.E(422, "UNKNOWN_SONG", "no such song", map[string]any{"song_id": songID})
	}
	if err != nil {
		return fmt.Errorf("records: read song: %w", err)
	}
	if retiredAt != nil {
		d := map[string]any{"song_id": songID, "retired_into": nil}
		if into != nil {
			d["retired_into"] = *into
		}
		return problem.E(409, "SONG_RETIRED", "the song is retired", d)
	}
	return nil
}

// insertSlot inserts a live slot row (the caller updates order_json afterwards). A nil pin makes it floating; its cursor
// starts at the Song's current max pick event.
func (s *Service) insertSlot(ctx context.Context, tx *sql.Tx, recordID string, p auth.Principal, at, songID string, pin, side *string) (Slot, error) {
	seq, err := songMaxSeq(ctx, tx, songID)
	if err != nil {
		return Slot{}, err
	}
	sl := Slot{ID: s.newID(), RecordID: recordID, SongID: songID, Side: side, PinnedTakeID: pin, SyncedSeq: seq, AddedBy: p.Key(), AddedAt: at}
	var pinBy, pinAt *string
	if pin != nil {
		pinBy, pinAt = strp(p.Key()), strp(at)
		sl.PinnedBy, sl.PinnedAt = pinBy, pinAt
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO record_slots(id, record_id, song_id, side, pinned_take_id, pinned_by, pinned_at, synced_seq, added_by, added_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, sl.ID, recordID, songID, side, pin, pinBy, pinAt, seq, sl.AddedBy, at); err != nil {
		return Slot{}, fmt.Errorf("records: insert slot: %w", err)
	}
	return sl, nil
}

// writeOrder stores the live slot ids in order; the trigger verifies them against record_slots.
func writeOrder(ctx context.Context, tx *sql.Tx, recordID string, ids []string) error {
	b, _ := json.Marshal(ids)
	if _, err := tx.ExecContext(ctx, `UPDATE records SET order_json = ? WHERE id = ?`, string(b), recordID); err != nil {
		return fmt.Errorf("records: write order: %w", err)
	}
	return nil
}
