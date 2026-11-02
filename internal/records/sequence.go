package records

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/sequence"
)

// sequence is the slot op "sequence" (L3 C138.2): it orders the working list by tempo, key and energy and records the run.
// It acts on the list as the previous ops of the call left it, uses each slot's resolved Take for the features (pin, else the
// Song's Pick, else the Song's own features), and changes only the order: membership, pins, sides and revision semantics are
// untouched. Its slot_moved rows carry the sequencer's evidence; an order that does not change writes the run and no row.
func (e *edit) sequence(op SlotOp) error {
	if op.SongID != "" || op.SlotID != "" || op.TakeID != "" || op.Position != 0 || op.Side.Set || len(op.SlotIDs) > 0 {
		return errInvalid("op", "sequence takes only strategy, first_song_id and last_song_id")
	}
	_, err := e.runSequence(SeqSpec{Strategy: op.Strategy, FirstSongID: op.FirstSongID, LastSongID: op.LastSongID})
	return err
}

// runSequence plans the working list, rewrites its order, stores the run and emits the slot_moved rows.
func (e *edit) runSequence(spec SeqSpec) (SequenceOutcome, error) {
	refs := make([]sequence.Ref, len(e.list))
	for i, sl := range e.list {
		take, err := e.resolvedTake(sl)
		if err != nil {
			return SequenceOutcome{}, err
		}
		refs[i] = sequence.Ref{SongID: sl.SongID, TakeID: take}
	}
	planned, err := sequence.Prepare(e.ctx, e.tx, refs, spec.Strategy, spec.FirstSongID, spec.LastSongID)
	if err != nil {
		return SequenceOutcome{}, err
	}
	bySong := make(map[string]*Slot, len(e.list))
	for _, sl := range e.list {
		bySong[sl.SongID] = sl
	}
	before := e.positions()
	beforeIDs := e.slotIDs()
	next := make([]*Slot, len(planned.Result.Order))
	for i, songID := range planned.Result.Order {
		next[i] = bySong[songID]
	}
	e.list = next
	afterIDs := e.slotIDs()
	id := e.s.newID()
	if err := e.insertSequence(id, planned, beforeIDs, afterIDs, spec); err != nil {
		return SequenceOutcome{}, err
	}
	detail := func(from int) []byte {
		b, _ := json.Marshal(struct {
			From       int    `json:"from"`
			By         string `json:"by"`
			Strategy   string `json:"strategy"`
			SequenceID string `json:"sequence_id"`
		}{from, "sequencer", planned.Strategy, id})
		return b
	}
	if err := e.emitMovesWith(before, detail); err != nil {
		return SequenceOutcome{}, err
	}
	out := SequenceOutcome{ID: id, Strategy: planned.Strategy, Before: beforeIDs, After: afterIDs, Changed: !equalStrings(beforeIDs, afterIDs)}
	e.sequences = append(e.sequences, out)
	return out, nil
}

func (e *edit) slotIDs() []string {
	ids := make([]string, len(e.list))
	for i, sl := range e.list {
		ids[i] = sl.ID
	}
	return ids
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// insertSequence appends the record_sequences row of a run: the revision the call was made against, the slot orders before
// and after the sequence step, and the mm.sequence/1 document (ids, integers and codes only).
func (e *edit) insertSequence(id string, p *sequence.Planned, before, after []string, spec SeqSpec) error {
	bj, _ := json.Marshal(before)
	aj, _ := json.Marshal(after)
	cov, _ := json.Marshal(p.Doc.Coverage)
	exact := 0
	if p.Result.Exact {
		exact = 1
	}
	var first, last any
	if spec.FirstSongID != "" {
		first = spec.FirstSongID
	}
	if spec.LastSongID != "" {
		last = spec.LastSongID
	}
	if _, err := e.tx.ExecContext(e.ctx, `INSERT INTO record_sequences(id, record_id, record_revision, strategy, first_song_id, last_song_id,
		order_before_json, order_after_json, exact, cost_input, cost_output, coverage_json, digest, explanation_json, actor, actor_kind, actor_name, at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, e.rec.ID, e.rec.Revision, p.Strategy, first, last, string(bj), string(aj), exact, p.Result.CostInput, p.Result.CostOutput,
		string(cov), p.Result.Digest, string(p.Doc.JSON()), e.p.Key(), actorKind(e.p), actorName(e.p), e.at); err != nil {
		return fmt.Errorf("records: write sequence: %w", err)
	}
	return nil
}

// newEdit is the working state of one write transaction over the live slots of rec.
func (s *Service) newEdit(ctx context.Context, tx *sql.Tx, rec Record, p auth.Principal, at string, slots []Slot) *edit {
	e := &edit{s: s, ctx: ctx, tx: tx, rec: rec, p: p, at: at}
	for i := range slots {
		e.list = append(e.list, &slots[i])
	}
	return e
}
