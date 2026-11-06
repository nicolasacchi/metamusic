package generation

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

// Expansion of the E2 items (C149.1-C149.3): the modes port, extend, repaint, reference, complete and stems, ACE-Step
// compose_new, and the variant modes on an ACE-Step parent. They run through the engine's RecipeCodec with its typed patch
// (acestep.Patch); the yue2 planner in expand.go is untouched. The planner only reaches this file when the batch engine is not
// yue2, so a yue2 estimate takes exactly the M3 code path.

// isE2Mode reports whether mode is one of the modes the E2 freeze added.
func isE2Mode(mode string) bool {
	switch mode {
	case lineage.ModePort, lineage.ModeExtend, lineage.ModeRepaint, lineage.ModeReference, lineage.ModeComplete, lineage.ModeStems:
		return true
	}
	return false
}

// itemRules are the engine issue codes that surface as 422 INVALID_ITEM rules (C149.2) instead of a lint failure.
var itemRules = map[string]bool{
	"range_order": true, "range_outside_source": true, "range_too_short": true, "range_too_long": true, "range_required": true,
	"extend_too_long": true, "extend_too_short": true, "extend_required": true, "total_over_600": true,
	"tracks_required": true, "tracks_duplicate": true, "tracks_too_many": true, "unknown_track_name": true,
	"reference_window": true, "instrumental_lyrics": true, "exceeds_max_runtime": true,
}

// aceCtx is what an ACE-Step base carries besides its recipe: the engine's typed patch, the pinned source and reference, and
// the facts the estimate echoes.
type aceCtx struct {
	patch *acestep.Patch
	src   *srcPin
	ref   *refPin
	edit  *PlannedEdit
	cross bool // the parent was rendered by the other engine (D-E2-25)
	stem  string
	warns []Issue // warnings of the base translation (FromYuE2) and of the mode
}

// takeE2 is what the estimate reports of an E2 take (TakePlan.Engine, Task, TaskClass, Edit, Reference, CrossEngine).
type takeE2 struct {
	Task, Class string
	Edit        *PlannedEdit
	Ref         *PlannedRef
	Cross       bool
	Audio       int
	LRC         bool
}

func warnIssue(code, field, msg string) Issue {
	return Issue{Code: code, Field: field, Severity: "warning", Message: msg}
}

// ---- static shape of an E2 item (no database) ----

func stemCount(it Item) int {
	if it.Edit == nil || len(it.Edit.Tracks) == 0 {
		return 1
	}
	return len(it.Edit.Tracks)
}

func validateE2Shape(i int, it Item, hasParent bool) error {
	if !hasParent {
		return invalidItem(i, "parent_track_id", "required")
	}
	notAllowed := func(field string, present bool) error {
		if present {
			return invalidItem(i, field, "not_allowed_for_"+it.Mode)
		}
		return nil
	}
	e := it.Edit
	has := func(f func(*Edit) bool) bool { return e != nil && f(e) }
	hasRange := has(func(e *Edit) bool { return e.FromS != nil || e.ToS != nil })
	hasExtend := has(func(e *Edit) bool { return e.ExtendS != nil })
	hasTracks := has(func(e *Edit) bool { return len(e.Tracks) > 0 })
	for _, err := range []error{
		notAllowed("parent_track_ids", len(it.ParentTrackIDs) > 0), notAllowed("draft_ids", len(it.DraftIDs) > 0),
		notAllowed("recipe", len(it.Recipe) > 0), notAllowed("template_mode", it.TemplateMode != ""),
		notAllowed("reference", it.Reference != nil && it.Mode != lineage.ModeReference),
		notAllowed("edit", e != nil && (it.Mode == lineage.ModePort || it.Mode == lineage.ModeReference)),
		notAllowed("edit.extend_s", hasExtend && it.Mode != lineage.ModeExtend),
		notAllowed("edit.from_s", hasRange && it.Mode != lineage.ModeRepaint),
		notAllowed("edit.tracks", hasTracks && it.Mode != lineage.ModeComplete && it.Mode != lineage.ModeStems),
	} {
		if err != nil {
			return err
		}
	}
	for _, v := range editSeconds(e) {
		if v != nil && !validSeconds(*v) {
			return invalidItem(i, "edit", "invalid_seconds")
		}
	}
	switch it.Mode {
	case lineage.ModeRepaint:
		if e == nil || e.FromS == nil || e.ToS == nil {
			return invalidItem(i, "edit", "range_required")
		}
		if ds(*e.FromS) >= ds(*e.ToS) {
			return invalidItem(i, "edit.to_s", "range_order")
		}
	case lineage.ModeExtend:
		if e == nil || e.ExtendS == nil {
			return invalidItem(i, "edit.extend_s", "extend_required")
		}
	case lineage.ModeComplete, lineage.ModeStems:
		return validateTracks(i, it)
	}
	return nil
}

