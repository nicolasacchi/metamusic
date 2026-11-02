package records

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// sequenceForPropose runs the sequence step of propose_record in the proposing transaction (L3 C138.3): catch-up first,
// then the plan on the draft's live slots as the proposing principal, the new order and the slot_moved rows. It returns the
// run so the caller can stamp the proposed row; an error fails the whole call and the transaction writes nothing.
func (s *Service) sequenceForPropose(ctx context.Context, tx *sql.Tx, rec Record, p auth.Principal, at string, spec SeqSpec) (SequenceOutcome, error) {
	if err := s.catchUpTx(ctx, tx, rec.ID); err != nil {
		return SequenceOutcome{}, err
	}
	slots, err := loadSlots(ctx, tx, rec.ID)
	if err != nil {
		return SequenceOutcome{}, err
	}
	e := s.newEdit(ctx, tx, rec, p, at, slots)
	out, err := e.runSequence(spec)
	if err != nil {
		return SequenceOutcome{}, err
	}
	if err := writeOrder(ctx, tx, rec.ID, e.slotIDs()); err != nil {
		return SequenceOutcome{}, err
	}
	return out, nil
}

// proposedDetail is the detail_json of a proposed row that follows a sequence step: the run and its strategy.
func proposedDetail(o SequenceOutcome) []byte {
	b, _ := json.Marshal(struct {
		SequenceID string `json:"sequence_id"`
		Strategy   string `json:"strategy"`
	}{o.ID, o.Strategy})
	return b
}
