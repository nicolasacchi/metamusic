package yue2

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Lint types and signatures are frozen by M2a-B3 (contract C32); the bodies are implemented by
// M2a-B5.

// LintItem is one request to lint.
type LintItem struct {
	Request        json.RawMessage // the request object to lint
	DraftID        string          // the draft being linted, if any
	SourceRecipeID string          // that draft's frozen source recipe, if any
	Carry          []Issue         // warnings of an earlier parse of the same item (upload lines)
}

// LintContext supplies the database-backed duplicate check.
type LintContext struct {
	// KnownHash reports an existing recipe or draft with that canonical hash, ignoring the item's own
	// draft and its own source recipe. Returns "recipe" | "draft:<id>". nil means no duplicate check.
	KnownHash func(hash, selfDraftID, selfRecipeID string) (where string, ok bool)
	// Targets are the operator's duration targets; nil means no duration_under_target / duration_outside_target warnings.
	Targets *DurationTargets
}

// DurationTargets are the songwriting duration targets in seconds (MinS <= TargetMinS <= TargetMaxS).
type DurationTargets struct {
	MinS, TargetMinS, TargetMaxS int
}

// LintResult is the lint outcome of one item.
type LintResult struct {
	Index              int      `json:"index"`
	RequestID          string   `json:"request_id"`
	Hash               string   `json:"hash"`
	Errors             []Issue  `json:"errors" nullable:"false"`
	Warnings           []Issue  `json:"warnings" nullable:"false"`
	EstimatedDurationS float64  `json:"estimated_duration_s"`
	DurationBasis      string   `json:"duration_basis"` // abc | lyrics_style_bpm | lyrics_default_bpm
	Language           Language `json:"language"`
}

// Language holds the detected and the style-tagged language (ISO 639-1, "" when unknown).
type Language struct {
	Detected string `json:"detected"`
	Tagged   string `json:"tagged"`
}

// LintCounts is the DraftSummary.lint shape.
type LintCounts struct {
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
}

// LintReport is the outcome of linting a list of items.
type LintReport struct {
	OK          bool         `json:"ok"`
	Items       []LintResult `json:"items" nullable:"false"`
	BatchErrors []Issue      `json:"batch_errors" nullable:"false"`
}

const (
	lintMaxDurationS = 360.0
	lintMinBPM       = 60
	lintMaxBPM       = 180
)

// Lint runs the protocol checks (CheckRequest in ModeApp, plus each item's Carry) and, for
// protocol-clean items, the songwriting rules of C32. batch adds the duplicate_id check across items.
// An item's Request is linted as given: callers pass the canonical request of drafts and upload lines.
func Lint(items []LintItem, batch bool, c LintContext) LintReport {
	rep := LintReport{OK: true, Items: make([]LintResult, 0, len(items)), BatchErrors: []Issue{}}
	seenID := map[string]int{}
	seenHash := map[string]int{}
	for i, it := range items {
		res := LintResult{Index: i, Errors: []Issue{}, Warnings: []Issue{}}
		for _, is := range CheckRequest(it.Request, ModeApp, nil) {
			if is.Severity == sevError {
				res.Errors = append(res.Errors, is)
			} else {
				res.Warnings = append(res.Warnings, is)
			}
		}
		for _, is := range it.Carry {
			if is.Severity == sevError {
				res.Errors = append(res.Errors, is)
			} else {
				res.Warnings = append(res.Warnings, is)
			}
		}
		p, perr := ParseRequest(it.Request, ParseOptions{})
		if perr == nil {
			res.RequestID, res.Hash = p.Request.ID, p.Hash
		} else {
			res.RequestID = looseID(it.Request)
		}
		if perr == nil && len(res.Errors) == 0 {
			lintSongwriting(&res, p.Request, c.Targets)
			if where, dup := knownOrEarlier(c, p.Hash, it, seenHash); dup {
				res.Warnings = append(res.Warnings, Issue{Code: "duplicate_recipe", Field: "recipe", Severity: sevWarning,
					Message: "same canonical request as " + where})
			}
			if _, ok := seenHash[p.Hash]; !ok {
				seenHash[p.Hash] = i + 1
			}
		}
		if batch && res.RequestID != "" {
			if _, dup := seenID[res.RequestID]; dup {
				rep.BatchErrors = append(rep.BatchErrors, Issue{Code: "duplicate_id", Field: "id", Severity: sevError,
					Message: "id already used by an earlier item", Line: i + 1})
			} else {
				seenID[res.RequestID] = i + 1
			}
		}
		if len(res.Errors) > 0 {
			rep.OK = false
		}
		rep.Items = append(rep.Items, res)
	}
	if len(rep.BatchErrors) > 0 {
		rep.OK = false
	}
	return rep
}