func editSeconds(e *Edit) []*float64 {
	if e == nil {
		return nil
	}
	return []*float64{e.FromS, e.ToS, e.ExtendS}
}

func validateTracks(i int, it Item) error {
	maxN := 4
	if it.Mode == lineage.ModeStems {
		maxN = 6
	}
	var tracks []string
	if it.Edit != nil {
		tracks = it.Edit.Tracks
	}
	switch {
	case len(tracks) == 0:
		return invalidItem(i, "edit.tracks", "tracks_required")
	case len(tracks) > maxN:
		return invalidItem(i, "edit.tracks", "tracks_too_many")
	}
	seen := map[string]bool{}
	for _, n := range tracks {
		if !slices.Contains(acestep.StemNames, n) {
			return invalidItem(i, "edit.tracks", "unknown_track_name")
		}
		if seen[n] {
			return invalidItem(i, "edit.tracks", "tracks_duplicate")
		}
		seen[n] = true
	}
	return nil
}

// presenceOf is the engine-patch presence the lineage rules read.
func presenceOf(ep *EnginePatch) lineage.EnginePresence {
	if ep == nil {
		return lineage.EnginePresence{}
	}
	return lineage.EnginePresence{BPM: ep.BPM != nil, KeyScale: ep.KeyScale != nil, TimeSignature: ep.TimeSignature != nil, DurationS: ep.DurationS != nil}
}

// checkPatchFor applies the patch rules of the mode row for the item's engine (lineage.CheckPatchFor); for yue2 without
// engine fields it is CheckPatch.
func checkPatchFor(spec lineage.ModeSpec, engineName string, it Item) (field, rule string) {
	return spec.CheckPatchFor(engineName, it.Patch, presenceOf(it.EnginePatch))
}

// capabilityOf names the capability a refused mode is reported under (C149.1): the mode row's own, else the mode.
func capabilityOf(mode string) string {
	if s, ok := lineage.Spec(mode); ok && s.Capability != "" {
		return s.Capability
	}
	return mode
}

// checkModeCaps refuses a mode the engine's effective caps do not list (verified file, C148.7): ENGINE_CAPABILITY with the
// capability name. yue2 keeps its M3 behaviour (no Tasks check).
func checkModeCaps(eng engine.Engine, mode string) error {
	if eng.Name() == lineage.EngineYuE2 {
		return nil
	}
	if !slices.Contains(eng.Caps().Tasks, mode) {
		return engineCapabilityErr(eng.Name(), mode, capabilityOf(mode))
	}
	return nil
}

// ---- patch building ----

