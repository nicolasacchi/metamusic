package recordview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/nicolasacchi/metamusic/internal/records"
)

// sequenceView renders the latest record_sequences row of a Record for get_record include=sequence (L3 C138.5): the stored
// document with Song and Take titles injected into the steps at read, and Stale when the Record no longer matches the run.
// A run goes stale when the live slot order differs from the run's order_after, or when a slot now resolves to another Take
// than the one whose features the run used (a Pick moved or a pin changed): stale is the only signal, it never re-raises
// record_unsequenced. It returns nil when the Record has never been sequenced.
func (s *Service) sequenceView(ctx context.Context, d *data) (*SequenceView, error) {
	if s.o.Store == nil {
		return nil, nil
	}
	var v SequenceView
	var after, doc string
	var exact int
	err := s.o.Store.R.QueryRowContext(ctx, `SELECT id, strategy, at, actor, actor_kind, actor_name, exact, order_after_json, explanation_json
		FROM record_sequences WHERE record_id = ? ORDER BY at DESC, id DESC LIMIT 1`, d.rec.ID).
		Scan(&v.ID, &v.Strategy, &v.At, &v.Actor, &v.ActorKind, &v.ActorName, &exact, &after, &doc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("recordview: read sequence: %w", err)
	}
	v.Exact = exact == 1
	var order []string
	if err := json.Unmarshal([]byte(after), &order); err != nil {
		return nil, fmt.Errorf("recordview: sequence order: %w", err)
	}
	live := make([]string, len(d.rs))
	for i, r := range d.rs {
		live[i] = r.Slot.ID
	}
	v.Stale = !slices.Equal(order, live)

	var parsed struct {
		Cost  struct{ Input, Output string } `json:"cost"`
		Steps []map[string]json.RawMessage   `json:"steps"`
	}
	if err := json.Unmarshal([]byte(doc), &parsed); err != nil {
		return nil, fmt.Errorf("recordview: sequence document: %w", err)
	}
	v.CostInput, v.CostOutput = parsed.Cost.Input, parsed.Cost.Output
	var doc2 map[string]json.RawMessage
	if err := json.Unmarshal([]byte(doc), &doc2); err != nil {
		return nil, fmt.Errorf("recordview: sequence document: %w", err)
	}
	songIDs, takeIDs := make([]string, 0, len(parsed.Steps)), make([]string, 0, len(parsed.Steps))
	stepSong, stepTake := make([]string, len(parsed.Steps)), make([]string, len(parsed.Steps))
	for i, st := range parsed.Steps {
		_ = json.Unmarshal(st["song_id"], &stepSong[i])
		_ = json.Unmarshal(st["take_id"], &stepTake[i])
		songIDs = append(songIDs, stepSong[i])
		takeIDs = append(takeIDs, stepTake[i])
	}
	if !v.Stale {
		current := make(map[string]string, len(d.rs))
		for _, r := range d.rs {
			if r.Take != nil {
				current[r.Slot.SongID] = r.Take.ID
			}
		}
		for i := range parsed.Steps {
			if current[stepSong[i]] != stepTake[i] {
				v.Stale = true
				break
			}
		}
	}
	songTitle := map[string]string{}
	if s.o.Songs != nil {
		if ids := distinct(songIDs); len(ids) > 0 {
			m, err := s.o.Songs.Briefs(ctx, ids)
			if err != nil {
				return nil, fmt.Errorf("recordview: sequence songs: %w", err)
			}
			for id, b := range m {
				songTitle[id] = b.Title
			}
		}
	}
	takeTitle := map[string]string{}
	if s.o.Library != nil {
		if ids := distinct(takeIDs); len(ids) > 0 {
			m, err := s.o.Library.TrackRefs(ctx, ids)
			if err != nil {
				return nil, fmt.Errorf("recordview: sequence takes: %w", err)
			}
			for id, r := range m {
				takeTitle[id] = r.Title
			}
		}
	}
	for i, st := range parsed.Steps {
		st["title"], _ = json.Marshal(songTitle[stepSong[i]])
		if stepTake[i] != "" {
			st["take_title"], _ = json.Marshal(takeTitle[stepTake[i]])
		}
	}
	doc2["steps"], _ = json.Marshal(parsed.Steps)
	var err2 error
	if v.Explanation, err2 = json.Marshal(doc2); err2 != nil {
		return nil, fmt.Errorf("recordview: sequence document: %w", err2)
	}
	return &v, nil
}

// changeSequence is the run a changelog row names: sequence_id with the strategy of the row's detail.
func changeSequence(c records.Change) *SequenceRef {
	if len(c.Detail) == 0 {
		return nil
	}
	var d struct {
		SequenceID string `json:"sequence_id"`
		Strategy   string `json:"strategy"`
	}
	if json.Unmarshal(c.Detail, &d) != nil || d.SequenceID == "" {
		return nil
	}
	return &SequenceRef{ID: d.SequenceID, Strategy: d.Strategy}
}
