package generation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/lineage"
)

// The render_plan item mode (M5b C360, D-DESK-2): the take renders the score text of a stored Desk plan. Its recipe is the plan's
// request with abc = the score bytes, the plan's cot and its seed; it travels as a plain mm.worker/1 job (no needs, so the
// capability gate is never read), is an ordinary spend (estimate, approval, create) and creates a NEW recipe and take: a plan, its
// file and every earlier recipe stay as they are. Everything here is reached only by an item of this mode.

// Desk item limits (D-DESK-18).
const (
	maxRenderPlans = 10
	maxPlanSeeds   = 4 // seeds per plan of one render_plan item
)

// Desk warning codes of a render_plan take (C360.3). Stable API values.
const (
	WarnPlanTruncated = "plan_truncated"
	WarnPlanUntimed   = "plan_untimed"
	WarnPlanEdited    = "plan_edited"
)

const (
	deskField    = "desk"
	planIDsField = "desk.plan_ids"
	planTitleSep = " · plan s"
)

// validateDeskShape is the static shape of an item as far as the Desk is concerned. A Desk object on any mode but render_plan is
// INVALID_ITEM rule field_forbidden; a render_plan item names its plans (1..10, unique) and nothing else of the Desk, takes no
// mother, draft, recipe, template, edit or reference (the plan names its own base), and renders at most 4 seeds of each plan.
func validateDeskShape(i int, it Item, eng engine.Engine) error {
	if it.Mode != lineage.ModeRenderPlan {
		if it.Desk != nil {
			return invalidItem(i, deskField, "field_forbidden")
		}
		return nil
	}
	if !eng.Caps().RenderFromScore {
		return engineCapabilityErr(eng.Name(), it.Mode, "render_from_score")
	}
	d := it.Desk
	if d == nil {
		return invalidItem(i, deskField, "required")
	}
	hasParent := it.ParentTrackID != nil && *it.ParentTrackID != ""
	switch {
	case hasParent || len(it.ParentTrackIDs) > 0 || len(it.DraftIDs) > 0 || len(it.Recipe) > 0 || it.TemplateMode != "" || it.Edit != nil ||
		it.Reference != nil || it.EnginePatch != nil:
		return invalidItem(i, "mode", "render_plan_fields")
	case d.NSeeds != 0 || len(d.Seeds) > 0 || d.Cot != "":
		return invalidItem(i, deskField, "field_forbidden")
	case len(d.PlanIDs) == 0:
		return invalidItem(i, planIDsField, "plan_ids_required")
	case len(d.PlanIDs) > maxRenderPlans:
		return invalidItem(i, planIDsField, "plan_ids_at_most_10")
	case it.NSeeds > maxPlanSeeds:
		return invalidItem(i, "n_seeds", "render_plan_at_most_4")
	case len(it.Seeds) > maxPlanSeeds:
		return invalidItem(i, "seeds", "render_plan_at_most_4")
	}
	return uniqueStrings(i, planIDsField, d.PlanIDs)
}

// planNotFound is the 404 of an unknown plan id when a PlanSource does not answer one itself.
func planNotFound(id string) error {
	return perr(http.StatusNotFound, "NOT_FOUND", "plan not found", map[string]any{"plan_id": id})
}

