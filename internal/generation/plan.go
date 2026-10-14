package generation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/lineage"
)

// DefaultEngine is the engine an estimate without one uses.
const DefaultEngine = "yue2"

// terminalJobStates are the job states a job never leaves; every other state is live.
const terminalJobStates = `('completed','failed','cancelled','expired')`

// ---- item validation (C71.1) ----

func isVariant(mode string) bool {
	switch mode {
	case lineage.ModeNewSeed, lineage.ModeSameSeedEdit, lineage.ModeLyricEdit, lineage.ModeStyleEdit:
		return true
	}
	return false
}

// itemTakeCount is how many takes an item expands to; it needs no database.
func itemTakeCount(it Item) int {
	per := len(it.Seeds)
	if per == 0 {
		per = max(it.NSeeds, 1)
		if it.Mode == lineage.ModeSameSeedEdit || (it.Mode == lineage.ModeFromTemplate && it.TemplateMode == lineage.ModeSameSeedEdit) {
			per = 1
		}
	}
	switch it.Mode {
	case lineage.ModeFromDraft:
		return per * len(it.DraftIDs)
	case lineage.ModeFromTemplate:
		return per * len(it.ParentTrackIDs)
	}
	return per
}

func validateItem(i int, it Item) error {
	spec, ok := lineage.Spec(it.Mode)
	if !ok {
		return invalidItem(i, "mode", "unknown_mode")
	}
	notAllowed := func(field string, present bool) error {
		if present {
			return invalidItem(i, field, "not_allowed_for_"+it.Mode)
		}
		return nil
	}
	hasParent := it.ParentTrackID != nil && *it.ParentTrackID != ""
	var errs []error
	switch {
	case isVariant(it.Mode):
		if !hasParent {
			return invalidItem(i, "parent_track_id", "required")
		}
		errs = append(errs, notAllowed("parent_track_ids", len(it.ParentTrackIDs) > 0), notAllowed("draft_ids", len(it.DraftIDs) > 0),
			notAllowed("recipe", len(it.Recipe) > 0), notAllowed("template_mode", it.TemplateMode != ""))
	case it.Mode == lineage.ModeFromDraft:
		if len(it.DraftIDs) == 0 {
			return invalidItem(i, "draft_ids", "required")
		}
		errs = append(errs, notAllowed("parent_track_ids", len(it.ParentTrackIDs) > 0), notAllowed("recipe", len(it.Recipe) > 0),
			notAllowed("template_mode", it.TemplateMode != ""), notAllowed("patch", it.Patch != nil))
	case it.Mode == lineage.ModeComposeNew:
		if len(it.Recipe) == 0 {
			return invalidItem(i, "recipe", "required")
		}
		errs = append(errs, notAllowed("parent_track_id", hasParent), notAllowed("parent_track_ids", len(it.ParentTrackIDs) > 0),
			notAllowed("draft_ids", len(it.DraftIDs) > 0), notAllowed("template_mode", it.TemplateMode != ""), notAllowed("patch", it.Patch != nil))
	case it.Mode == lineage.ModeFromTemplate:
		if len(it.ParentTrackIDs) < 2 || len(it.ParentTrackIDs) > MaxTakes {
			return invalidItem(i, "parent_track_ids", "needs_2_to_50")
		}
		if !lineage.IsTemplateMode(it.TemplateMode) {
			return invalidItem(i, "template_mode", "required")
		}
		errs = append(errs, notAllowed("parent_track_id", hasParent), notAllowed("draft_ids", len(it.DraftIDs) > 0), notAllowed("recipe", len(it.Recipe) > 0))
	}
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	if err := uniqueStrings(i, "draft_ids", it.DraftIDs); err != nil {
		return err
	}
	if err := uniqueStrings(i, "parent_track_ids", it.ParentTrackIDs); err != nil {
		return err
	}
	if len(it.DraftIDs) > MaxTakes {
		return invalidItem(i, "draft_ids", "at_most_50")
	}
	// patch rules of the mode (a template borrows its inner mode's row)
	ps := spec
	if it.Mode == lineage.ModeFromTemplate {
		ps, _ = lineage.Spec(it.TemplateMode)
	}
	if it.Mode != lineage.ModeFromDraft && it.Mode != lineage.ModeComposeNew {
		if f, r := ps.CheckPatch(it.Patch); f != "" {
			return invalidItem(i, f, r)
		}
	}
	if it.NSeeds < 0 || it.NSeeds > 10 {
		return invalidItem(i, "n_seeds", "range_1_to_10")
	}
	if len(it.Seeds) > 10 {
		return invalidItem(i, "seeds", "at_most_10")
	}
	seen := map[int64]bool{}
	for _, s := range it.Seeds {
		if s < 0 || s > lineage.MaxSeed {
			return invalidItem(i, "seeds", "out_of_range")
		}
		if seen[s] {
			return invalidItem(i, "seeds", "duplicate")
		}
		seen[s] = true
	}
	single := it.Mode == lineage.ModeSameSeedEdit || (it.Mode == lineage.ModeFromTemplate && it.TemplateMode == lineage.ModeSameSeedEdit)
	if single && it.NSeeds > 1 {
		return invalidItem(i, "n_seeds", "same_seed_edit_renders_one_take")
	}
	if single && len(it.Seeds) > 1 {
		return invalidItem(i, "seeds", "same_seed_edit_renders_one_take")
	}
	if it.Title != nil {
		if strings.TrimSpace(*it.Title) == "" {
			return invalidItem(i, "title", "empty")
		}
		if itemTakeCount(it) != 1 {
			return invalidItem(i, "title", "single_take_only")
		}
	}
	return nil
}