// acePatch converts the item's patch (lineage.Patch plus the acestep15 fields) into the engine's typed patch.
func acePatch(i int, it Item) (*acestep.Patch, error) {
	pt := &acestep.Patch{}
	if p := it.Patch; p != nil {
		pt.Style, pt.Lyrics = p.Style, p.Lyrics
		if p.Cot != nil {
			switch *p.Cot {
			case "think":
				t := true
				pt.Thinking = &t
			case "off":
				f := false
				pt.Thinking = &f
			default:
				return nil, invalidItem(i, "patch.cot", "cot_invalid")
			}
		}
	}
	if e := it.EnginePatch; e != nil {
		if e.BPM != nil {
			pt.BPM = acestep.OptInt{Set: true, Value: *e.BPM}
		}
		if e.KeyScale != nil {
			pt.KeyScale = acestep.OptString{Set: true, Value: *e.KeyScale}
		}
		if e.TimeSignature != nil {
			pt.TimeSignature = acestep.OptString{Set: true, Value: *e.TimeSignature}
		}
		if e.DurationS != nil {
			if !validSeconds(*e.DurationS) {
				return nil, invalidItem(i, "patch.duration_s", "invalid_seconds")
			}
			d := ds(*e.DurationS)
			pt.DurationDS = &d
		}
	}
	return pt, nil
}

// aceDoc renders the ACE-Step patch as the document stored in items_json and jobs.mutation_json (recipe_patch): the item's
// patch fields and the mode's inputs, so a replay over the stored parent request reproduces the render hash.
func aceDoc(pt *acestep.Patch) json.RawMessage {
	m := map[string]any{}
	if pt != nil {
		if pt.Style != nil {
			m["style"] = *pt.Style
		}
		if pt.Lyrics != nil {
			m["lyrics"] = *pt.Lyrics
		}
		if pt.Thinking != nil {
			m["thinking"] = *pt.Thinking
		}
		if pt.BPM.Set {
			m["bpm"] = pt.BPM.Value
		}
		if pt.KeyScale.Set {
			m["keyscale"] = pt.KeyScale.Value
		}
		if pt.TimeSignature.Set {
			m["timesignature"] = pt.TimeSignature.Value
		}
		if pt.DurationDS != nil {
			m["duration_ds"] = *pt.DurationDS
		}
		if pt.Src != nil {
			m["src"] = pt.Src
		}
		if pt.Ref != nil {
			m["ref"] = pt.Ref
		}
		if pt.RepaintFromDS != nil {
			m["repaint_from_ds"] = *pt.RepaintFromDS
		}
		if pt.RepaintToDS != nil {
			m["repaint_to_ds"] = *pt.RepaintToDS
		}
		if pt.ExtendDS != nil {
			m["extend_ds"] = *pt.ExtendDS
		}
		if len(pt.Tracks) > 0 {
			m["tracks"] = pt.Tracks
		}
	}
	if len(m) == 0 {
		return json.RawMessage("null")
	}
	b, _ := json.Marshal(m)
	return b
}

// ---- bases ----

// aceBase is the base recipe of an ACE-Step derivation (C149.1): the parent's request when it is an ACE-Step take, else the
// translation of a yue2 parent (FromYuE2), whose warnings come back. cross is true for a yue2 parent.
func (p *planner) aceBase(i int, t trackInfo) (rec engine.Recipe, cross bool, warns []Issue, err error) {
	switch t.Engine {
	case acestep.Name:
		rec, err = p.parseStored(t.RequestJSON)
		return rec, false, nil, err
	case lineage.EngineYuE2:
		y, e := lineage.RequestOf([]byte(t.RequestJSON))
		if e != nil {
			return rec, true, nil, fmt.Errorf("generation: parent recipe of %s: %w", t.ID, e)
		}
		id := y.ID
		if len(id) > 170 {
			id = id[:170]
		}
		r, is := acestep.FromYuE2(y, id+"-ace")
		if acestep.HasErrors(is) {
			p.failures = append(p.failures, lintFailure{Item: i, TakeIndex: -1, RequestID: y.ID, Errors: engineIssues(is, "error")})
			return rec, true, nil, errSkipItem
		}
		raw, e := acestep.Canonical(r)
		if e != nil {
			return rec, true, nil, e
		}
		rec, err = p.parseStored(string(raw))
		return rec, true, engineIssues(is, "warning"), err
	}
	return rec, false, nil, invalidItem(i, "parent_track_id", "parent_engine_unsupported")
}

