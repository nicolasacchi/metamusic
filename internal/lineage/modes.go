package lineage

// Modes and operators of the Generation planner (M3 C71.2). The table is data: the planner, the
// estimate validation and TestModes_Table all read it.

// Item modes.
const (
	ModeNewSeed      = "new_seed"
	ModeSameSeedEdit = "same_seed_edit"
	ModeLyricEdit    = "lyric_edit"
	ModeStyleEdit    = "style_edit"
	ModeFromDraft    = "from_draft"
	ModeComposeNew   = "compose_new"
	ModeFromTemplate = "from_template"
)

// Operators written to the Operator Ledger (C71.5).
const (
	OpInferredSibling = "inferred_sibling" // M1
	OpRerollSeed      = "reroll_seed"
	OpSameSeedEdit    = "same_seed_edit"
	OpLyricEdit       = "lyric_edit"
	OpStyleEdit       = "style_edit"
	OpFromDraft       = "from_draft"
	OpComposeNew      = "compose_new"
)

// Rule says whether a patch field is forbidden, optional or required for a mode.
type Rule int

// Patch field rules.
const (
	Forbidden Rule = iota
	Optional
	Required
)

// SeedRule names how the seeds of one base recipe are chosen (C71.3).
type SeedRule string

// Seed rules.
const (
	SeedsNew        SeedRule = "new"         // n new seeds above the parent's
	SeedsParentOnly SeedRule = "parent_only" // [parent seed], n = 1
	SeedsParentNew  SeedRule = "parent+new"  // [parent seed] + n-1 new
	SeedsDraftNew   SeedRule = "draft+new"   // [draft seed] + n-1 new
	SeedsRecipeNew  SeedRule = "recipe+new"  // [recipe seed] + n-1 new
)

// ModeSpec is one row of the C71.2 table.
type ModeSpec struct {
	Mode, Operator string
	// Patch: a patch object is accepted at all; the field rules apply when it is.
	Patch                             bool
	Style, Lyrics, Cot, ABC, CFGScale Rule
	Seeds                             SeedRule
	// Parent: "required" (variants), "optional" (from_draft override), "none" (compose_new), "many" (from_template).
	Parent string

	// E2 engine axis (C149.1). Engines empty = every engine; Task is the ACE-Step task the row renders;
	// Capability names the engine capability the row needs ("" = none beyond the mode itself); Part is
	// "E2a" or "E2b". BPM..DurationS are the acestep15-only patch field rules (rows of the M3 table
	// leave them zero; SpecFor overlays the variant rules for acestep15).
	Engines                                 []string
	Task, Capability, Part                  string
	BPM, KeyScale, TimeSignature, DurationS Rule
}

// Modes is the C71.2 table, one row per mode (from_template borrows the row of its template mode).
var Modes = []ModeSpec{
	{Mode: ModeNewSeed, Operator: OpRerollSeed, Seeds: SeedsNew, Parent: "required"},
	{Mode: ModeSameSeedEdit, Operator: OpSameSeedEdit, Patch: true, Style: Optional, Lyrics: Optional, Cot: Optional, ABC: Optional, CFGScale: Optional, Seeds: SeedsParentOnly, Parent: "required"},
	{Mode: ModeLyricEdit, Operator: OpLyricEdit, Patch: true, Lyrics: Required, Cot: Optional, ABC: Optional, Seeds: SeedsParentNew, Parent: "required"},
	{Mode: ModeStyleEdit, Operator: OpStyleEdit, Patch: true, Style: Required, Cot: Optional, ABC: Optional, CFGScale: Optional, Seeds: SeedsParentNew, Parent: "required"},
	{Mode: ModeFromDraft, Operator: OpFromDraft, Seeds: SeedsDraftNew, Parent: "optional"},
	{Mode: ModeComposeNew, Operator: OpComposeNew, Seeds: SeedsRecipeNew, Parent: "none"},
	{Mode: ModeFromTemplate, Parent: "many"},
}

// Spec returns the row of a mode (M3 table first, then the E2 rows).
func Spec(mode string) (ModeSpec, bool) {
	for _, m := range Modes {
		if m.Mode == mode {
			return m, true
		}
	}
	for _, m := range E2Modes {
		if m.Mode == mode {
			return m, true
		}
	}
	return ModeSpec{}, false
}

// TemplateModes are the inner modes from_template accepts.
var TemplateModes = []string{ModeNewSeed, ModeSameSeedEdit, ModeLyricEdit, ModeStyleEdit}

