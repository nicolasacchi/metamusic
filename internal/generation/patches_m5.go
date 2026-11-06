package generation

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/lineage"
)

// The song_pr and backlog call sites (C253.4, C255.5). Everything here is nil-safe: no PatchSource refuses song_pr with 503, no
// BacklogGate lets everything through. Estimate runs these checks through the reader pool, create through its own transaction, so
// a Song PR whose comments changed, whose render budget is spent or whose lineage waits for the human is refused at both ends.

func unavailableErr(component string) error {
	return perr(http.StatusServiceUnavailable, "UNAVAILABLE", component+" is not available", map[string]any{"component": component})
}

// patchSourceErr maps a source that is not wired (the late delegate answers errors.ErrUnsupported) onto 503.
func patchSourceErr(err error) error {
	if errors.Is(err, errors.ErrUnsupported) {
		return unavailableErr("songpr")
	}
	return err
}

func renderLimitErr(patchID string, n int) error {
	return perr(http.StatusUnprocessableEntity, "PATCH_RENDER_LIMIT", "this Song PR has been rendered the most times it may be",
		map[string]any{"patch_id": patchID, "renders": n, "limit": PatchMaxRenders})
}

func backlogErr(root string, pending, limit int) error {
	return perr(http.StatusConflict, "CALLBACKS_BACKLOG", "callbacks of this lineage wait for the human; answer them before more is generated",
		map[string]any{"root_track_id": root, "pending": pending, "limit": limit})
}

// checkPatch re-validates one Song PR: its comments are still addressable and its render budget is not spent.
func (s *Service) checkPatch(ctx context.Context, q lineage.Querier, id string) error {
	src := s.o.PatchSource
	if src == nil {
		return unavailableErr("songpr")
	}
	if err := src.RecheckComments(ctx, q, id); err != nil {
		return patchSourceErr(err)
	}
	n, err := src.RenderCount(ctx, q, id)
	if err != nil {
		return patchSourceErr(err)
	}
	if n >= PatchMaxRenders {
		return renderLimitErr(id, n)
	}
	return nil
}

// resolvePatches loads the Song PR of every song_pr item, fills in the item's parent (the patch's base; a parent that is not the base
// is an invalid item) and re-validates the patch. It returns a copy of the items and the rows the planner reads; other items pass
// through untouched.
func (s *Service) resolvePatches(ctx context.Context, q lineage.Querier, items []Item) ([]Item, map[string]PatchRow, error) {
	out := slices.Clone(items)
	rows := map[string]PatchRow{}
	for i, it := range out {
		if it.Mode != lineage.ModeSongPR {
			continue
		}
		src := s.o.PatchSource
		if src == nil {
			return nil, nil, unavailableErr("songpr")
		}
		if it.PatchID == nil || *it.PatchID == "" {
			return nil, nil, invalidItem(i, "patch_id", "required")
		}
		id := *it.PatchID
		row, done := rows[id]
		if !done {
			var err error
			if row, err = src.PatchForEstimate(ctx, q, id); err != nil {
				return nil, nil, patchSourceErr(err)
			}
			if err = s.checkPatch(ctx, q, id); err != nil {
				return nil, nil, err
			}
			rows[id] = row
		}
		if it.ParentTrackID != nil && *it.ParentTrackID != "" && *it.ParentTrackID != row.BaseTrackID {
			return nil, nil, invalidItem(i, "parent_track_id", "not_the_patch_base")
		}
		base := row.BaseTrackID
		out[i].ParentTrackID = &base
	}
	return out, rows, nil
}

// gateBacklog refuses an agent's work on a lineage whose callbacks wait for the human (D-M5-R3-3): 409 CALLBACKS_BACKLOG at
// pending >= Limit. parents are the mother tracks of the planned takes; humans and a limit of 0 pass.
func (s *Service) gateBacklog(ctx context.Context, q lineage.Querier, p auth.Principal, parents []string) error {
	g := s.o.BacklogGate
	if g == nil || p.Kind != auth.KindAgent {
		return nil
	}
	limit := g.Limit()
	if limit <= 0 {
		return nil
	}
	var roots []string
	for _, id := range parents {
		var root string
		err := q.QueryRowContext(ctx, `SELECT root_track_id FROM tracks WHERE id = ?`, id).Scan(&root)
		if err != nil {
			return fmt.Errorf("generation: root of %s: %w", id, err)
		}
		if !slices.Contains(roots, root) {
			roots = append(roots, root)
		}
	}
	for _, root := range roots {
		n, err := g.Backlog(ctx, q, root)
		if err != nil {
			return err
		}
		if n >= limit {
			return backlogErr(root, n, limit)
		}
	}
	return nil
}

// patchWarnings is the estimate warning comment_in_flight (C253.4): comments this Song PR addresses that another Song PR with
// unfinished renders addresses too. One warning per take; it names no comment text.
func (p *planner) patchWarnings(patchID string) []Issue {
	n := len(p.inFlight[patchID])
	if n == 0 {
		return nil
	}
	return []Issue{{Code: "comment_in_flight", Field: "patch_id", Severity: "warning",
		Message: fmt.Sprintf("%d addressed comment(s) are also addressed by another Song PR that is still rendering", n)}}
}

// loadInFlight fills planner.inFlight for the resolved patches.
func (p *planner) loadInFlight() error {
	p.inFlight = map[string][]string{}
	for id, row := range p.patches {
		for _, c := range row.CommentIDs {
			var n int
			err := p.q.QueryRowContext(p.ctx, `SELECT EXISTS(SELECT 1 FROM patch_comments pc JOIN jobs j ON j.patch_id = pc.patch_id
				WHERE pc.comment_id = ? AND pc.patch_id <> ? AND j.state NOT IN `+terminalJobStates+`)`, c, id).Scan(&n) //nolint:gosec // G202: constant state list
			if err != nil {
				return fmt.Errorf("generation: comment in flight: %w", err)
			}
			if n == 1 {
				p.inFlight[id] = append(p.inFlight[id], c)
			}
		}
	}
	return nil
}

// recheckPatches is create's step 5b: every Song PR of the estimate is re-validated through the transaction, and the lineage
// backlog is checked for agents.
func (s *Service) recheckPatches(ctx context.Context, tx lineage.Querier, p auth.Principal, e estimateRow) error {
	seen := map[string]bool{}
	var parents []string
	for _, t := range e.Items {
		if t.PatchID != nil && !seen[*t.PatchID] {
			seen[*t.PatchID] = true
			if err := s.checkPatch(ctx, tx, *t.PatchID); err != nil {
				return err
			}
		}
		if t.ParentTrackID != nil {
			parents = append(parents, *t.ParentTrackID)
		}
	}
	return s.gateBacklog(ctx, tx, p, parents)
}
