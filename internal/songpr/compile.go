package songpr

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

// Op is one patch operation: replace_line (SectionIdx, LineIdx, Text) or set_style (Style). SectionIdx and LineIdx index
// yue2.LyricsSections of the base lyrics and its non-empty lines.
type Op struct {
	Op         string
	SectionIdx int
	LineIdx    int
	Text       string
	Style      string
}

// Spot is where an addressed comment sits on the base take, already mapped by the anchor leaf: a section kind and its 1-based
// ordinal (timed bases) and/or the stored section label (untimed bases). Nil on an Addressed means the comment has no time.
type Spot struct {
	Kind    string
	Ordinal int
	Label   string
}

// Addressed is a comment a patch names, with the facts addressability depends on (D-M5-3).
type Addressed struct {
	CommentID string
	Kind      string // must be fix or note
	State     string // must be open or regressed
	Hidden    bool
	OnLineage bool // the comment's track is the base track or an ancestor on its path to the root
	Spot      *Spot
}

// CompileInput is everything Compile needs; it reads no database.
type CompileInput struct {
	Base          yue2.Request
	RecipeKnown   bool // false (a reference or unknown-engine recipe) refuses the patch; the zero value fails closed
	Ops           []Op
	Addressed     []Addressed
	ExtraSections []int
}

// Violation is one failed rule.
type Violation struct {
	Code       string   `json:"code"`
	Rule       string   `json:"rule"`
	OpIndex    *int     `json:"op_index,omitempty"`
	Field      string   `json:"field,omitempty"`
	CommentIDs []string `json:"comment_ids,omitempty"`
	Detail     string   `json:"detail,omitempty"` // no lyric or comment text
}

// CompileError lists every violation (never first-fail).
type CompileError struct{ Violations []Violation }

var codePriority = []string{CodeRecipeUnknown, CodeCommentNotAddressable, CodePatchInvalid, CodePatchOutOfBounds, CodeLintFailed, CodePatchNoChange}

// Code is the most specific code among the violations (recipe, addressability, invalid, out of bounds, lint, no change).
func (e *CompileError) Code() string {
	for _, c := range codePriority {
		for _, v := range e.Violations {
			if v.Code == c {
				return c
			}
		}
	}
	return CodePatchInvalid
}

func (e *CompileError) Error() string {
	parts := make([]string, len(e.Violations))
	for i, v := range e.Violations {
		parts[i] = v.Code + ":" + v.Rule
	}
	return "songpr: " + e.Code() + " (" + strings.Join(parts, ", ") + ")"
}

// LineChange is one replaced lyric line.
type LineChange struct {
	SectionIdx int    `json:"section_idx"`
	LineIdx    int    `json:"line_idx"`
	Before     string `json:"before"`
	After      string `json:"after"`
}

// StyleChange is the style edit.
type StyleChange struct {
	Before  string   `json:"before"`
	After   string   `json:"after"`
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
}

// Diff is the reviewable change; Style is nil without a style edit.
type Diff struct {
	Lyrics []LineChange `json:"lyrics"`
	Style  *StyleChange `json:"style"`
}

// ScopedSection is a lyrics section the patch may touch, with the comments that put it in scope.
type ScopedSection struct {
	Idx        int      `json:"idx"`
	Kind       string   `json:"kind"`
	Label      string   `json:"label"`
	CommentIDs []string `json:"comment_ids,omitempty"`
	Extra      bool     `json:"extra,omitempty"` // added by the proposer, not by an addressed spot
}

// Bounds is the bounds report: what the patch uses of each limit.
type Bounds struct {
	LinesChanged      int `json:"lines_changed"`
	MaxLines          int `json:"max_lines"`
	SectionsScoped    int `json:"sections_scoped"`
	MaxSections       int `json:"max_sections"`
	StyleTermsChanged int `json:"style_terms_changed"`
	MaxStyleTerms     int `json:"max_style_terms"`
	BPMDelta          int `json:"bpm_delta"`
	MaxBPMDelta       int `json:"max_bpm_delta"`
}

// Scope is the scoped sections plus the bounds report (patches.scope_json).
type Scope struct {
	Sections []ScopedSection `json:"sections"`
	Bounds   Bounds          `json:"bounds"`
}

// Patch is the M3 patch the compilation produces: only the changed fields are set.
type Patch struct{ Style, Lyrics *string }

// Compiled is the result of Compile.
type Compiled struct {
	Operator string
	Patch    Patch
	Request  yue2.Request // base with the patch applied (id and seed untouched)
	Diff     Diff
	Scope    Scope
}