// IsTemplateMode reports whether m may be a template_mode.
func IsTemplateMode(m string) bool {
	for _, t := range TemplateModes {
		if t == m {
			return true
		}
	}
	return false
}

// CheckPatch validates a patch against the row: it returns the offending field and rule, or "", "".
func (m ModeSpec) CheckPatch(p *Patch) (field, rule string) {
	if p == nil {
		if m.Patch && (m.Style == Required || m.Lyrics == Required) {
			return "patch", "patch_required"
		}
		return "", ""
	}
	if !m.Patch {
		return "patch", "patch_forbidden"
	}
	type f struct {
		name    string
		present bool
		rule    Rule
	}
	for _, x := range []f{
		{"style", p.Style != nil, m.Style}, {"lyrics", p.Lyrics != nil, m.Lyrics}, {"cot", p.Cot != nil, m.Cot},
		{"abc", p.ABC.Set, m.ABC}, {"cfg_scale", p.CFGScale.Set, m.CFGScale},
	} {
		switch {
		case x.present && x.rule == Forbidden:
			return "patch." + x.name, x.name + "_forbidden"
		case !x.present && x.rule == Required:
			return "patch." + x.name, x.name + "_required"
		}
	}
	return "", ""
}

var operators = map[string]bool{
	OpInferredSibling: true, OpRerollSeed: true, OpSameSeedEdit: true, OpLyricEdit: true, OpStyleEdit: true,
	OpFromDraft: true, OpComposeNew: true,
	// E2 (C149.1)
	OpEnginePort: true, OpExtend: true, OpRepaint: true, OpReferenceRender: true, OpCompleteTracks: true, OpExtractStems: true,
}

// ValidOperator reports whether op is an Operator Ledger operator of a generation job or M1's inferred edge.
func ValidOperator(op string) bool { return operators[op] }

// ---- E2: the engine axis, ACE-Step modes and operators (C149.1) ----

// Engine names (spelled here because lineage does not import the engine packages).
const (
	EngineYuE2    = "yue2"
	EngineACEStep = "acestep15"
)

// E2 item modes (acestep15 only).
const (
	ModePort      = "port"
	ModeExtend    = "extend"
	ModeRepaint   = "repaint"
	ModeReference = "reference"
	ModeComplete  = "complete"
	ModeStems     = "stems"
)

// E2 operators written to the Operator Ledger.
const (
	OpEnginePort      = "engine_port"
	OpExtend          = "extend"
	OpRepaint         = "repaint"
	OpReferenceRender = "reference_render"
	OpCompleteTracks  = "complete_tracks"
	OpExtractStems    = "extract_stems"
)

// Parts of the milestone: E2a modes are offered once the engine is adopted, E2b modes only when the verified file
// turns their capability on.
const (
	PartE2a = "E2a"
	PartE2b = "E2b"
)

var aceOnly = []string{EngineACEStep}

// E2Modes are the ACE-Step rows of the C149.1 table. Style maps to the caption and Cot to thinking. The yue2-only
// patch fields (abc, cfg_scale) are forbidden on every acestep15 request. Kept apart from Modes so the M3 table (and
// its pinned test) is unchanged; Spec reads both.
var E2Modes = []ModeSpec{
	{Mode: ModePort, Operator: OpEnginePort, Patch: true, Style: Optional, Lyrics: Optional, BPM: Optional, KeyScale: Optional,
		Seeds: SeedsParentNew, Parent: "required", Engines: aceOnly, Task: "text2music", Part: PartE2a},
	{Mode: ModeExtend, Operator: OpExtend, Patch: true, Style: Optional,
		Seeds: SeedsParentNew, Parent: "required", Engines: aceOnly, Task: "extend", Capability: "extend", Part: PartE2b},
	{Mode: ModeRepaint, Operator: OpRepaint, Patch: true, Style: Optional, Lyrics: Optional,
		Seeds: SeedsParentNew, Parent: "required", Engines: aceOnly, Task: "repaint", Capability: "repaint", Part: PartE2a},
	{Mode: ModeReference, Operator: OpReferenceRender, Patch: true, Style: Optional, Lyrics: Optional, BPM: Optional, KeyScale: Optional, DurationS: Optional,
		Seeds: SeedsParentNew, Parent: "required", Engines: aceOnly, Task: "text2music", Capability: "reference", Part: PartE2a},
	{Mode: ModeComplete, Operator: OpCompleteTracks, Patch: true, Style: Optional,
		Seeds: SeedsParentOnly, Parent: "required", Engines: aceOnly, Task: "complete", Capability: "complete", Part: PartE2b},
	{Mode: ModeStems, Operator: OpExtractStems,
		Seeds: SeedsParentOnly, Parent: "required", Engines: aceOnly, Task: "extract", Capability: "stems", Part: PartE2b},
}

