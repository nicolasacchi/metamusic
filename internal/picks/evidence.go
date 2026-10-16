package picks

import (
	"encoding/json"
	"fmt"
)

// pinFacts are the pin columns of song_picks as a recompute sees them (after an explicit pin or unpin was applied).
type pinFacts struct {
	TakeID *string
	By     *string
	At     *string
}

// finalEvidence completes the Derive evidence with the facts only the service knows (C91.3): who pinned and when, and
// the accepted proposal. proposalID is carried over from the stored evidence when the same pin is refreshed.
func finalEvidence(r Result, pin pinFacts, proposalID *string) (json.RawMessage, error) {
	ev := r.Evidence
	if r.Source == SourcePinned {
		ev.PinnedBy, ev.PinnedAt, ev.ProposalID = pin.By, pin.At, proposalID
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return nil, fmt.Errorf("picks: marshal evidence: %w", err)
	}
	return b, nil
}

// storedProposalID reads the accepted proposal id of a stored pinned evidence document ("" when absent).
func storedProposalID(raw string) *string {
	var e struct {
		ProposalID *string `json:"proposal_id"`
	}
	if json.Unmarshal([]byte(raw), &e) != nil {
		return nil
	}
	return e.ProposalID
}