var tagRe = regexp.MustCompile(`\[[^\]]*\]`)
var headerLineRe = regexp.MustCompile(`^\s*\[([^\]]+)\]\s*$`)

// lyricLine locates the nth non-empty lyric line of a section in the raw lyrics.
type lyricLine struct{ raw int } // index into the raw line slice

// scanLyrics mirrors yue2.LyricsSections and returns, per section, the raw line numbers of its non-empty lines.
func scanLyrics(lyrics string) (raw []string, sections [][]lyricLine) {
	raw = strings.Split(lyrics, "\n")
	cur := -1
	for i, l := range raw {
		l = strings.TrimSuffix(l, "\r")
		if headerLineRe.MatchString(l) {
			sections = append(sections, nil)
			cur = len(sections) - 1
			continue
		}
		if strings.TrimSpace(l) == "" {
			continue
		}
		if cur < 0 {
			sections = append(sections, nil)
			cur = 0
		}
		sections[cur] = append(sections[cur], lyricLine{i})
	}
	return raw, sections
}

type compiler struct {
	vs []Violation
}

func (c *compiler) add(code, rule, field string, opIdx int, detail string) {
	v := Violation{Code: code, Rule: rule, Field: field, Detail: detail}
	if opIdx >= 0 {
		v.OpIndex = &opIdx
	}
	c.vs = append(c.vs, v)
}

func (c *compiler) has(codes ...string) bool {
	for _, v := range c.vs {
		if slices.Contains(codes, v.Code) {
			return true
		}
	}
	return false
}

func sortedKeys(m map[int]bool) []int {
	k := make([]int, 0, len(m))
	for i := range m {
		k = append(k, i)
	}
	sort.Ints(k)
	return k
}

