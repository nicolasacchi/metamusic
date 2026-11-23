package generation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/jobspec"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/spend"
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
		if it.Mode == lineage.ModePlanSeeds {
			return nil // plan_seeds has its own shape (validatePlanSeedsItem), checked right after this
		}
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

// ---- plan_seeds (DESK-M2, C360.3): the GPU planning job ----

// Plan job limits (D-DESK-3, C360.5).
const (
	defaultPlanSeeds = 8
	maxJobPlanSeeds  = jobspec.MaxSeeds // desk.n_seeds and desk.seeds, 1..16
)

// WarnPlanSplit says one plan_seeds item became several jobs because its seeds do not fit one job's runtime (C360.5).
const WarnPlanSplit = "plan_split"

const (
	deskNSeedsField = "desk.n_seeds"
	deskSeedsField  = "desk.seeds"
	deskCotField    = "desk.cot"
	planTitleSuffix = " · plans ×"
)

// validatePlanSeedsItem is the static shape of a plan_seeds item: the engine must plan scores (else 422 ENGINE_CAPABILITY); exactly
// one base (parent_track_id, one draft id or a recipe); no plan ids, mother list, template, edit, reference, title or item-level
// seeds (the seeds are desk.n_seeds 1..16 or desk.seeds 1..16); the patch follows the mode row (abc only as null is the planner's,
// rule plan_requires_no_abc, because it depends on the base).
func validatePlanSeedsItem(i int, it Item, eng engine.Engine) error {
	if !eng.Caps().PlanScore {
		return engineCapabilityErr(eng.Name(), it.Mode, "plan_score")
	}
	hasParent := it.ParentTrackID != nil && *it.ParentTrackID != ""
	if len(it.ParentTrackIDs) > 0 || it.TemplateMode != "" || it.Edit != nil || it.Reference != nil || it.EnginePatch != nil ||
		it.PatchID != nil || len(it.TrackIDs) > 0 || it.Title != nil || it.NSeeds != 0 || len(it.Seeds) > 0 {
		return invalidItem(i, "mode", "plan_seeds_fields")
	}
	bases := 0
	for _, on := range []bool{hasParent, len(it.DraftIDs) > 0, len(it.Recipe) > 0} {
		if on {
			bases++
		}
	}
	switch {
	case bases != 1:
		return invalidItem(i, "mode", "plan_seeds_one_base")
	case len(it.DraftIDs) > 1:
		return invalidItem(i, "draft_ids", "plan_seeds_one_draft")
	}
	if f, r := checkPatchFor(lineage.ModeSpec{Patch: true, Style: lineage.Optional, Lyrics: lineage.Optional, Cot: lineage.Optional, ABC: lineage.Optional},
		eng.Name(), it); f != "" {
		return invalidItem(i, f, r)
	}
	d := it.Desk
	if d == nil {
		return nil
	}
	switch {
	case len(d.PlanIDs) > 0:
		return invalidItem(i, planIDsField, "field_forbidden")
	case d.NSeeds < 0 || d.NSeeds > maxJobPlanSeeds:
		return invalidItem(i, deskNSeedsField, "range_1_to_16")
	case d.NSeeds > 0 && len(d.Seeds) > 0:
		return invalidItem(i, deskSeedsField, "n_seeds_or_seeds")
	case len(d.Seeds) > maxJobPlanSeeds:
		return invalidItem(i, deskSeedsField, "at_most_16")
	case d.Cot != "" && d.Cot != "full" && d.Cot != "melody":
		return invalidItem(i, deskCotField, "cot_invalid")
	}
	seen := map[int64]bool{}
	for _, sd := range d.Seeds {
		switch {
		case sd < 0 || sd > lineage.MaxSeed:
			return invalidItem(i, deskSeedsField, "out_of_range")
		case seen[sd]:
			return invalidItem(i, deskSeedsField, "duplicate")
		}
		seen[sd] = true
	}
	return nil
}

