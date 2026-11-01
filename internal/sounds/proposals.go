package sounds

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Limits of taxonomy proposals (C140.1).
const (
	ProposalMaxOpenPerPrincipal = 10
	ProposalMaxOpen             = 50
	ProposalMaxRationaleRunes   = 500
)

// Proposal states.
const (
	ProposalOpen     = "open"
	ProposalAccepted = "accepted"
	ProposalRejected = "rejected"
	ProposalExpired  = "expired"
)

// Proposal is one row of taxonomy_proposals. Rationale and the terms of Mappings are agent-authored text (C140.5).
type Proposal struct {
	ID, State         string
	BaseVersion       int
	Rationale         string
	Mappings          []Mapping
	Impact            Impact
	ProposedBy        auth.PrincipalRef // the stored key, kind and name; the handler renders it for the viewer
	CreatedAt         string
	DecidedAt         *string
	DecidedBy         *string // Principal.Key() of the human, or system:sounds when the proposal expired
	DecidedNote       *string
	Accepted, Skipped json.RawMessage // accepted mapping indices; [{index, reason}]; nil until decided
	AppliedVersion    *int
	Reused            bool
}

// wireMapping is the stored shape of a mapping in mappings_json (the shipped MapEntry has no JSON tags).
type wireMapping struct {
	Term  string `json:"term"`
	Facet string `json:"facet"`
	Value string `json:"value"`
	Op    string `json:"op"`
}

func toWire(ms []Mapping) []wireMapping {
	out := make([]wireMapping, len(ms))
	for i, m := range ms {
		out[i] = wireMapping(m)
	}
	return out
}

// mappingsDigest is the digest of the canonical mapping set: the entries ordered by (term, facet, value, op).
func mappingsDigest(ms []Mapping) string {
	w := toWire(ms)
	slices.SortFunc(w, func(a, b wireMapping) int {
		return strings.Compare(a.Term+"\x00"+a.Facet+"\x00"+a.Value+"\x00"+a.Op, b.Term+"\x00"+b.Facet+"\x00"+b.Value+"\x00"+b.Op)
	})
	return sum256(jsonString(w))
}

func humanOnlyPropose() error {
	return problem.E(403, "HUMAN_ONLY", "an agent proposes taxonomy changes; the operator maps terms directly",
		map[string]any{"hint": "use map_sound_term"})
}

