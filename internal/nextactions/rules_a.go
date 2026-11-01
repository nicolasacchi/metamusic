package nextactions

import (
	"context"
	"database/sql"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// rulesA are the four rules of lane NA1 (C139.2).
var rulesA = []Rule{
	{
		Code: "record_proposed_waiting", Priority: 90, Audience: AudienceHuman, MinScope: auth.ScopeRead,
		Find: findRecordProposed, Text: textRecordProposed,
	},
	{
		Code: "muse_unrendered", Priority: 70, Audience: AudienceAgent, MinScope: auth.ScopeRead,
		Find: findMuseUnrendered, Text: textMuseUnrendered, Hint: hintMuseUnrendered,
	},
	{
		Code: "taxonomy_proposals_open", Priority: 70, Audience: AudienceHuman, MinScope: auth.ScopeRead,
		Find: findTaxonomyOpen, Text: textTaxonomyOpen,
	},
	{
		Code: "muse_unjudged", Priority: 65, Audience: AudienceBoth, MinScope: auth.ScopeRead,
		Find: findMuseUnjudged, Text: textMuseUnjudged, Hint: hintGetMuse,
	},
}

// findRecordProposed: a non-hidden Record in status proposed. The proposing agent's name is the actor of its latest proposed
// changelog row; it is private evidence of the text (a token name chosen by the operator, never Muse text).
func findRecordProposed(ctx context.Context, q Reader, lim int) ([]Candidate, error) {
	return queryCandidates(ctx, q, `SELECT r.id,
		COALESCE((SELECT c.actor_name FROM record_changes c WHERE c.record_id = r.id AND c.kind = 'proposed' ORDER BY c.seq DESC LIMIT 1), ''),
		COUNT(*) OVER ()
		FROM records r WHERE r.status = 'proposed' AND r.hidden_at IS NULL ORDER BY r.id LIMIT ?`, []any{lim},
		func(rows *sql.Rows) (Candidate, error) {
			var id, agent string
			var total int
			if err := rows.Scan(&id, &agent, &total); err != nil {
				return Candidate{}, err
			}
			return Candidate{Subject: Subject{Kind: SubjectRecord, ID: &id}, Evidence: map[string]any{"_agent": agent, evTotal: total}}, nil
		})
}

// findTaxonomyOpen: at least one open taxonomy proposal; one library-level item.
func findTaxonomyOpen(ctx context.Context, q Reader, _ int) ([]Candidate, error) {
	return queryCandidates(ctx, q, `SELECT COUNT(*) FROM taxonomy_proposals WHERE state = 'open' HAVING COUNT(*) >= 1`, nil,
		func(rows *sql.Rows) (Candidate, error) {
			var n int
			if err := rows.Scan(&n); err != nil {
				return Candidate{}, err
			}
			return Candidate{Subject: Subject{Kind: SubjectTaxonomy}, Evidence: map[string]any{"open": n}}, nil
		})
}

// findMuseUnrendered: a live Muse with at least one draft Song, no Song with a take and no queued job. The evidence names the
// first draft in angle order for the estimate hint.
func findMuseUnrendered(ctx context.Context, q Reader, lim int) ([]Candidate, error) {
	return queryCandidates(ctx, q, museSongsCTE+`, g AS (SELECT muse_id, COUNT(*) AS songs, SUM(state = 'draft') AS drafts, SUM(state <> 'draft') AS taken, SUM(queued) AS queued
		FROM x GROUP BY muse_id)`+firstDraftCTE+`
		SELECT g.muse_id, g.drafts, f.draft_id, COUNT(*) OVER () FROM g JOIN f ON f.muse_id = g.muse_id
		WHERE g.drafts >= 1 AND g.taken = 0 AND g.queued = 0 ORDER BY g.muse_id LIMIT ?`, []any{LiveMuses(q), lim}, scanMuseDraft)
}

func scanMuseDraft(rows *sql.Rows) (Candidate, error) {
	var id, draft string
	var n, total int
	if err := rows.Scan(&id, &n, &draft, &total); err != nil {
		return Candidate{}, err
	}
	return Candidate{Subject: Subject{Kind: SubjectMuse, ID: &id}, Evidence: map[string]any{"songs": n, "draft_id": draft, evTotal: total}}, nil
}

// findMuseUnjudged: a live Muse with at least one rendered Song and no Song in state judged or picked.
func findMuseUnjudged(ctx context.Context, q Reader, lim int) ([]Candidate, error) {
	return queryCandidates(ctx, q, museSongsCTE+`, g AS (SELECT muse_id, SUM(state = 'rendered') AS rendered, SUM(state IN ('judged','picked')) AS judged
		FROM x GROUP BY muse_id)
		SELECT g.muse_id, g.rendered, COUNT(*) OVER () FROM g WHERE g.rendered >= 1 AND g.judged = 0 ORDER BY g.muse_id LIMIT ?`,
		[]any{LiveMuses(q), lim},
		func(rows *sql.Rows) (Candidate, error) {
			var id string
			var n, total int
			if err := rows.Scan(&id, &n, &total); err != nil {
				return Candidate{}, err
			}
			return Candidate{Subject: Subject{Kind: SubjectMuse, ID: &id}, Evidence: map[string]any{"rendered": n, evTotal: total}}, nil
		})
}

// hintMuseUnrendered is the free estimate of the first draft when the viewer may generate, else the Muse itself.
func hintMuseUnrendered(c Candidate, viewer auth.Principal) *Hint {
	if viewer.Has(auth.ScopeGenerate) {
		if draft := evidenceString(c, "draft_id"); draft != "" {
			return &Hint{Op: "estimate_generation", Args: map[string]any{"items": []any{map[string]any{"mode": "from_draft", "draft_ids": []any{draft}}}}}
		}
	}
	return hintGetMuse(c, viewer)
}

// hintGetMuse points at get_muse for the candidate's Muse.
func hintGetMuse(c Candidate, _ auth.Principal) *Hint {
	return &Hint{Op: "get_muse", Args: map[string]any{"id": subjectID(c.Subject)}}
}