// Compile validates a patch against the bounds of D-M5-2 and applies it to the base request, pure (C253.2). Every failure is
// collected. A changed style counts as swapping max(terms added, terms removed) terms; the BPM bound applies when both styles
// declare one; the language tag (the one the songwriting lint reads) must be unchanged.
func Compile(in CompileInput) (Compiled, error) {
	c := &compiler{}
	if !in.RecipeKnown {
		c.add(CodeRecipeUnknown, "unknown_engine", "base_track_id", -1, "")
		return Compiled{}, &CompileError{c.vs}
	}
	secs := yue2.LyricsSections(in.Base.Lyrics)
	raw, positions := scanLyrics(in.Base.Lyrics)

	// counts and shapes
	if n := len(in.Ops); n < 1 || n > MaxOps {
		c.add(CodePatchInvalid, "ops_count", "ops", -1, fmt.Sprintf("%d ops", n))
	}
	if n := len(in.Addressed); n < 1 || n > MaxAddressed {
		c.add(CodePatchInvalid, "addresses_count", "addresses_comment_ids", -1, fmt.Sprintf("%d comments", n))
	}
	seenComment := map[string]bool{}
	for _, a := range in.Addressed {
		if seenComment[a.CommentID] {
			c.add(CodePatchInvalid, "duplicate_comment", "addresses_comment_ids", -1, "")
		}
		seenComment[a.CommentID] = true
	}
	if len(in.ExtraSections) > MaxExtraSections {
		c.add(CodePatchInvalid, "extra_sections_count", "extra_sections", -1, "")
	}
	extra := map[int]bool{}
	for _, x := range in.ExtraSections {
		if x < 0 || x >= len(secs) {
			c.add(CodePatchInvalid, "no_such_section", "extra_sections", -1, "")
			continue
		}
		extra[x] = true
	}

	// (1) addressability
	var addressable []Addressed
	for _, a := range in.Addressed {
		why := ""
		switch {
		case a.Hidden:
			why = "hidden"
		case a.Kind != "fix" && a.Kind != "note":
			why = "kind"
		case a.State != "open" && a.State != "regressed":
			why = "state"
		case !a.OnLineage:
			why = "lineage"
		}
		if why != "" {
			c.vs = append(c.vs, Violation{Code: CodeCommentNotAddressable, Rule: why, Field: "addresses_comment_ids", CommentIDs: []string{a.CommentID}})
			continue
		}
		addressable = append(addressable, a)
	}

	// (2) scope
	scopeIdx := map[int][]string{}
	for _, a := range addressable {
		idx := -1
		if a.Spot != nil {
			idx = sectionOfSpot(secs, *a.Spot)
		}
		switch {
		case idx >= 0:
			scopeIdx[idx] = append(scopeIdx[idx], a.CommentID)
		case a.Spot == nil && len(extra) > 0: // a comment without a time is covered by the sections the proposer names
		default:
			c.vs = append(c.vs, Violation{Code: CodePatchOutOfBounds, Rule: "comment_unscoped", Field: "extra_sections", CommentIDs: []string{a.CommentID}})
		}
	}
	scoped := map[int]bool{}
	for i := range scopeIdx {
		scoped[i] = true
	}
	for i := range extra {
		scoped[i] = true
	}
	if len(scoped) > MaxScopedSections {
		c.add(CodePatchOutOfBounds, "too_many_sections", "extra_sections", -1, fmt.Sprintf("%d sections", len(scoped)))
	}

	// (3) ops
	lines := map[[2]int]string{}
	styleOps := 0
	newStyle := in.Base.Style
	for i, op := range in.Ops {
		switch op.Op {
		case OpReplaceLine:
			if op.SectionIdx < 0 || op.SectionIdx >= len(secs) {
				c.add(CodePatchInvalid, "no_such_section", "section_idx", i, "")
				continue
			}
			if op.LineIdx < 0 || op.LineIdx >= len(secs[op.SectionIdx].Lines) {
				c.add(CodePatchInvalid, "no_such_line", "line_idx", i, "")
				continue
			}
			text := strings.TrimSpace(op.Text)
			bad := false
			if n := utf8.RuneCountInString(text); n < 1 || n > MaxLineRunes {
				c.add(CodePatchInvalid, "text_length", "text", i, "")
				bad = true
			}
			if strings.ContainsAny(text, "\r\n") {
				c.add(CodePatchInvalid, "newline", "text", i, "")
				bad = true
			}
			if tagRe.MatchString(text) {
				c.add(CodePatchOutOfBounds, "section_tag", "text", i, "")
				bad = true
			}
			if !scoped[op.SectionIdx] {
				c.add(CodePatchOutOfBounds, "section_out_of_scope", "section_idx", i, "")
				bad = true
			}
			key := [2]int{op.SectionIdx, op.LineIdx}
			if _, dup := lines[key]; dup {
				c.add(CodePatchInvalid, "duplicate_line", "line_idx", i, "")
				continue
			}
			if !bad {
				lines[key] = text
			} else {
				lines[key] = secs[op.SectionIdx].Lines[op.LineIdx] // counted, never applied
			}
		case OpSetStyle:
			styleOps++
			if styleOps > 1 {
				c.add(CodePatchInvalid, "duplicate_style", "style", i, "")
				continue
			}
			s := strings.TrimSpace(op.Style)
			if n := utf8.RuneCountInString(s); n < 1 || n > MaxStyleRunes {
				c.add(CodePatchInvalid, "style_length", "style", i, "")
				continue
			}
			newStyle = s
		default:
			c.add(CodePatchInvalid, "unknown_op", "op", i, "")
		}
	}
	if len(lines) > MaxLines {
		c.add(CodePatchOutOfBounds, "too_many_lines", "ops", -1, fmt.Sprintf("%d lines", len(lines)))
	}

	// style bounds
	var styleChange *StyleChange
	bounds := Bounds{MaxLines: MaxLines, MaxSections: MaxScopedSections, MaxStyleTerms: MaxStyleTermDelta, MaxBPMDelta: MaxBPMDelta, SectionsScoped: len(scoped)}
	if styleOps > 0 && newStyle != in.Base.Style {
		oldT, oldBPM := yue2.StyleTerms(in.Base.Style)
		newT, newBPM := yue2.StyleTerms(newStyle)
		added, removed := diffTerms(newT, oldT), diffTerms(oldT, newT)
		styleChange = &StyleChange{Before: in.Base.Style, After: newStyle, Added: added, Removed: removed}
		bounds.StyleTermsChanged = max(len(added), len(removed))
		if bounds.StyleTermsChanged > MaxStyleTermDelta {
			c.add(CodePatchOutOfBounds, "style_terms", "style", -1, fmt.Sprintf("%d terms", bounds.StyleTermsChanged))
		}
		if oldBPM != 0 && newBPM != 0 {
			d := newBPM - oldBPM
			bounds.BPMDelta = d
			if d < -MaxBPMDelta || d > MaxBPMDelta {
				c.add(CodePatchOutOfBounds, "bpm_delta", "style", -1, fmt.Sprintf("%+d bpm", d))
			}
		}
		if yue2.StyleLanguage(in.Base.Style) != yue2.StyleLanguage(newStyle) {
			c.add(CodePatchOutOfBounds, "language_tag", "style", -1, "")
		}
	}

	// apply (only when the ops themselves are sound)
	var diff Diff
	diff.Lyrics = []LineChange{}
	out := in.Base
	newRaw := slices.Clone(raw)
	if !c.has(CodePatchInvalid, CodePatchOutOfBounds) {
		keys := make([][2]int, 0, len(lines))
		for k := range lines {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			return keys[i][0] < keys[j][0] || (keys[i][0] == keys[j][0] && keys[i][1] < keys[j][1])
		})
		for _, k := range keys {
			before := secs[k[0]].Lines[k[1]]
			if lines[k] == before {
				continue
			}
			pos := positions[k[0]][k[1]].raw
			eol := ""
			if strings.HasSuffix(newRaw[pos], "\r") {
				eol = "\r"
			}
			newRaw[pos] = lines[k] + eol
			diff.Lyrics = append(diff.Lyrics, LineChange{SectionIdx: k[0], LineIdx: k[1], Before: before, After: lines[k]})
		}
		out.Lyrics = strings.Join(newRaw, "\n")
		out.Style = newStyle
		bounds.LinesChanged = len(diff.Lyrics)
		diff.Style = styleChange
		if out.Lyrics == in.Base.Lyrics && out.Style == in.Base.Style || (len(diff.Lyrics) == 0 && styleChange == nil) {
			c.add(CodePatchNoChange, "unchanged", "ops", -1, "")
		}
		if styleChange != nil && len(styleChange.Added) == 0 && len(styleChange.Removed) == 0 && bounds.BPMDelta == 0 && len(diff.Lyrics) == 0 {
			// a reordered or re-spaced style string is no change
			if !c.has(CodePatchNoChange) {
				c.add(CodePatchNoChange, "unchanged", "style", -1, "")
			}
		}
	}

	// (4) protocol and songwriting lint: errors the base already had are not the patch's
	if !c.has(CodePatchInvalid, CodePatchOutOfBounds, CodePatchNoChange) {
		parsed, err := yue2.ParseRequest(yue2.CanonicalJSON(out), yue2.ParseOptions{})
		if err != nil {
			c.add(CodeLintFailed, "parse", "request", -1, err.Error())
		} else {
			baseKnown := map[string]bool{}
			for _, is := range lintErrors(yue2.CanonicalJSON(in.Base)) {
				baseKnown[is.Code+"/"+is.Field] = true
			}
			for _, is := range lintErrors(parsed.Canonical) {
				if !baseKnown[is.Code+"/"+is.Field] {
					c.add(CodeLintFailed, is.Code, is.Field, -1, "")
				}
			}
		}
	}
	if len(c.vs) > 0 {
		return Compiled{}, &CompileError{c.vs}
	}

	// scope report
	var sections []ScopedSection
	for _, i := range sortedKeys(scoped) {
		ids := scopeIdx[i]
		sort.Strings(ids)
		sections = append(sections, ScopedSection{Idx: i, Kind: secs[i].Kind, Label: secs[i].Label, CommentIDs: ids, Extra: len(ids) == 0})
	}
	res := Compiled{Request: out, Diff: diff, Scope: Scope{Sections: sections, Bounds: bounds}}
	switch {
	case len(diff.Lyrics) > 0 && styleChange != nil:
		res.Operator = OperatorMultiEdit
	case styleChange != nil:
		res.Operator = OperatorStyleEdit
	default:
		res.Operator = OperatorLyricEdit
	}
	if len(diff.Lyrics) > 0 {
		l := out.Lyrics
		res.Patch.Lyrics = &l
	}
	if styleChange != nil {
		s := out.Style
		res.Patch.Style = &s
	}
	return res, nil
}

func lintErrors(canonical []byte) []yue2.Issue {
	rep := yue2.Lint([]yue2.LintItem{{Request: canonical}}, false, yue2.LintContext{})
	if len(rep.Items) == 0 {
		return nil
	}
	return rep.Items[0].Errors
}

// sectionOfSpot finds the lyrics section of a spot: the nth section of the kind, else the section with the stored label.
func sectionOfSpot(secs []yue2.Section, s Spot) int {
	if s.Kind != "" && s.Ordinal > 0 {
		n := 0
		for i, x := range secs {
			if x.Kind == s.Kind {
				if n++; n == s.Ordinal {
					return i
				}
			}
		}
	}
	if s.Label != "" {
		for i, x := range secs {
			if x.Label == s.Label {
				return i
			}
		}
	}
	return -1
}

// diffTerms returns the terms of a that are not in b, in a's order.
func diffTerms(a, b []string) []string {
	out := []string{}
	for _, t := range a {
		if !slices.Contains(b, t) {
			out = append(out, t)
		}
	}
	return out
}
