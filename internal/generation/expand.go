package generation

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

// expandBase renders the takes of one base recipe: seeds (C71.3), ids, the derived request, lint, the
// planner warnings and the sizes the quote needs.
func (p *planner) expandBase(b base) error {
	op := b.spec.Operator
	probe, err := lineage.ApplyParsed(b.req, op, lineage.Params{Mode: b.mode, Seed: b.req.Seed, ID: b.req.ID}, b.patch)
	if err != nil {
		p.failures = append(p.failures, lintFailure{Item: b.item, TakeIndex: -1, RequestID: b.req.ID, Errors: []Issue{parseIssue(err)}})
		return nil
	}
	content, _ := lineage.Keys(probe.Canonical)
	seeds, err := p.chooseSeeds(b, content)
	if err != nil {
		return err
	}
	for _, sp := range seeds {
		if err := p.planTake(b, op, sp, len(seeds)); err != nil {
			return err
		}
	}
	return nil
}

func (p *planner) planTake(b base, op string, sp seedPlan, nSeeds int) error {
	keep := b.mode == lineage.ModeComposeNew && nSeeds == 1 && sp.seed == b.req.Seed
	reuse := ""
	if b.draft != nil && sp.seed == b.req.Seed {
		rid, ok, err := lineage.ReusableDraftRecipe(p.ctx, p.q, lineage.DraftRow{ID: b.draft.ID, Revision: b.draft.Revision,
			SourceRecipeID: b.draft.SourceRecipeID, RequestJSON: b.draft.RequestJSON})
		if err != nil {
			return err
		}
		if ok && !p.reused[rid] {
			reuse = rid
			p.reused[rid] = true
		}
	}
	id := b.req.ID
	if reuse == "" {
		var err error
		id, err = lineage.AllocateID(b.req.ID, sp.seed, keep, p.taken())
		if p.err != nil {
			return p.err
		}
		if errors.Is(err, lineage.ErrNoFreeID) {
			return invalidItem(b.item, "id", "id_exhausted")
		}
		if err != nil {
			return err
		}
	}
	params := lineage.Params{Mode: b.mode, SeedPolicy: sp.policy, NSeeds: nSeeds, Via: b.via, Seed: sp.seed, ID: id}
	parsed, err := lineage.ApplyParsed(b.req, op, params, b.patch)
	if err != nil {
		p.failures = append(p.failures, lintFailure{Item: b.item, TakeIndex: len(p.takes), RequestID: id, Errors: []Issue{parseIssue(err)}})
		return nil
	}
	content, render := lineage.Keys(parsed.Canonical)
	t := &planTake{
		TakeIndex: len(p.takes), ItemIndex: b.item, Mode: b.mode, Operator: op,
		RequestJSON: string(parsed.Canonical), RecipeHash: parsed.Hash, ContentHash: content, RenderHash: render,
		RequestID: id, Seed: sp.seed, Params: paramsDoc{Mode: b.mode, SeedPolicy: sp.policy, NSeeds: nSeeds, Via: b.via},
		Patch: patchDoc(b.patch), Warnings: []Issue{},
	}
	if reuse != "" {
		t.ReuseRecipeID = &reuse
	}
	if b.parent != nil {
		pid := b.parent.ID
		t.ParentTrackID = &pid
	}
	if b.draft != nil {
		did, rev := b.draft.ID, b.draft.Revision
		t.DraftID, t.DraftRevision = &did, &rev
	}
	explicit := ""
	if b.it.Title != nil {
		explicit = *b.it.Title
	} else if nSeeds == 1 && b.mode == lineage.ModeComposeNew {
		explicit = b.label
	}
	label := ""
	if sp.seed == b.req.Seed {
		label = b.label
	}
	parentTitle := ""
	if b.parent != nil {
		parentTitle = b.parent.Title
	}
	t.Title = lineage.DefaultTitle(lineage.TitleIn{Explicit: explicit, Variant: isVariant(b.mode), ParentTitle: parentTitle,
		FromDraft: b.draft != nil, Label: label, HasMother: b.parent != nil, RequestID: id, Seed: sp.seed})

	// lint: protocol and songwriting findings, then the engine's own batch rules
	errs, warns := lineage.Lint(parsed.Canonical)
	engErrs := toIssues(errs)
	for _, is := range p.eng.Validate(json.RawMessage(parsed.Canonical)) {
		engErrs = append(engErrs, Issue{Code: is.Code, Field: is.Field, Severity: "error", Message: is.Message})
	}
	if len(engErrs) > 0 {
		p.failures = append(p.failures, lintFailure{Item: b.item, TakeIndex: t.TakeIndex, RequestID: id, Errors: engErrs, Warnings: toIssues(warns)})
	}
	t.Warnings = append(t.Warnings, toIssues(warns)...)
	w, err := p.plannerWarnings(b, t, reuse)
	if err != nil {
		return err
	}
	t.Warnings = append(t.Warnings, w...)
	if secs, err := p.eng.ExpectedAudioSeconds(json.RawMessage(parsed.Canonical)); err != nil {
		if len(engErrs) == 0 {
			return fmt.Errorf("generation: expected audio of %s: %w", id, err)
		}
	} else {
		t.ExpectedAudioS = secs
	}
	n, err := p.inputBytes(t, parsed.Canonical)
	if err != nil {
		return err
	}
	if n > provider.MaxRunBody {
		return invalidItem(b.item, "recipe", "input_too_large")
	}
	t.InputBytes = n

	p.ids[id] = true
	p.planned[content] = append(p.planned[content], sp.seed)
	if _, ok := p.renders[render]; !ok {
		p.renders[render] = t.TakeIndex
	}
	p.takes = append(p.takes, t)
	return nil
}

