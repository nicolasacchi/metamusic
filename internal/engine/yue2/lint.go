package yue2

import "encoding/json"

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
}

// LintResult is the lint outcome of one item.
type LintResult struct {
	Index              int      `json:"index"`
	RequestID          string   `json:"request_id"`
	Hash               string   `json:"hash"`
	Errors             []Issue  `json:"errors"`
	Warnings           []Issue  `json:"warnings"`
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
	Items       []LintResult `json:"items"`
	BatchErrors []Issue      `json:"batch_errors"`
}

// Lint runs protocol and songwriting checks.
func Lint(items []LintItem, batch bool, c LintContext) LintReport {
	// STUB(M2a-B5)
	return LintReport{}
}