func uniqueStrings(i int, field string, ids []string) error {
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" {
			return invalidItem(i, field, "empty_id")
		}
		if seen[id] {
			return invalidItem(i, field, "duplicate")
		}
		seen[id] = true
	}
	return nil
}

// ---- reads ----

type trackInfo struct {
	ID, Title, Status, RecipeID, RequestJSON string
	Seed                                     int64
}

func loadTrack(ctx context.Context, q lineage.Querier, id string) (trackInfo, bool, error) {
	t := trackInfo{ID: id}
	var engineName string
	err := q.QueryRowContext(ctx, `SELECT t.title, t.status, t.recipe_id, r.request_json, r.seed, r.engine FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE t.id = ?`, id).
		Scan(&t.Title, &t.Status, &t.RecipeID, &t.RequestJSON, &t.Seed, &engineName)
	if errors.Is(err, sql.ErrNoRows) {
		return t, false, nil
	}
	if err != nil {
		return t, false, fmt.Errorf("generation: load track: %w", err)
	}
	return t, true, nil
}

type draftInfo struct {
	ID, Status, RequestJSON, SourceRecipeID, RequestID, Label string
	Revision                                                  int
	ParentTrackID                                             *string
	InFlight                                                  bool
}

func loadDraft(ctx context.Context, q lineage.Querier, id string) (draftInfo, bool, error) {
	d := draftInfo{ID: id}
	var src, par, label sql.NullString
	var live int
	err := q.QueryRowContext(ctx, `SELECT d.status, d.revision, d.request_json, d.source_recipe_id, d.parent_track_id, d.label, d.request_id,
		EXISTS(SELECT 1 FROM jobs j WHERE j.draft_id = d.id AND j.state NOT IN `+terminalJobStates+`) FROM recipe_drafts d WHERE d.id = ?`, id). //nolint:gosec // G202: constant state list
		Scan(&d.Status, &d.Revision, &d.RequestJSON, &src, &par, &label, &d.RequestID, &live)
	if errors.Is(err, sql.ErrNoRows) {
		return d, false, nil
	}
	if err != nil {
		return d, false, fmt.Errorf("generation: load draft: %w", err)
	}
	d.SourceRecipeID, d.Label, d.InFlight = src.String, label.String, live == 1
	if par.Valid {
		v := par.String
		d.ParentTrackID = &v
	}
	return d, true, nil
}

