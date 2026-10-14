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

// Spec returns the row of a mode.
func Spec(mode string) (ModeSpec, bool) {
	for _, m := range Modes {
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
}

// ValidOperator reports whether op is an Operator Ledger operator of a generation job or M1's inferred edge.
func ValidOperator(op string) bool { return operators[op] }