func looseID(raw json.RawMessage) string {
	var h struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &h) != nil {
		return ""
	}
	return h.ID
}

func knownOrEarlier(c LintContext, hash string, it LintItem, seen map[string]int) (string, bool) {
	if c.KnownHash != nil {
		if where, ok := c.KnownHash(hash, it.DraftID, it.SourceRecipeID); ok {
			return where, true
		}
	}
	if n, ok := seen[hash]; ok {
		return "item " + strconv.Itoa(n), true
	}
	return "", false
}

var (
	nonSungParenRe = regexp.MustCompile(`(?i)\((repeat|rit\.?|bis|x\s*\d+|\d+\s*x|instrumental|chorus)[^)]*\)`)
	nonSungTokenRe = regexp.MustCompile(`(?i)(?:^|[\s,;])(?:[x×]\s?\d+|\d+\s?[x×]|bis)(?:$|[\s,;.!?])`)
	repeatAnnotRe  = regexp.MustCompile(`(?i)(?:^|\s)(?:[x×]\s?\d+|\d+\s?[x×])\s*$|\(?\s*(?:repeat|bis)\b[^)]*\)?\s*$`)
	trailAnnotRe   = regexp.MustCompile(`(?i)^\s*\(?\s*(?:repeat|bis|[x×]\s?\d+|\d+\s?[x×])\b?[^)]*\)?\s*$`)
	looseHeaderRe  = regexp.MustCompile(`^\s*\[([^\]]+)\]\s*(.*?)\s*$`)
)

type lyricLine struct {
	no   int
	text string
}

type lintSection struct {
	kind, label string
	headerNo    int // 0 for text before the first header
	header      bool
	known       bool
	repeatMark  bool
	lines       []lyricLine
}

// scanLyrics splits lyrics like LyricsSections but keeps 1-based line numbers, recognises a header
// followed by a repeat annotation ("[Chorus] (repeat)") and detects annotated headers ("[Chorus x2]").
func scanLyrics(lyrics string) []lintSection {
	var secs []lintSection
	cur := -1
	for i, line := range strings.Split(strings.ReplaceAll(lyrics, "\r\n", "\n"), "\n") {
		no := i + 1
		if m := looseHeaderRe.FindStringSubmatch(line); m != nil && (m[2] == "" || trailAnnotRe.MatchString(m[2])) {
			text, mark := m[1], m[2] != ""
			if repeatAnnotRe.MatchString(text) {
				mark = true
				text = strings.TrimSpace(repeatAnnotRe.ReplaceAllString(text, ""))
			}
			k, known := sectionKind(text)
			secs = append(secs, lintSection{kind: k, headerNo: no, header: true, known: known, repeatMark: mark})
			cur = len(secs) - 1
			continue
		}
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if cur < 0 {
			secs = append(secs, lintSection{kind: "other"})
			cur = 0
		}
		secs[cur].lines = append(secs[cur].lines, lyricLine{no, t})
	}
	kinds := make([]string, len(secs))
	for i, s := range secs {
		kinds[i] = s.kind
	}
	for i, l := range labelKinds(kinds) {
		secs[i].label = l
	}
	return secs
}

var languageTags = map[string]string{
	"italian": "it", "italiano": "it", "english": "en", "spanish": "es", "español": "es", "espanol": "es",
	"french": "fr", "français": "fr", "francais": "fr", "german": "de", "deutsch": "de",
	"portuguese": "pt", "português": "pt", "portugues": "pt", //nolint:misspell // the ASCII spelling of português
}

// styleLanguage returns the language tagged in the style string, or "".
func styleLanguage(style string) string {
	for _, w := range strings.FieldsFunc(strings.ToLower(style), func(r rune) bool { return !unicode.IsLetter(r) }) {
		if code, ok := languageTags[w]; ok {
			return code
		}
	}
	return ""
}