// planSecondsPerSeed is s_per_seed = ceil(PlanMilli x A / 1000): the planning seconds one seed of A audio seconds costs.
func planSecondsPerSeed(audioS int) int {
	return int((int64(spend.PlanMilli)*int64(audioS) + 999) / 1000)
}

// seedsPerJob is the largest count of seeds whose quote still fits the runtime cap: the planning phase takes LoadS + n x s_per_seed
// seconds and the timeout is ceil(1.6 x that) + 80, so n x s_per_seed <= (MaxRuntimeS - 80) x 5/8 - LoadS (C360.5). It is 1..16.
func seedsPerJob(audioS, maxRuntimeS int) int {
	room := int64(maxRuntimeS-spend.WorkerFinishMarginS-spend.UploadBudgetS-spend.TimeoutSlackS)*5 - 8*int64(spend.LoadS)
	n := room / (8 * int64(planSecondsPerSeed(audioS)))
	return int(min(max(n, 1), maxJobPlanSeeds))
}

// splitSeeds cuts seeds into the fewest chunks of at most per seeds, of near-equal size (the larger chunks first).
func splitSeeds(seeds []int64, per int) [][]int64 {
	jobs := (len(seeds) + per - 1) / per
	var out [][]int64
	size, extra := len(seeds)/jobs, len(seeds)%jobs
	for at, k := 0, 0; k < jobs; k++ {
		n := size
		if k < extra {
			n++
		}
		out = append(out, seeds[at:at+n])
		at += n
	}
	return out
}