// errSkipItem marks an item whose failure is already recorded in planner.failures.
var errSkipItem = errors.New("generation: item recorded as a lint failure")

func engineIssues(is []engine.Issue, severity string) []Issue {
	var out []Issue
	for _, x := range is {
		if x.Severity == severity {
			out = append(out, Issue{Code: x.Code, Field: x.Field, Severity: x.Severity, Message: x.Message})
		}
	}
	return out
}

// ---- expansion ----

// expandAny expands a base of the batch engine: through expand.go for yue2, through expandACE otherwise.
func (p *planner) expandAny(b base) error {
	if p.ace {
		return p.expandACE(b)
	}
	return p.expandBase(b)
}

// expandE2 expands one item of port, extend, repaint, reference, complete or stems.
func (p *planner) expandE2(i int, it Item) error {
	mode := it.Mode
	t, err := p.track(*it.ParentTrackID)
	if err != nil {
		return err
	}
	if mode == lineage.ModePort && t.Engine != lineage.EngineYuE2 {
		return invalidItem(i, "parent_track_id", "parent_not_yue2")
	}
	rec, cross, warns, err := p.aceBase(i, t)
	if errors.Is(err, errSkipItem) {
		return nil
	}
	if err != nil {
		return err
	}
	pt, err := acePatch(i, it)
	if err != nil {
		return err
	}
	spec, _ := lineage.SpecFor(mode, p.engName)
	ac := &aceCtx{patch: pt, cross: cross, warns: warns}
	b := base{item: i, it: it, mode: mode, spec: spec, rec: rec, parent: &t, ace: ac}
	switch mode {
	case lineage.ModeReference:
		r := Reference{TrackID: t.ID}
		if it.Reference != nil {
			r = *it.Reference
		}
		pin, err := p.resolveRef(i, r)
		if err != nil {
			return err
		}
		ac.ref, pt.Ref = &pin, &pin.Ref
	case lineage.ModeRepaint:
		src, err := p.resolveSrc(mode, t.ID)
		if err != nil {
			return err
		}
		from, to := ds(*it.Edit.FromS), ds(*it.Edit.ToS)
		if to > src.Ref.DurationDS {
			return invalidItem(i, "edit.to_s", "range_outside_source")
		}
		if err := p.checkRange(i, from, to); err != nil {
			return err
		}
		ac.src, ac.edit = &src, &PlannedEdit{FromS: f64(from), ToS: f64(to)}
		pt.Src, pt.RepaintFromDS, pt.RepaintToDS = &src.Ref, &from, &to
		if to-from > abMaxDS {
			ac.warns = append(ac.warns, warnIssue(WarnABWindowTruncated, "edit", "an A/B with the parent compares only the first 45 s of this range"))
		}
	case lineage.ModeExtend:
		src, err := p.resolveSrc(mode, t.ID)
		if err != nil {
			return err
		}
		ext := ds(*it.Edit.ExtendS)
		if err := p.checkExtend(i, ext, src.Ref.DurationDS); err != nil {
			return err
		}
		ac.src, ac.edit = &src, &PlannedEdit{ExtendS: f64(ext)}
		pt.Src, pt.ExtendDS = &src.Ref, &ext
		ac.warns = append(ac.warns, warnIssue(WarnExtendSeamExperimental, "edit", "the seam between the source and the extension is unverified"))
	case lineage.ModeComplete, lineage.ModeStems:
		src, err := p.resolveSrc(mode, t.ID)
		if err != nil {
			return err
		}
		ac.src = &src
		pt.Src = &src.Ref
		if mode == lineage.ModeComplete {
			pt.Tracks = slices.Clone(it.Edit.Tracks)
			ac.edit = &PlannedEdit{Tracks: slices.Clone(it.Edit.Tracks)}
			return p.expandACE(b)
		}
		return p.expandStems(b)
	}
	if ac.cross && mode != lineage.ModePort {
		ac.warns = append(ac.warns, warnIssue(WarnCrossEngineEdit, "parent_track_id", "the parent was rendered by YuE2; the edit uses its audio only and the seam is unvalidated"))
	}
	return p.expandACE(b)
}