var exemptEmpty = map[string]bool{"intro": true, "outro": true, "instrumental": true, "interlude": true, "solo": true}

func lintSongwriting(res *LintResult, r Request, targets *DurationTargets) {
	warn := func(code, field, msg string, line int, section string) {
		res.Warnings = append(res.Warnings, Issue{Code: code, Field: field, Severity: sevWarning, Message: msg, Line: line, Section: section})
	}
	secs := scanLyrics(r.Lyrics)
	hasHeader := false
	chorusWithLines := false
	for _, s := range secs {
		if s.header {
			hasHeader = true
		}
	}
	if !hasHeader {
		warn("no_sections", "lyrics", "lyrics contain no [Section] header", 0, "")
	}
	for _, s := range secs {
		if s.header && !s.known {
			warn("unknown_section_tag", "lyrics", "section header is not a known kind", s.headerNo, s.label)
		}
		if s.repeatMark {
			warn("repeat_marker", "lyrics", "write repeated sections out instead of marking the repeat", s.headerNo, s.label)
		}
		for _, l := range s.lines {
			if nonSungParenRe.MatchString(l.text) || nonSungTokenRe.MatchString(l.text) {
				warn("non_sung_text", "lyrics", "line looks like a performance instruction, not sung text", l.no, s.label)
			}
		}
		switch {
		case !s.header:
		case len(s.lines) == 0 && s.kind == "chorus" && chorusWithLines:
			warn("repeat_not_written_out", "lyrics", "chorus is empty but an earlier chorus has lines; write it out", s.headerNo, s.label)
		case len(s.lines) == 0 && !exemptEmpty[s.kind]:
			warn("empty_section", "lyrics", "section has no lines", s.headerNo, s.label)
		}
		if s.kind == "chorus" && len(s.lines) > 0 {
			chorusWithLines = true
		}
		switch s.kind {
		case "verse", "pre-chorus", "chorus", "bridge":
			if n := len(s.lines); n > 0 && (n < 4 || n > 6) {
				warn("section_line_count", "lyrics", "section should have 4 to 6 lines", s.headerNo, s.label)
			}
		}
	}
	res.EstimatedDurationS, res.DurationBasis = EstimateDuration(r)
	if res.EstimatedDurationS > lintMaxDurationS {
		warn("duration_over_cap", "lyrics", "estimated duration is over 360 seconds", 0, "")
	}
	if targets != nil {
		// under the minimum is its own warning; the band warning covers the rest (between min and the band, or above it)
		code, msg := "", ""
		switch est := res.EstimatedDurationS; {
		case est < float64(targets.MinS):
			code, msg = "duration_under_target", "estimated duration is under the minimum song length"
		case est < float64(targets.TargetMinS) || est > float64(targets.TargetMaxS):
			code, msg = "duration_outside_target", "estimated duration is outside the target band"
		}
		if code != "" {
			res.Warnings = append(res.Warnings, Issue{Code: code, Field: "lyrics", Severity: sevWarning, Message: msg, Details: map[string]any{
				"estimated_s": math.Round(res.EstimatedDurationS*10) / 10, "min_s": targets.MinS,
				"target_min_s": targets.TargetMinS, "target_max_s": targets.TargetMaxS}})
		}
	}
	if _, bpm := StyleTerms(r.Style); bpm != 0 && (bpm < lintMinBPM || bpm > lintMaxBPM) {
		warn("bpm_out_of_range", "style", "BPM is outside 60 to 180", 0, "")
	}
	res.Language.Tagged = styleLanguage(r.Style)
	res.Language.Detected = DetectLanguage(r.Lyrics)
	if res.Language.Tagged == "" {
		warn("missing_language_tag", "style", "style names no language", 0, "")
	}
	if t, d := res.Language.Tagged, res.Language.Detected; t != "" && d != "" && t != d {
		warn("language_mismatch", "style", "tagged language differs from the detected lyrics language", 0, "")
	}
	if res.Language.Tagged == "it" || res.Language.Detected == "it" {
		warn("italian_unofficial", "style", "YuE2 does not officially support Italian", 0, "")
	}
}
