package sounds

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// ErrTaxonomyStale is returned by Decide, with the expired proposal in the result, when every accepted mapping is no
// longer applicable. The expiry is committed; the API maps the error to 409 TAXONOMY_STALE.
var ErrTaxonomyStale = errors.New("sounds: every accepted mapping is stale")

// Proposal decision limits.
const maxDecisionNoteRunes = 300

// systemSounds is the decider recorded when a proposal expires.
const systemSounds = "system:sounds"

// Decision is the input of Decide: accept (all mappings, or the AcceptMappings indices) or reject.
type Decision struct {
	Decision       string // accept | reject
	AcceptMappings []int  // nil accepts all; otherwise distinct valid indices
	Note           string // optional, at most 300 characters
}

// Skipped names a mapping index a decision did not apply.
type Skipped struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

// DecideResult is the outcome of Decide. Applied is nil unless a version was written.
type DecideResult struct {
	Proposal Proposal
	Applied  *MapResult
	Skipped  []Skipped
}

func humanOnlyDecide() error {
	return problem.E(403, "HUMAN_ONLY", "only the operator decides a taxonomy proposal",
		map[string]any{"hint": "use list_taxonomy_proposals to read the outcome"})
}

func invalidDecision(msg, field string) error {
	return problem.E(422, "VALIDATION_FAILED", msg, map[string]any{"field": field})
}

// Decide accepts or rejects an open proposal (C140.3). A principal that does not act as the operator is refused with
// 403 HUMAN_ONLY before anything is read. Accepting re-validates each chosen mapping against the effective index now:
// an add already effective or a remove no longer effective is skipped as stale. When none is left the proposal
// expires (committed) and ErrTaxonomyStale is returned with the expired proposal. Otherwise one extension version
// (note "proposal <id>") and the accepted state are written in one transaction, then every stale subject is recomputed
// before the call returns.
func (s *Service) Decide(ctx context.Context, p auth.Principal, id string, d Decision) (DecideResult, error) {
	if !p.ActsAsHuman() {
		return DecideResult{}, humanOnlyDecide()
	}
	if d.Decision != "accept" && d.Decision != "reject" {
		return DecideResult{}, invalidDecision("decision must be accept or reject", "decision")
	}
	if utf8.RuneCountInString(d.Note) > maxDecisionNoteRunes {
		return DecideResult{}, invalidDecision(fmt.Sprintf("note must be at most %d characters", maxDecisionNoteRunes), "note")
	}
	var note any
	if d.Note != "" {
		note = d.Note
	}
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return DecideResult{}, fmt.Errorf("sounds: decide: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	pr, ok, err := proposalWhere(ctx, tx, `id = ?`, id)
	if err != nil {
		return DecideResult{}, err
	}
	if !ok {
		return DecideResult{}, problem.E(404, "NOT_FOUND", "taxonomy proposal not found", map[string]any{"proposal_id": id})
	}
	if pr.State != ProposalOpen {
		return DecideResult{}, problem.E(409, "TAXONOMY_PROPOSAL_CLOSED", "the taxonomy proposal is already decided", map[string]any{"proposal_id": id, "state": pr.State})
	}
	now := store.FormatTime(s.now())

	if d.Decision == "reject" {
		if _, err := tx.ExecContext(ctx, `UPDATE taxonomy_proposals SET state = 'rejected', decided_at = ?, decided_by = ?, decided_note = ? WHERE id = ?`, now, p.Key(), note, id); err != nil {
			return DecideResult{}, fmt.Errorf("sounds: decide reject: %w", err)
		}
		out, err := commitDecided(ctx, tx, id)
		return DecideResult{Proposal: out}, err
	}

	idx, err := acceptedIndices(d.AcceptMappings, len(pr.Mappings))
	if err != nil {
		return DecideResult{}, err
	}
	cur, err := currentVersion(ctx, tx)
	if err != nil {
		return DecideResult{}, err
	}
	rows, err := loadTermRows(ctx, tx, cur)
	if err != nil {
		return DecideResult{}, err
	}
	ix := BuildIndex(cur, rows)
	skipped := []Skipped{}
	var apply []Mapping
	var applied []int
	for _, i := range idx {
		m := pr.Mappings[i]
		if has := ix.Has(m.Term, m.Facet, m.Value); (m.Op == "add") == has {
			skipped = append(skipped, Skipped{Index: i, Reason: "stale"})
			continue
		}
		apply = append(apply, m)
		applied = append(applied, i)
	}
	if len(apply) == 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE taxonomy_proposals SET state = 'expired', decided_at = ?, decided_by = ?, skipped_json = ? WHERE id = ?`,
			now, systemSounds, jsonString(skipped), id); err != nil {
			return DecideResult{}, fmt.Errorf("sounds: decide expire: %w", err)
		}
		out, err := commitDecided(ctx, tx, id)
		if err != nil {
			return DecideResult{}, err
		}
		return DecideResult{Proposal: out, Skipped: skipped}, ErrTaxonomyStale
	}
	versionNote := "proposal " + id
	res, wrote, err := s.applyTx(ctx, tx, p, apply, &versionNote)
	if err != nil {
		return DecideResult{}, err
	}
	if !wrote || res.Version == nil {
		return DecideResult{}, fmt.Errorf("sounds: decide: no version written for %d effective mappings", len(apply))
	}
	if _, err := tx.ExecContext(ctx, `UPDATE taxonomy_proposals SET state = 'accepted', decided_at = ?, decided_by = ?, decided_note = ?,
		accepted_json = ?, skipped_json = ?, applied_version = ? WHERE id = ?`,
		now, p.Key(), note, jsonString(applied), jsonString(skipped), *res.Version, id); err != nil {
		return DecideResult{}, fmt.Errorf("sounds: decide accept: %w", err)
	}
	out, err := commitDecided(ctx, tx, id)
	if err != nil {
		return DecideResult{}, err
	}
	ch, err := s.recompute(ctx, p)
	res.RecomputedTakes, res.RecomputedSongs = ch.Takes, ch.Songs
	return DecideResult{Proposal: out, Applied: &res, Skipped: skipped}, err
}

// commitDecided re-reads the decided row inside tx and commits.
func commitDecided(ctx context.Context, tx *sql.Tx, id string) (Proposal, error) {
	out, ok, err := proposalWhere(ctx, tx, `id = ?`, id)
	if err != nil {
		return Proposal{}, err
	}
	if !ok {
		return Proposal{}, fmt.Errorf("sounds: decided proposal %s vanished", id)
	}
	if err := tx.Commit(); err != nil {
		return Proposal{}, fmt.Errorf("sounds: decide commit: %w", err)
	}
	return out, nil
}

// acceptedIndices resolves accept_mappings: absent is every index; otherwise distinct valid indices, applied in order.
func acceptedIndices(sel []int, n int) ([]int, error) {
	if sel == nil {
		all := make([]int, n)
		for i := range all {
			all[i] = i
		}
		return all, nil
	}
	if len(sel) == 0 {
		return nil, invalidDecision("accept_mappings names no mapping", "accept_mappings")
	}
	out := slices.Clone(sel)
	slices.Sort(out)
	for i, v := range out {
		if v < 0 || v >= n || (i > 0 && out[i-1] == v) {
			return nil, invalidDecision("accept_mappings must be distinct indices of the proposal's mappings", "accept_mappings")
		}
	}
	return out, nil
}