// Propose stores a taxonomy proposal of an agent (C140.1). The mappings are validated as Map validates them against
// the current effective index; an add that is already effective is dropped (the stored mappings are what remains), and
// nothing remaining is 422 NOTHING_TO_PROPOSE. An open proposal of the same principal with the same mapping set is
// returned unchanged with Reused set. A human principal is refused: it maps terms directly.
func (s *Service) Propose(ctx context.Context, p auth.Principal, rationale string, ms []Mapping) (Proposal, error) {
	if p.ActsAsHuman() {
		return Proposal{}, humanOnlyPropose()
	}
	rationale = strings.TrimSpace(rationale)
	if n := utf8.RuneCountInString(rationale); n < 1 || n > ProposalMaxRationaleRunes {
		return Proposal{}, problem.E(422, "VALIDATION_FAILED", fmt.Sprintf("rationale must be 1..%d characters", ProposalMaxRationaleRunes), map[string]any{"field": "rationale"})
	}
	entries, err := validateMap(MapRequest{Mappings: ms})
	if err != nil {
		return Proposal{}, err
	}
	seen := map[mapKey]bool{}
	for i, e := range entries {
		k := mapKey{e.Term, e.Facet, e.Value}
		if seen[k] {
			return Proposal{}, problem.E(422, "DUPLICATE_MAPPING", "two mappings name the same term, facet and value", map[string]any{"index": i})
		}
		seen[k] = true
	}
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return Proposal{}, fmt.Errorf("sounds: propose: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	cur, err := currentVersion(ctx, tx)
	if err != nil {
		return Proposal{}, err
	}
	rows, err := loadTermRows(ctx, tx, cur)
	if err != nil {
		return Proposal{}, err
	}
	ix := BuildIndex(cur, rows)
	var keep []Mapping
	for i, e := range entries {
		has := ix.Has(e.Term, e.Facet, e.Value)
		switch {
		case e.Op == "add" && has:
			// already effective: dropped
		case e.Op == "remove" && !has:
			return Proposal{}, problem.E(422, "MAPPING_NOT_EFFECTIVE", "the mapping to remove is not effective", map[string]any{"index": i, "facet": e.Facet, "value": e.Value})
		default:
			keep = append(keep, e)
		}
	}
	if len(keep) == 0 {
		return Proposal{}, problem.E(422, "NOTHING_TO_PROPOSE", "every mapping is already effective", nil)
	}
	digest := mappingsDigest(keep)
	if old, ok, err := proposalWhere(ctx, tx, `state = 'open' AND principal_id = ? AND mappings_digest = ?`, p.Key(), digest); err != nil {
		return Proposal{}, err
	} else if ok {
		old.Reused = true
		return old, nil
	}
	var mine, all int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(principal_id = ?), 0), COUNT(*) FROM taxonomy_proposals WHERE state = 'open'`, p.Key()).Scan(&mine, &all); err != nil {
		return Proposal{}, fmt.Errorf("sounds: propose limits: %w", err)
	}
	switch {
	case mine >= ProposalMaxOpenPerPrincipal:
		return Proposal{}, problem.E(409, "TAXONOMY_PROPOSAL_LIMIT", "this principal has too many open taxonomy proposals", map[string]any{"scope": "principal", "limit": ProposalMaxOpenPerPrincipal})
	case all >= ProposalMaxOpen:
		return Proposal{}, problem.E(409, "TAXONOMY_PROPOSAL_LIMIT", "too many open taxonomy proposals", map[string]any{"scope": "total", "limit": ProposalMaxOpen})
	}
	imp, err := s.impactIn(ctx, tx, keep)
	if err != nil {
		return Proposal{}, err
	}
	name := p.Name
	if name == "" {
		name = p.ID
	}
	if utf8.RuneCountInString(name) > 120 {
		name = string([]rune(name)[:120])
	}
	pr := Proposal{
		ID: s.newID(), State: ProposalOpen, BaseVersion: cur, Rationale: rationale, Mappings: keep, Impact: imp,
		ProposedBy: auth.PrincipalRef{Kind: string(p.Kind), ID: p.Key(), Name: name}, CreatedAt: store.FormatTime(s.now()),
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO taxonomy_proposals(id, state, base_version, rationale, mappings_json, mappings_digest, impact_json,
		principal_id, principal_kind, principal_name, created_at) VALUES(?, 'open', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pr.ID, cur, rationale, jsonString(toWire(keep)), digest, jsonString(imp), pr.ProposedBy.ID, pr.ProposedBy.Kind, name, pr.CreatedAt); err != nil {
		return Proposal{}, fmt.Errorf("sounds: propose insert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Proposal{}, fmt.Errorf("sounds: propose commit: %w", err)
	}
	return pr, nil
}

const proposalCols = `id, state, base_version, rationale, mappings_json, impact_json, principal_id, principal_kind, principal_name,
	created_at, decided_at, decided_by, decided_note, accepted_json, skipped_json, applied_version`

// proposalWhere reads the first proposal matching a condition (newest first).
func proposalWhere(ctx context.Context, q querier, cond string, args ...any) (Proposal, bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+proposalCols+` FROM taxonomy_proposals WHERE `+cond+` ORDER BY created_at DESC, id DESC LIMIT 1`, args...)
	if err != nil {
		return Proposal{}, false, fmt.Errorf("sounds: proposal: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return Proposal{}, false, rows.Err()
	}
	p, err := scanProposal(rows)
	return p, err == nil, err
}

func scanProposal(rows *sql.Rows) (Proposal, error) {
	var p Proposal
	var mappings, impact string
	var decidedAt, decidedBy, decidedNote, accepted, skipped sql.NullString
	var applied sql.NullInt64
	if err := rows.Scan(&p.ID, &p.State, &p.BaseVersion, &p.Rationale, &mappings, &impact, &p.ProposedBy.ID, &p.ProposedBy.Kind, &p.ProposedBy.Name,
		&p.CreatedAt, &decidedAt, &decidedBy, &decidedNote, &accepted, &skipped, &applied); err != nil {
		return p, fmt.Errorf("sounds: scan proposal: %w", err)
	}
	var w []wireMapping
	if err := json.Unmarshal([]byte(mappings), &w); err != nil {
		return p, fmt.Errorf("sounds: proposal mappings: %w", err)
	}
	p.Mappings = make([]Mapping, len(w))
	for i, m := range w {
		p.Mappings[i] = Mapping(m)
	}
	if err := json.Unmarshal([]byte(impact), &p.Impact); err != nil {
		return p, fmt.Errorf("sounds: proposal impact: %w", err)
	}
	p.Impact.Gain, p.Impact.Loss, p.Impact.UnmappedCleared = nonNil(p.Impact.Gain), nonNil(p.Impact.Loss), nonNil(p.Impact.UnmappedCleared)
	if decidedAt.Valid {
		p.DecidedAt = &decidedAt.String
	}
	if decidedBy.Valid {
		p.DecidedBy = &decidedBy.String
	}
	if decidedNote.Valid {
		p.DecidedNote = &decidedNote.String
	}
	if accepted.Valid {
		p.Accepted = json.RawMessage(accepted.String)
	}
	if skipped.Valid {
		p.Skipped = json.RawMessage(skipped.String)
	}
	if applied.Valid {
		v := int(applied.Int64)
		p.AppliedVersion = &v
	}
	return p, nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