// resolvePlans loads the plans of every render_plan item in one call. It returns nil, nil for an estimate with no such item, which
// never reads the source; a nil source is 503 UNAVAILABLE {component: "desk"}.
func (s *Service) resolvePlans(ctx context.Context, q lineage.Querier, items []Item) (map[string]PlanRow, error) {
	var ids []string
	seen := map[string]bool{}
	for _, it := range items {
		if it.Mode != lineage.ModeRenderPlan || it.Desk == nil {
			continue
		}
		for _, id := range it.Desk.PlanIDs {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	src := s.o.PlanSource
	if src == nil {
		return nil, unavailableErr("desk")
	}
	rows, err := src.PlansForEstimate(ctx, q, ids)
	if errors.Is(err, errors.ErrUnsupported) {
		return nil, unavailableErr("desk")
	}
	if err != nil {
		return nil, err
	}
	out := make(map[string]PlanRow, len(rows))
	for _, r := range rows {
		out[r.ID] = r
	}
	for _, id := range ids {
		if _, ok := out[id]; !ok {
			return nil, planNotFound(id)
		}
	}
	return out, nil
}

// deskMothers lists the base takes of the plans the items render, as parent-only items, so the mother checks and the callback
// backlog gate treat them like the mother of any variant.
func deskMothers(items []Item, plans map[string]PlanRow) []Item {
	var out []Item
	seen := map[string]bool{}
	for _, it := range items {
		if it.Mode != lineage.ModeRenderPlan || it.Desk == nil {
			continue
		}
		for _, id := range it.Desk.PlanIDs {
			b := plans[id].BaseTrackID
			if b == nil || seen[*b] {
				continue
			}
			seen[*b] = true
			out = append(out, Item{ParentTrackID: b})
		}
	}
	return out
}

// planRequest is the request a plan renders: the plan's canonical request with abc = the score bytes and the plan's cot and seed.
func planRequest(r PlanRow) (string, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(r.Request, &m); err != nil || m == nil {
		return "", fmt.Errorf("generation: plan %s request: %w", r.ID, errors.New("not a JSON object"))
	}
	delete(m, "abc_path")
	var err error
	for k, v := range map[string]any{"abc": string(r.Score), "cot": r.Cot, "seed": r.Seed} {
		if m[k], err = json.Marshal(v); err != nil {
			return "", err
		}
	}
	b, err := json.Marshal(m)
	return string(b), err
}

// expandRenderPlan plans the takes of one render_plan item: per plan, the plan's own seed plus n-1 new ones (C360.1). The take's
// parent is the plan's base take, or none (a root) when the plan has no base.
func (p *planner) expandRenderPlan(i int, it Item) error {
	for _, id := range it.Desk.PlanIDs {
		row := p.plans[id]
		raw, err := planRequest(row)
		if err != nil {
			return err
		}
		rec, err := p.parseStored(raw)
		if err != nil {
			return invalidItem(i, planIDsField, "plan_request_invalid")
		}
		spec, _ := lineage.SpecFor(lineage.ModeRenderPlan, p.engName)
		b := base{item: i, it: it, mode: lineage.ModeRenderPlan, spec: spec, rec: rec, plan: &row}
		if row.BaseTrackID != nil {
			t, err := p.track(*row.BaseTrackID)
			if err != nil {
				return err
			}
			b.parent = &t
		}
		if err := p.expandBase(b); err != nil {
			return err
		}
	}
	return nil
}

// planFloor raises the engine's expected length of a render_plan take to the plan's score-timed length: a take is never quoted
// shorter than the score the Desk showed. Any other base is returned unchanged.
func (b base) planFloor(secs int) int {
	if b.plan == nil {
		return secs
	}
	return max(secs, b.plan.ExpectedAudioS)
}

// finishPlanTake records what makes a take a render of a plan: params.plan_id, the title and the plan warnings.
func (p *planner) finishPlanTake(b base, t *planTake) {
	id := b.plan.ID
	t.Params.PlanID = &id
	if b.it.Title == nil {
		title := b.plan.Title
		if b.parent != nil {
			title = b.parent.Title
		}
		t.Title = planTitle(title, t.Seed)
	}
	if b.plan.Truncated {
		t.Warnings = append(t.Warnings, warnIssue(WarnPlanTruncated, planIDsField, "the planning pass hit its token limit: the score may end before the song does"))
	}
	if !b.plan.Timed {
		t.Warnings = append(t.Warnings, warnIssue(WarnPlanUntimed, planIDsField, "the score has no Q: tempo line: its length is a guess"))
	}
	if b.plan.Source == "edit" {
		t.Warnings = append(t.Warnings, warnIssue(WarnPlanEdited, planIDsField, "the score was edited after it was planned: the model may sing it differently"))
	}
}

// planTitle is "<base title> · plan s<seed>", the base title cut so the whole stays within the 200-rune title limit.
func planTitle(base string, seed int64) string {
	suffix := fmt.Sprintf("%s%d", planTitleSep, seed)
	if n := maxTitleRunes - utf8.RuneCountInString(suffix); utf8.RuneCountInString(base) > n {
		base = string([]rune(base)[:max(n, 0)])
	}
	return base + suffix
}