func recipeIDTaken(ctx context.Context, q lineage.Querier, requestID string) (bool, error) {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recipes WHERE request_id = ?)`, requestID).Scan(&n); err != nil {
		return false, fmt.Errorf("generation: request id: %w", err)
	}
	return n == 1, nil
}

// ---- mothers and drafts ----

func (s *Service) motherErr(err error, id string) error {
	var ru *library.RecipeUnknownError
	var lm *library.LossyMasterError
	switch {
	case errors.Is(err, library.ErrNotFound):
		return unknownTrack(id)
	case errors.As(err, &ru):
		return perr(http.StatusUnprocessableEntity, "RECIPE_UNKNOWN", "the recipe of this track is unknown (reference track)", map[string]any{"track_id": ru.TrackID})
	case errors.As(err, &lm):
		return perr(http.StatusUnprocessableEntity, "LOSSY_MASTER", "the master of this track is a lossy MP3", map[string]any{"track_id": lm.TrackID})
	}
	return err
}

// mothers lists the mother tracks of an item in the order the checks run.
func mothers(it Item) []string {
	var out []string
	if it.ParentTrackID != nil && *it.ParentTrackID != "" {
		out = append(out, *it.ParentTrackID)
	}
	return append(out, it.ParentTrackIDs...)
}

// checkMothers refuses reference and lossy mothers before anything else is computed (D-M3-34).
func (s *Service) checkMothers(ctx context.Context, items []Item) error {
	seen := map[string]bool{}
	for _, it := range items {
		for _, id := range mothers(it) {
			if seen[id] {
				continue
			}
			seen[id] = true
			if err := s.o.Library.RequireRecipeKnown(ctx, id); err != nil {
				return s.motherErr(err, id)
			}
			if err := s.o.Library.RequireLosslessMaster(ctx, id); err != nil {
				return s.motherErr(err, id)
			}
		}
	}
	return nil
}

// ---- planner ----

type planner struct {
	s       *Service
	ctx     context.Context
	q       lineage.Querier
	eng     engine.Engine
	backend string

	takes    []*planTake
	items    [][]int // take indexes per input item
	ids      map[string]bool
	planned  map[string][]int64 // content hash -> seeds planned in this estimate
	renders  map[string]int     // render hash -> first take index
	tracks   map[string]trackInfo
	drafts   map[string]draftInfo
	failures []lintFailure
	reused   map[string]bool // draft recipes already claimed by a take of this estimate
	err      error           // the first failed lookup inside a callback
}

type lintFailure struct {
	Item      int     `json:"item"`
	TakeIndex int     `json:"take_index"`
	RequestID string  `json:"request_id"`
	Errors    []Issue `json:"errors"`
	Warnings  []Issue `json:"warnings"`
}

func (p *planner) track(id string) (trackInfo, error) {
	if t, ok := p.tracks[id]; ok {
		return t, nil
	}
	t, ok, err := loadTrack(p.ctx, p.q, id)
	if err != nil {
		return t, err
	}
	if !ok {
		return t, unknownTrack(id)
	}
	p.tracks[id] = t
	return t, nil
}

// loadDrafts resolves every draft id of the items and refuses unknown and non-generatable ones.
func (p *planner) loadDrafts(items []Item) error {
	var unknown, blocked []string
	why := ""
	for _, it := range items {
		for _, id := range it.DraftIDs {
			if _, done := p.drafts[id]; done {
				continue
			}
			d, ok, err := loadDraft(p.ctx, p.q, id)
			if err != nil {
				return err
			}
			if !ok {
				unknown = append(unknown, id)
				continue
			}
			p.drafts[id] = d
			reason := ""
			switch {
			case d.Status == "fulfilled":
				reason = "fulfilled"
			case d.Status == "archived":
				reason = "archived"
			case d.InFlight:
				reason = "in_flight"
			}
			if reason != "" {
				blocked = append(blocked, id)
				if why == "" {
					why = reason
				}
			}
		}
	}
	if len(unknown) > 0 {
		return perr(http.StatusUnprocessableEntity, "UNKNOWN_DRAFT", "no such draft", map[string]any{"draft_ids": unknown})
	}
	if len(blocked) > 0 {
		return perr(http.StatusConflict, "DRAFT_NOT_GENERATABLE", "a draft cannot be generated: "+why, map[string]any{"draft_ids": blocked, "why": why})
	}
	return nil
}

// base is one recipe an item renders takes of.
type base struct {
	item   int
	it     Item
	mode   string // effective mode: the template mode for from_template
	spec   lineage.ModeSpec
	req    lineage.Request
	parent *trackInfo // mother track, when there is one
	draft  *draftInfo
	patch  *lineage.Patch
	via    *string
	label  string
}

func (p *planner) expandAll(items []Item) error {
	for i, it := range items {
		first := len(p.takes)
		var err error
		switch it.Mode {
		case lineage.ModeFromDraft:
			for _, id := range it.DraftIDs {
				d := p.drafts[id]
				if err = p.expandDraft(i, it, d); err != nil {
					return err
				}
			}
		case lineage.ModeComposeNew:
			err = p.expandCompose(i, it)
		case lineage.ModeFromTemplate:
			via := lineage.ModeFromTemplate
			for _, id := range it.ParentTrackIDs {
				if err = p.expandVariant(i, it, it.TemplateMode, id, &via); err != nil {
					return err
				}
			}
		default:
			err = p.expandVariant(i, it, it.Mode, *it.ParentTrackID, nil)
		}
		if err != nil {
			return err
		}
		idx := make([]int, 0, len(p.takes)-first)
		for k := first; k < len(p.takes); k++ {
			idx = append(idx, k)
		}
		p.items = append(p.items, idx)
	}
	if len(p.failures) > 0 {
		return perr(http.StatusUnprocessableEntity, "LINT_FAILED", "the request has protocol errors", map[string]any{"items": p.failures})
	}
	return nil
}

func (p *planner) expandVariant(i int, it Item, mode, parentID string, via *string) error {
	t, err := p.track(parentID)
	if err != nil {
		return err
	}
	req, err := lineage.RequestOf([]byte(t.RequestJSON))
	if err != nil {
		return fmt.Errorf("generation: parent recipe of %s: %w", parentID, err)
	}
	spec, _ := lineage.Spec(mode)
	return p.expandBase(base{item: i, it: it, mode: mode, spec: spec, req: req, parent: &t, patch: it.Patch, via: via})
}

func (p *planner) expandDraft(i int, it Item, d draftInfo) error {
	req, err := lineage.RequestOf([]byte(d.RequestJSON))
	if err != nil {
		return fmt.Errorf("generation: draft %s: %w", d.ID, err)
	}
	spec, _ := lineage.Spec(lineage.ModeFromDraft)
	b := base{item: i, it: it, mode: lineage.ModeFromDraft, spec: spec, req: req, draft: &d, label: d.Label}
	mother := d.ParentTrackID
	if it.ParentTrackID != nil && *it.ParentTrackID != "" {
		mother = it.ParentTrackID
	}
	if mother != nil {
		t, err := p.track(*mother)
		if err != nil {
			return err
		}
		b.parent = &t
	}
	return p.expandBase(b)
}

func (p *planner) expandCompose(i int, it Item) error {
	raw := []byte(it.Recipe)
	errs, warns := lineage.Lint(raw)
	if len(errs) == 0 {
		var n int
		if id := recipeIDOf(raw); id != "" {
			if err := p.q.QueryRowContext(p.ctx, `SELECT EXISTS(SELECT 1 FROM recipes WHERE engine = 'unknown' AND request_id = ?)`, id).Scan(&n); err != nil {
				return fmt.Errorf("generation: stub id: %w", err)
			}
			if n == 1 {
				errs = append(errs, Issue{Code: "recipe_unknown", Field: "id", Severity: "error", Message: "this id belongs to a reference track whose recipe is unknown"}.asLineage())
			}
		}
	}
	if len(errs) > 0 {
		p.failures = append(p.failures, lintFailure{Item: i, TakeIndex: -1, RequestID: recipeIDOf(raw), Errors: toIssues(errs), Warnings: toIssues(warns)})
		return nil
	}
	parsed, err := lineage.ParseRequest(raw, true)
	if err != nil {
		p.failures = append(p.failures, lintFailure{Item: i, TakeIndex: -1, RequestID: recipeIDOf(raw), Errors: []Issue{parseIssue(err)}})
		return nil
	}
	spec, _ := lineage.Spec(lineage.ModeComposeNew)
	return p.expandBase(base{item: i, it: it, mode: lineage.ModeComposeNew, spec: spec, req: parsed.Request, label: parsed.Title})
}

func recipeIDOf(raw []byte) string {
	var m struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.ID
}

func parseIssue(err error) Issue {
	var pe *lineage.ParseError
	if errors.As(err, &pe) {
		return Issue{Code: pe.Code, Severity: "error", Message: pe.Detail}
	}
	return Issue{Code: "invalid_request", Severity: "error", Message: "the request is not valid"}
}

func toIssues(in []lineage.Issue) []Issue {
	out := make([]Issue, 0, len(in))
	for _, is := range in {
		out = append(out, Issue{Code: is.Code, Field: is.Field, Severity: is.Severity, Message: is.Message})
	}
	return out
}

func (is Issue) asLineage() lineage.Issue {
	return lineage.Issue{Code: is.Code, Field: is.Field, Severity: is.Severity, Message: is.Message}
}

// seedPlan is one seed of a base with the policy that chose it.
type seedPlan struct {
	seed   int64
	policy string
}

// chooseSeeds applies the seed rule of the mode (C71.3).
func (p *planner) chooseSeeds(b base, content string) ([]seedPlan, error) {
	own := b.req.Seed
	it := b.it
	policyFixed := map[lineage.SeedRule]string{lineage.SeedsParentOnly: "parent", lineage.SeedsParentNew: "parent", lineage.SeedsDraftNew: "draft", lineage.SeedsRecipeNew: "recipe"}[b.spec.Seeds]
	if len(it.Seeds) > 0 {
		out := make([]seedPlan, 0, len(it.Seeds))
		for _, s := range it.Seeds {
			switch {
			case b.spec.Seeds == lineage.SeedsNew && s == own:
				return nil, invalidItem(b.item, "seeds", "contains_parent_seed")
			case b.spec.Seeds == lineage.SeedsParentOnly && s != own:
				return nil, invalidItem(b.item, "seeds", "same_seed_edit_uses_the_parent_seed")
			}
			out = append(out, seedPlan{s, "explicit"})
		}
		return out, nil
	}
	n := max(it.NSeeds, 1)
	used, err := lineage.UsedSeeds(p.ctx, p.q, content)
	if err != nil {
		return nil, err
	}
	used = append(used, p.planned[content]...)
	var out []seedPlan
	switch b.spec.Seeds {
	case lineage.SeedsNew:
		for _, s := range lineage.NextSeeds(own, used, n) {
			out = append(out, seedPlan{s, "auto"})
		}
	case lineage.SeedsParentOnly:
		out = append(out, seedPlan{own, policyFixed})
	default:
		out = append(out, seedPlan{own, policyFixed})
		for _, s := range lineage.NextSeeds(own, append(used, own), n-1) {
			out = append(out, seedPlan{s, "auto"})
		}
	}
	return out, nil
}

func (p *planner) taken() func(string) bool {
	return func(id string) bool {
		if p.ids[id] {
			return true
		}
		t, err := recipeIDTaken(p.ctx, p.q, id)
		if err != nil && p.err == nil {
			p.err = err
		}
		return t || err != nil
	}
}