// aceVariantRules are the acestep15 tempo/key/duration rules of the M3 variant modes (text2music parents): a
// same-seed or style edit may re-declare bpm, keyscale and timesignature; a lyric edit may only re-declare the
// duration (lyric length drives it); a new seed edits nothing.
var aceVariantRules = map[string][4]Rule{ // bpm, keyscale, timesignature, duration_s
	ModeSameSeedEdit: {Optional, Optional, Optional, Optional},
	ModeStyleEdit:    {Optional, Optional, Optional, Forbidden},
	ModeLyricEdit:    {Forbidden, Forbidden, Forbidden, Optional},
}

// SpecFor returns the row of a mode as it applies to an engine: ok is false for an unknown mode or an engine the
// mode does not list (the caller answers 422 ENGINE_CAPABILITY capability "mode"). For acestep15 the M3 variant rows
// gain the engine patch field rules; for every other engine they are returned as they are.
func SpecFor(mode, engine string) (ModeSpec, bool) {
	m, ok := Spec(mode)
	if !ok || !ModeAllowed(mode, engine) {
		return ModeSpec{}, false
	}
	if r, v := aceVariantRules[mode]; v && engine == EngineACEStep {
		m.BPM, m.KeyScale, m.TimeSignature, m.DurationS = r[0], r[1], r[2], r[3]
	}
	return m, true
}

// ModeAllowed reports whether mode exists and lists engine (an empty Engines list means every engine).
func ModeAllowed(mode, engine string) bool {
	m, ok := Spec(mode)
	if !ok || (engine != EngineYuE2 && engine != EngineACEStep) {
		return false
	}
	if len(m.Engines) == 0 {
		return true
	}
	for _, e := range m.Engines {
		if e == engine {
			return true
		}
	}
	return false
}

// EnginePresence says which acestep15-only patch fields a request sets (the lineage Patch is yue2's type).
type EnginePresence struct {
	BPM, KeyScale, TimeSignature, DurationS bool
}

func (e EnginePresence) any() bool { return e.BPM || e.KeyScale || e.TimeSignature || e.DurationS }

// CheckPatchFor is CheckPatch for an engine: for yue2 the acestep15-only fields are forbidden; for acestep15 abc
// and cfg_scale are forbidden and the engine fields follow the row. It returns the offending field and rule, or
// "", "". Obtain the row through SpecFor so the variant overlay applies.
func (m ModeSpec) CheckPatchFor(engine string, p *Patch, ep EnginePresence) (field, rule string) {
	if engine != EngineACEStep {
		if ep.any() {
			if !m.Patch {
				return "patch", "patch_forbidden"
			}
			for _, x := range []struct {
				name    string
				present bool
			}{{"bpm", ep.BPM}, {"keyscale", ep.KeyScale}, {"timesignature", ep.TimeSignature}, {"duration_s", ep.DurationS}} {
				if x.present {
					return "patch." + x.name, x.name + "_forbidden"
				}
			}
		}
		return m.CheckPatch(p)
	}
	if p == nil && !ep.any() {
		if m.Patch && (m.Style == Required || m.Lyrics == Required) {
			return "patch", "patch_required"
		}
		return "", ""
	}
	if !m.Patch {
		return "patch", "patch_forbidden"
	}
	if p == nil {
		p = &Patch{}
	}
	type f struct {
		name    string
		present bool
		rule    Rule
	}
	for _, x := range []f{
		{"style", p.Style != nil, m.Style}, {"lyrics", p.Lyrics != nil, m.Lyrics}, {"cot", p.Cot != nil, m.Cot},
		{"abc", p.ABC.Set, Forbidden}, {"cfg_scale", p.CFGScale.Set, Forbidden},
		{"bpm", ep.BPM, m.BPM}, {"keyscale", ep.KeyScale, m.KeyScale},
		{"timesignature", ep.TimeSignature, m.TimeSignature}, {"duration_s", ep.DurationS, m.DurationS},
	} {
		switch {
		case x.present && x.rule == Forbidden:
			return "patch." + x.name, x.name + "_forbidden"
		case !x.present && x.rule == Required:
			return "patch." + x.name, x.name + "_required"
		}
	}
	return "", ""
}