// planJobSeeds lists the seeds of the live and finished plan jobs of a recipe, so a repeat of plan_seeds on the same song plans new
// seeds instead of the same scores again (a failed, cancelled or expired job planned nothing).
func planJobSeeds(ctx context.Context, q lineage.Querier, recipeID string) ([]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT s.value FROM jobs j, json_each(j.spec_json, '$.plan.seeds') s
		WHERE j.kind = 'plan' AND j.recipe_id = ? AND j.state NOT IN ('failed','cancelled','expired')`, recipeID)
	if err != nil {
		return nil, fmt.Errorf("generation: plan job seeds: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// planSeedsBase is what the three kinds of base give the planner: the recipe, the mother track, the draft and a title to show.
type planSeedsBase struct {
	rec    engine.Recipe
	parent *trackInfo
	draft  *draftInfo
	label  string
	failed bool // the base did not lint; the failure is recorded
}

func (p *planner) planSeedsBase(i int, it Item) (planSeedsBase, error) {
	var b planSeedsBase
	switch {
	case it.ParentTrackID != nil && *it.ParentTrackID != "":
		t, err := p.track(*it.ParentTrackID)
		if err != nil {
			return b, err
		}
		if b.rec, err = p.parseStored(t.RequestJSON); err != nil {
			return b, fmt.Errorf("generation: parent recipe of %s: %w", t.ID, err)
		}
		b.parent, b.label = &t, t.Title
	case len(it.DraftIDs) == 1:
		d := p.drafts[it.DraftIDs[0]]
		rec, err := p.parseStored(d.RequestJSON)
		if err != nil {
			return b, fmt.Errorf("generation: draft %s: %w", d.ID, err)
		}
		b.rec, b.draft, b.label = rec, &d, d.Label
		if d.ParentTrackID != nil { // the draft's mother is the plan's base take
			t, err := p.track(*d.ParentTrackID)
			if err != nil {
				return b, err
			}
			b.parent = &t
		}
	default:
		raw := []byte(it.Recipe)
		errs, warns := lineage.Lint(raw)
		if len(errs) > 0 {
			p.failures = append(p.failures, lintFailure{Item: i, TakeIndex: -1, RequestID: recipeIDOf(raw), Errors: toIssues(errs), Warnings: toIssues(warns)})
			b.failed = true
			return b, nil
		}
		parsed, err := lineage.ParseRequest(raw, true)
		if err != nil {
			p.failures = append(p.failures, lintFailure{Item: i, TakeIndex: -1, RequestID: recipeIDOf(raw), Errors: []Issue{parseIssue(err)}})
			b.failed = true
			return b, nil
		}
		if b.rec, err = p.parseStored(string(parsed.Canonical)); err != nil {
			return b, err
		}
		b.label = parsed.Title
	}
	if b.label == "" {
		b.label = b.rec.ID
	}
	return b, nil
}

// planSeedsPatch is the item's patch with desk.cot applied (a cot equal to the base's changes nothing, so a repeat on a take keeps
// its recipe). It returns nil for no change at all.
func planSeedsPatch(it Item, rec engine.Recipe) *lineage.Patch {
	var pt lineage.Patch
	if it.Patch != nil {
		pt = *it.Patch
	}
	if it.Desk != nil && it.Desk.Cot != "" {
		c := it.Desk.Cot
		pt.Cot = &c
	}
	if pt.Cot != nil && *pt.Cot == rec.Cot {
		pt.Cot = nil
	}
	if pt.IsEmpty() {
		return nil
	}
	return &pt
}

// planSeedsFor chooses the seeds of the item: desk.seeds as given, else desk.n_seeds (default 8) new ones above the base's, none of
// which a recipe of the same content, an earlier item of this estimate or an earlier plan job of the same recipe has used.
func (p *planner) planSeedsFor(it Item, rec engine.Recipe, content, reuseID string) ([]int64, string, error) {
	if it.Desk != nil && len(it.Desk.Seeds) > 0 {
		return it.Desk.Seeds, "explicit", nil
	}
	n := defaultPlanSeeds
	if it.Desk != nil && it.Desk.NSeeds > 0 {
		n = it.Desk.NSeeds
	}
	used, err := lineage.UsedSeeds(p.ctx, p.q, content)
	if err != nil {
		return nil, "", err
	}
	used = append(used, p.planned[content]...)
	if reuseID != "" {
		more, err := planJobSeeds(p.ctx, p.q, reuseID)
		if err != nil {
			return nil, "", err
		}
		used = append(used, more...)
	}
	return lineage.NextSeeds(rec.Seed, append(used, rec.Seed), n), "auto", nil
}

// expandPlanSeeds plans the jobs of one plan_seeds item (C360.3): the base's recipe (the parent's own when nothing is patched, else a
// new one carrying the patch), its seeds, split into jobs that each fit the runtime cap. A plan job makes no take; it needs the plan
// capability, which requireCaps checks right after expansion.
func (p *planner) expandPlanSeeds(i int, it Item) error {
	b, err := p.planSeedsBase(i, it)
	if err != nil || b.failed {
		return err
	}
	patch := planSeedsPatch(it, b.rec)
	reuseID := ""
	planRec := b.rec
	if b.parent != nil && b.draft == nil && patch == nil {
		reuseID = b.parent.RecipeID
	} else {
		id, err := lineage.AllocateID(b.rec.ID, b.rec.Seed, false, p.taken())
		if p.err != nil {
			return p.err
		}
		if errors.Is(err, lineage.ErrNoFreeID) {
			return invalidItem(i, "id", "id_exhausted")
		}
		if err != nil {
			return err
		}
		c, err := p.codec()
		if err != nil {
			return err
		}
		if planRec, err = c.ApplyMode(b.rec, engine.ModeParams{Mode: it.Mode, Operator: lineage.OpPlanSeeds, SeedPolicy: "auto", NSeeds: 1, Seed: b.rec.Seed, ID: id}, patch); err != nil {
			p.failures = append(p.failures, lintFailure{Item: i, TakeIndex: -1, RequestID: id, Errors: []Issue{parseIssue(err)}})
			return nil
		}
	}
	probe := probeRequest(string(planRec.Canonical))
	switch {
	case probe.Cot != "full" && probe.Cot != "melody":
		return invalidItem(i, "desk.cot", "plan_requires_cot")
	case probe.ABC != "":
		return invalidItem(i, "patch.abc", "plan_requires_no_abc")
	}
	errs, warns := lineage.Lint(planRec.Canonical)
	engErrs := toIssues(errs)
	for _, is := range p.eng.Validate(json.RawMessage(planRec.Canonical)) {
		engErrs = append(engErrs, Issue{Code: is.Code, Field: is.Field, Severity: "error", Message: is.Message})
	}
	if len(engErrs) > 0 {
		p.failures = append(p.failures, lintFailure{Item: i, TakeIndex: len(p.takes), RequestID: planRec.ID, Errors: engErrs, Warnings: toIssues(warns)})
		return nil
	}
	secs, err := p.eng.ExpectedAudioSeconds(json.RawMessage(planRec.Canonical))
	if err != nil {
		return fmt.Errorf("generation: expected audio of %s: %w", planRec.ID, err)
	}
	audioS := min(max(secs, 1), maxAnalyzeAudioS)
	content, render := lineage.Keys(planRec.Canonical)
	seeds, policy, err := p.planSeedsFor(it, planRec, content, reuseID)
	if err != nil {
		return err
	}
	sps := planSecondsPerSeed(audioS)
	chunks := splitSeeds(seeds, seedsPerJob(audioS, int(p.s.o.MaxRuntime/time.Second)))
	for _, chunk := range chunks {
		spec := jobspec.Spec{Plan: &jobspec.Plan{Seeds: chunk, SecondsPerSeed: sps}}
		raw, err := spec.Canonical()
		if err != nil {
			return err
		}
		t := &planTake{
			TakeIndex: len(p.takes), ItemIndex: i, Mode: it.Mode, Operator: lineage.OpPlanSeeds, RequestJSON: string(planRec.Canonical), RecipeHash: planRec.Hash,
			ContentHash: content, RenderHash: render, RequestID: planRec.ID, Seed: planRec.Seed, Title: planSeedsTitle(b.label, len(chunk)),
			Params: paramsDoc{Mode: it.Mode, SeedPolicy: policy, NSeeds: len(chunk)}, Patch: patchDoc(patch), ExpectedAudioS: audioS, Warnings: toIssues(warns),
			Kind: jobspec.KindPlan, Needs: jobspec.Needs(jobspec.KindPlan, spec), Spec: raw,
		}
		if t.Warnings == nil {
			t.Warnings = []Issue{}
		}
		if reuseID != "" {
			t.ReuseRecipeID = &reuseID
		}
		if b.parent != nil {
			pid := b.parent.ID
			t.ParentTrackID = &pid
		}
		if b.draft != nil {
			did, rev := b.draft.ID, b.draft.Revision
			t.DraftID, t.DraftRevision = &did, &rev
		}
		if len(chunks) > 1 {
			t.Warnings = append(t.Warnings, warnIssue(WarnPlanSplit, deskNSeedsField,
				fmt.Sprintf("the %d seeds do not fit one job's runtime: they are planned in %d jobs", len(seeds), len(chunks))))
		}
		if p.planJobs == nil {
			p.planJobs = map[int]int{}
		}
		p.planJobs[t.TakeIndex] = len(chunk)
		p.planned[content] = append(p.planned[content], chunk...)
		p.takes = append(p.takes, t)
	}
	p.ids[planRec.ID] = true
	return nil
}

// planSeedsTitle is "<base title> · plans ×<n>", the base title cut so the whole stays within the 200-rune title limit.
func planSeedsTitle(base string, n int) string {
	suffix := fmt.Sprintf("%s%d", planTitleSuffix, n)
	if k := maxTitleRunes - utf8.RuneCountInString(suffix); utf8.RuneCountInString(base) > k {
		base = string([]rune(base)[:max(k, 0)])
	}
	return base + suffix
}