// plannerWarnings are the warnings only the planner can raise (M3 lint.go): duplicate_render,
// parent_discarded and fake_marker.
func (p *planner) plannerWarnings(b base, t *planTake, reuse string) ([]Issue, error) {
	var out []Issue
	var dup int
	if err := p.q.QueryRowContext(p.ctx, `SELECT EXISTS(SELECT 1 FROM recipe_keys WHERE render_hash = ? AND recipe_id <> ?)`, t.RenderHash, reuse).Scan(&dup); err != nil {
		return nil, fmt.Errorf("generation: duplicate render: %w", err)
	}
	where := ""
	if dup == 1 {
		where = "recipe"
	} else if first, ok := p.renders[t.RenderHash]; ok {
		where = fmt.Sprintf("take:%d", first)
	}
	if where != "" {
		out = append(out, Issue{Code: "duplicate_render", Field: "", Severity: "warning",
			Message: "the same render already exists (" + where + "); YuE2's output does not depend on the request id"})
	}
	if b.parent != nil && b.parent.Status == "discarded" {
		out = append(out, Issue{Code: "parent_discarded", Field: "parent_track_id", Severity: "warning", Message: "the mother track is discarded"})
	}
	if p.backend != provider.BackendFake {
		var req struct {
			Style string `json:"style"`
		}
		if json.Unmarshal([]byte(t.RequestJSON), &req) == nil && strings.Contains(req.Style, "fake:") {
			out = append(out, Issue{Code: "fake_marker", Field: "style", Severity: "warning", Message: "fake:<fault> markers only act on the fake backend; here they are plain style text"})
		}
	}
	return out, nil
}

// patchDoc renders a patch as the JSON document stored in items_json and jobs.mutation_json: absent keys are
// omitted, null removes, a value sets. nil for an empty patch.
func patchDoc(pt *lineage.Patch) json.RawMessage {
	if pt.IsEmpty() {
		return json.RawMessage("null")
	}
	m := map[string]any{}
	if pt.Style != nil {
		m["style"] = *pt.Style
	}
	if pt.Lyrics != nil {
		m["lyrics"] = *pt.Lyrics
	}
	if pt.Cot != nil {
		m["cot"] = *pt.Cot
	}
	if pt.ABC.Set {
		if pt.ABC.Null {
			m["abc"] = nil
		} else {
			m["abc"] = pt.ABC.Value
		}
	}
	if pt.CFGScale.Set {
		if pt.CFGScale.Null {
			m["cfg_scale"] = nil
		} else {
			m["cfg_scale"] = pt.CFGScale.Value
		}
	}
	b, _ := json.Marshal(m)
	return b
}

// inputBytes is the size of the /run body this take would produce with maximal-length placeholder URLs
// (C73.3): a take that cannot fit MaxRunBody is refused at estimate, never discovered at submit.
func (p *planner) inputBytes(t *planTake, canonical []byte) (int, error) {
	ph := strings.Repeat("u", 2048)
	long := strings.Repeat("x", 64)
	req, opt := p.eng.Artifacts(json.RawMessage(canonical))
	uploads := map[string]provider.UploadTarget{}
	for _, name := range append(append([]string(nil), req...), opt...) {
		uploads[name] = provider.UploadTarget{URL: ph, MaxBytes: 1 << 30}
	}
	in := provider.WorkerInput{
		Contract: provider.Contract, TakeID: long, AttemptID: long, Engine: p.s.engineName(p.eng), Request: json.RawMessage(canonical),
		TimeoutS: 2700, UploadBudgetS: 600, DeadlineUnix: 1 << 40, Start: provider.StartTarget{URL: ph}, Uploads: uploads,
		Expect: provider.WorkerExpect{Weights: map[string]string{
			"m-a-p/YuE2-3B": strings.Repeat("0", 40), "m-a-p/YuE2-Vae": strings.Repeat("0", 40),
		}},
	}
	b, err := json.Marshal(struct {
		Input provider.WorkerInput `json:"input"`
	}{in})
	if err != nil {
		return 0, fmt.Errorf("generation: job input: %w", err)
	}
	return len(b), nil
}

func (s *Service) engineName(e interface{ Name() string }) string { return e.Name() }