// expandStems expands one take-less job per name (D-E2-11): each stem is its own request on the same source.
func (p *planner) expandStems(b base) error {
	for _, name := range b.it.Edit.Tracks {
		bb := b
		ac := *b.ace
		pt := *b.ace.patch
		pt.Tracks = []string{name}
		ac.patch, ac.stem = &pt, name
		ac.edit = &PlannedEdit{Tracks: []string{name}, Stem: name}
		ac.warns = slices.Clone(b.ace.warns)
		if ac.cross {
			ac.warns = append(ac.warns, warnIssue(WarnCrossEngineEdit, "parent_track_id", "the parent was rendered by YuE2; the edit uses its audio only and the seam is unvalidated"))
		}
		exists, err := p.stemExists(b.parent.ID, name)
		if err != nil {
			return err
		}
		if exists {
			ac.warns = append(ac.warns, warnIssue(WarnStemExists, "edit.tracks", "this take already has a "+name+" stem; the new one is stored under a numbered name"))
		}
		bb.ace = &ac
		if err := p.expandACE(bb); err != nil {
			return err
		}
	}
	return nil
}

// stemExists reports whether the source take already holds a stem artifact of that name (stem-<name>.flac or a numbered one).
func (p *planner) stemExists(trackID, name string) (bool, error) {
	var n int
	err := p.q.QueryRowContext(p.ctx, `SELECT EXISTS(SELECT 1 FROM artifacts WHERE track_id = ? AND (name = ? OR name GLOB ?))`,
		trackID, "stem-"+name+".flac", "stem-"+name+"-[0-9]*.flac").Scan(&n)
	if err != nil {
		return false, fmt.Errorf("generation: stem artifacts: %w", err)
	}
	return n == 1, nil
}

// checkRange applies the repaint bounds of the effective caps (C148.7).
func (p *planner) checkRange(i, from, to int) error {
	c := p.eng.Caps()
	lo, hi := orDefault(c.RepaintMinS, acestep.SegmentMinS), orDefault(c.RepaintMaxS, acestep.SegmentMaxS)
	switch n := to - from; {
	case n < 10*lo:
		return invalidItem(i, "edit", "range_too_short")
	case n > 10*hi:
		return invalidItem(i, "edit", "range_too_long")
	}
	return nil
}

// checkExtend applies the extend bounds of the effective caps and the 600 s total (C148.7).
func (p *planner) checkExtend(i, ext, srcDS int) error {
	c := p.eng.Caps()
	lo, hi := orDefault(c.ExtendMinS, acestep.SegmentMinS), orDefault(c.ExtendMaxS, acestep.SegmentMaxS)
	switch {
	case ext < 10*lo:
		return invalidItem(i, "edit.extend_s", "extend_too_short")
	case ext > 10*hi:
		return invalidItem(i, "edit.extend_s", "extend_too_long")
	case srcDS+ext > 10*acestep.AbsoluteMaxS:
		return invalidItem(i, "edit.extend_s", "total_over_600")
	}
	return nil
}

