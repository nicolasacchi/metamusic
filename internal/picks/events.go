package picks

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

const (
	systemActor     = "system:provenance"
	systemActorKind = "system"
	systemActorName = "provenance"
	defaultEvents   = 100
	maxEvents       = 200
)

// actor is the (actor, actor_kind, actor_name) triple of an event row.
type actor struct{ Key, Kind, Name string }

var systemActorRow = actor{systemActor, systemActorKind, systemActorName}

// insertEventTx appends one event to the Song's chain (seq = max+1) and returns its id.
func insertEventTx(ctx context.Context, tx *sql.Tx, env store.Env, songID string, from, to current, reason string, a actor, ev json.RawMessage) (string, int, error) {
	var seq int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM pick_events WHERE song_id = ?`, songID).Scan(&seq); err != nil {
		return "", 0, fmt.Errorf("picks: next seq: %w", err)
	}
	id := env.NewID()
	if _, err := tx.ExecContext(ctx, `INSERT INTO pick_events(id,song_id,seq,at,from_take_id,to_take_id,from_source,to_source,reason,actor,actor_kind,actor_name,evidence_json)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, songID, seq, store.FormatTime(env.Now), from.Take, to.Take, string(from.Source), string(to.Source), reason, a.Key, a.Kind, a.Name, string(ev)); err != nil {
		return "", 0, fmt.Errorf("picks: insert event: %w", err)
	}
	return id, seq, nil
}

// Events pages the pick-event feed by the integer cursor (C91.8).
func (s *Service) Events(ctx context.Context, f EventFilter) (EventPage, error) {
	if s.st == nil {
		return EventPage{}, errUnconfigured
	}
	ids := slices.Clone(f.SongIDs)
	if f.SongID != "" {
		ids = append(ids, f.SongID)
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(f.SongIDs) > MaxSongIDsFilter || len(ids) > MaxSongIDsFilter+1 {
		return EventPage{}, problem.E(422, "VALIDATION_FAILED", fmt.Sprintf("song_ids takes at most %d ids", MaxSongIDsFilter), map[string]any{"field": "song_ids"})
	}
	limit := f.Limit
	if limit <= 0 {
		limit = defaultEvents
	}
	limit = min(limit, maxEvents)
	q := `SELECT rowid, id, song_id, seq, at, from_take_id, to_take_id, from_source, to_source, reason, actor, actor_kind, actor_name, evidence_json
		FROM pick_events WHERE rowid > ?`
	args := []any{f.After}
	if len(ids) > 0 {
		q += ` AND song_id IN (?` + strings.Repeat(`,?`, len(ids)-1) + `)` //nolint:gosec // G202: only "?" placeholders are concatenated
		for _, id := range ids {
			args = append(args, id)
		}
	}
	q += ` ORDER BY rowid LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.st.R.QueryContext(ctx, q, args...) //nolint:gosec // G202: only "?" placeholders are concatenated
	if err != nil {
		return EventPage{}, fmt.Errorf("picks: events: %w", err)
	}
	defer rows.Close()
	page := EventPage{Items: []PickEvent{}}
	for rows.Next() {
		var e PickEvent
		var ev string
		if err := rows.Scan(&e.Cursor, &e.ID, &e.SongID, &e.Seq, &e.At, &e.FromTakeID, &e.ToTakeID, &e.FromSource, &e.ToSource, &e.Reason,
			&e.Actor, &e.ActorKind, &e.ActorName, &ev); err != nil {
			return EventPage{}, err
		}
		e.Evidence = json.RawMessage(ev)
		page.Items = append(page.Items, e)
	}
	if err := rows.Err(); err != nil {
		return EventPage{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		c := page.Items[limit-1].Cursor
		page.NextCursor = &c
	}
	return page, nil
}