func orDefault(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

// variantCtx builds the ACE-Step context of a variant base (new_seed, *_edit; from_template over them): the parent must be an
// ACE-Step text2music take.
func (p *planner) variantCtx(b base) (*aceCtx, error) {
	if b.parent == nil || b.parent.Engine != acestep.Name {
		return nil, invalidItem(b.item, "parent_track_id", "parent_engine_mismatch")
	}
	if r, _, is := acestep.Parse(json.RawMessage(b.parent.RequestJSON)); acestep.HasErrors(is) || r.Task != acestep.TaskText2Music {
		return nil, invalidItem(b.item, "parent_track_id", "parent_not_text2music")
	}
	pt, err := acePatch(b.item, b.it)
	if err != nil {
		return nil, err
	}
	return &aceCtx{patch: pt}, nil
}

// expandComposeACE expands compose_new of an ACE-Step request: the item's recipe is the whole request (orchestrator
// decision); an optional reference is resolved and pinned like the reference mode's.
func (p *planner) expandComposeACE(i int, it Item) error {
	r, canonical, is := acestep.Parse(it.Recipe)
	if r.Task != "" && r.Task != acestep.TaskText2Music && slices.Contains([]string{acestep.TaskRepaint, acestep.TaskExtend, acestep.TaskComplete, acestep.TaskExtract}, r.Task) {
		return invalidItem(i, "recipe", "task_not_text2music") // edits have their own modes; a new song is text2music
	}
	if acestep.HasErrors(is) {
		return p.mapIssues(i, lineage.ModeComposeNew, r.ID, -1, is)
	}
	var pin *refPin
	if ref := composeRef(it, r); ref != nil {
		rp, err := p.resolveRef(i, *ref)
		if err != nil {
			return err
		}
		pin = &rp
		r.Ref = &rp.Ref
		if canonical, err = acestep.Canonical(r); err != nil {
			return err
		}
	}
	rec, err := p.parseStored(string(canonical))
	if err != nil {
		return err
	}
	spec, _ := lineage.SpecFor(lineage.ModeComposeNew, p.engName)
	ac := &aceCtx{ref: pin, warns: engineIssues(is, "warning")}
	return p.expandACE(base{item: i, it: it, mode: lineage.ModeComposeNew, spec: spec, rec: rec, ace: ac})
}

// composeRef is the reference of a compose_new item: the item's, else the window the recipe's own ref names.
func composeRef(it Item, r acestep.Request) *Reference {
	if it.Reference != nil {
		return it.Reference
	}
	if r.Ref == nil {
		return nil
	}
	from, to := secs(r.Ref.FromDS), secs(r.Ref.ToDS)
	return &Reference{TrackID: r.Ref.TrackID, FromS: &from, ToS: &to}
}

// deriveIssues reads the finding out of an ApplyMode failure: the codec reports a derived request that does not parse as
// "acestep: derived recipe invalid: <code> (<field>)"; anything else is a generic invalid_request.
func deriveIssues(err error) []engine.Issue {
	const marker = "derived recipe invalid: "
	msg := err.Error()
	if i := strings.Index(msg, marker); i >= 0 {
		code, field, _ := strings.Cut(msg[i+len(marker):], " (")
		return []engine.Issue{{Code: code, Field: strings.TrimSuffix(field, ")"), Severity: "error", Message: msg}}
	}
	return []engine.Issue{{Code: "invalid_request", Severity: "error", Message: "the request is not valid"}}
}

// mapIssues turns engine findings into the E2 errors: ENGINE_CAPABILITY, ENGINE_PARAMS_STALE and the INVALID_ITEM rules are
// returned as errors; the rest become a lint failure of the item.
func (p *planner) mapIssues(item int, mode, requestID string, takeIndex int, is []engine.Issue) error {
	var lint []Issue
	for _, x := range is {
		if x.Severity != "error" {
			continue
		}
		switch {
		case x.Code == "ENGINE_CAPABILITY":
			return engineCapabilityErr(acestep.Name, mode, strings.TrimPrefix(x.Message, "capability="))
		case x.Code == "ENGINE_PARAMS_STALE":
			return perr(http.StatusUnprocessableEntity, "ENGINE_PARAMS_STALE", "the engine parameters of the request are not the pinned version",
				map[string]any{"engine_params_version": x.Message, "current": acestep.CurrentParamsVersion()})
		case itemRules[x.Code]:
			return invalidItem(item, x.Field, x.Code)
		}
		lint = append(lint, Issue{Code: x.Code, Field: x.Field, Severity: "error", Message: x.Message})
	}
	if len(lint) > 0 {
		p.failures = append(p.failures, lintFailure{Item: item, TakeIndex: takeIndex, RequestID: requestID, Errors: lint})
	}
	return nil
}

// expandACE renders the takes of one ACE-Step base: seeds (C71.3), ids, the derived request, the engine's validation, the
// planner warnings and the sizes the quote needs.
func (p *planner) expandACE(b base) error {
	if b.ace == nil {
		ac, err := p.variantCtx(b)
		if err != nil {
			return err
		}
		b.ace = ac
	}
	probe, err := p.derive(b, lineage.Params{Mode: b.mode, Seed: b.rec.Seed, ID: b.rec.ID})
	if err != nil {
		return p.mapIssues(b.item, b.mode, b.rec.ID, -1, deriveIssues(err))
	}
	content, _ := lineage.Keys(probe.Canonical)
	seeds, err := p.chooseSeeds(b, content)
	if err != nil {
		return err
	}
	for _, sp := range seeds {
		if err := p.planTakeACE(b, sp, len(seeds)); err != nil {
			return err
		}
	}
	return nil
}

func (p *planner) planTakeACE(b base, sp seedPlan, nSeeds int) error {
	ac := b.ace
	keep := b.mode == lineage.ModeComposeNew && nSeeds == 1 && sp.seed == b.rec.Seed
	id, err := lineage.AllocateID(b.rec.ID, sp.seed, keep, p.taken())
	if p.err != nil {
		return p.err
	}
	if errors.Is(err, lineage.ErrNoFreeID) {
		return invalidItem(b.item, "id", "id_exhausted")
	}
	if err != nil {
		return err
	}
	params := lineage.Params{Mode: b.mode, SeedPolicy: sp.policy, NSeeds: nSeeds, Via: b.via, Seed: sp.seed, ID: id}
	parsed, err := p.derive(b, params)
	if err != nil {
		return p.mapIssues(b.item, b.mode, id, len(p.takes), deriveIssues(err))
	}
	req, _, perrs := acestep.Parse(parsed.Canonical)
	if acestep.HasErrors(perrs) {
		return fmt.Errorf("generation: derived request of %s does not parse: %v", id, perrs)
	}
	content, render := lineage.Keys(parsed.Canonical)
	t := &planTake{
		TakeIndex: len(p.takes), ItemIndex: b.item, Mode: b.mode, Operator: b.spec.Operator,
		RequestJSON: string(parsed.Canonical), RecipeHash: parsed.Hash, ContentHash: content, RenderHash: render,
		RequestID: id, Seed: sp.seed, Warnings: []Issue{},
		Params: paramsDoc{Mode: b.mode, SeedPolicy: sp.policy, NSeeds: nSeeds, Via: b.via, TrialID: p.trial, CrossEngine: ac.cross, Stem: ac.stem},
		Patch:  aceDoc(ac.patch),
	}
	if ac.ref != nil {
		f := ac.ref.frozen()
		t.Ref = &f
	}
	if b.parent != nil {
		pid := b.parent.ID
		t.ParentTrackID = &pid
	}
	explicit, parentTitle := "", ""
	if b.it.Title != nil {
		explicit = *b.it.Title
	}
	if b.parent != nil {
		parentTitle = b.parent.Title
	}
	t.Title = lineage.DefaultTitle(lineage.TitleIn{Explicit: explicit, Variant: isVariant(b.mode), ParentTitle: parentTitle,
		HasMother: b.parent != nil, RequestID: id, Seed: sp.seed})

	// the engine's own validation against the effective caps; findings with an E2 error code are errors, the rest lint failures
	if is := p.eng.Validate(json.RawMessage(parsed.Canonical)); len(is) > 0 {
		if err := p.mapIssues(b.item, b.mode, id, t.TakeIndex, is); err != nil {
			return err
		}
	}
	t.Warnings = append(t.Warnings, ac.warns...)
	t.Warnings = append(t.Warnings, engineIssues(parsed.Warnings, "warning")...)
	t.Warnings = append(t.Warnings, p.refWarnings(ac)...)
	w, err := p.plannerWarnings(b, t, "")
	if err != nil {
		return err
	}
	t.Warnings = append(t.Warnings, w...)
	t.ExpectedAudioS = acestep.ExpectedSeconds(req)
	n, err := p.inputBytesACE(req, parsed.Canonical)
	if err != nil {
		return err
	}
	if n > provider.MaxRunBody {
		return invalidItem(b.item, "recipe", "input_too_large")
	}
	t.InputBytes = n

	info := &takeE2{Task: req.Task, Class: req.Class(), Edit: ac.edit, Cross: ac.cross, Audio: t.ExpectedAudioS, LRC: req.LRC}
	if ac.ref != nil {
		info.Ref = &PlannedRef{TrackID: ac.ref.Ref.TrackID, FromS: secs(ac.ref.Ref.FromDS), ToS: secs(ac.ref.Ref.ToDS), ClipSHA256: ac.ref.Ref.ClipSHA256}
	}
	p.e2[t.TakeIndex] = info

	p.ids[id] = true
	p.planned[content] = append(p.planned[content], sp.seed)
	if _, ok := p.renders[render]; !ok {
		p.renders[render] = t.TakeIndex
	}
	p.takes = append(p.takes, t)
	return nil
}

// refWarnings are the reference warnings (C149.2, D-E2-9): a short window, and a render that is not reproducible until the
// verified file says it is.
func (p *planner) refWarnings(ac *aceCtx) []Issue {
	if ac.ref == nil {
		return nil
	}
	var out []Issue
	if ac.ref.Short {
		out = append(out, warnIssue(WarnReferenceWindowShort, "reference", "the reference window is under 5 s; it is looped to 30 s"))
	}
	if f, ok := aceFeatures(p.eng); !ok || !f.ReferenceReproducible {
		out = append(out, warnIssue(WarnReferenceNotReproducible, "reference", "a reference render is not reproducible from its seed until the canary says so"))
	}
	return out
}

// aceFeatures returns the verified features of an ACE-Step engine value.
func aceFeatures(e engine.Engine) (acestep.Features, bool) {
	switch v := e.(type) {
	case acestep.Engine:
		return v.Verified, true
	case *acestep.Engine:
		return v.Verified, true
	}
	return acestep.Features{}, false
}

// inputBytesACE is the size of the /run body this take would produce with maximal-length placeholder URLs (C73.3).
func (p *planner) inputBytesACE(req acestep.Request, canonical []byte) (int, error) {
	ph := strings.Repeat("u", 2048)
	long := strings.Repeat("x", 64)
	reqd, opt := p.eng.Artifacts(json.RawMessage(canonical))
	uploads := map[string]provider.UploadTarget{}
	for _, name := range append(append([]string(nil), reqd...), opt...) {
		uploads[name] = provider.UploadTarget{URL: ph, MaxBytes: 1 << 30}
	}
	inputs := map[string]provider.InputRef{}
	if req.Src != nil {
		inputs["src_audio"] = provider.InputRef{URL: ph, SHA256: strings.Repeat("0", 64), Bytes: 1 << 30}
	}
	if req.Ref != nil {
		inputs["reference_audio"] = provider.InputRef{URL: ph, SHA256: strings.Repeat("0", 64), Bytes: 1 << 30}
	}
	in := provider.WorkerInput{
		Contract: provider.Contract, TakeID: long, AttemptID: long, Engine: acestep.Name, Request: json.RawMessage(canonical),
		TimeoutS: 2700, UploadBudgetS: 600, DeadlineUnix: 1 << 40, Start: provider.StartTarget{URL: ph}, Uploads: uploads,
		Expect: provider.WorkerExpect{Weights: map[string]string{
			acestep.WeightsRepoMain: strings.Repeat("0", 40), acestep.WeightsRepoBase: strings.Repeat("0", 40),
		}},
	}
	if len(inputs) > 0 {
		in.Inputs = inputs
	}
	b, err := json.Marshal(struct {
		Input provider.WorkerInput `json:"input"`
	}{in})
	if err != nil {
		return 0, fmt.Errorf("generation: job input: %w", err)
	}
	return len(b), nil
}
