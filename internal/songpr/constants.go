// Package songpr holds the Song Pull Request: a stored, bounded patch (a few lyric lines and/or a style tweak) that names
// the comments it addresses (C253, D-M5-1..3). This file is the one place for the bounds; Compile enforces them.
package songpr

// Bounds and limits of a patch (C250, operator decision 2026-11-03).
const (
	MaxLines          = 8    // distinct lyric lines one patch may replace
	MaxScopedSections = 4    // lyrics sections a patch may touch, addressed spots and extra sections together
	MaxStyleTermDelta = 3    // style terms a patch may swap (the larger of terms added and terms removed)
	MaxBPMDelta       = 20   // largest change of the declared BPM
	MaxAddressed      = 10   // comments one patch may address
	MaxOps            = 20   // operations in one patch
	MaxExtraSections  = 2    // neighbour sections the proposer may add on top of the addressed spots
	MaxLineRunes      = 200  // longest replacement lyric line
	MaxStyleRunes     = 1000 // longest replacement style string
	PatchMaxRenders   = 3    // non-cancelled batches one patch may be rendered in
	MaxSeeds          = 4    // takes of one Song PR render
)

// Patch operators (the Operator Ledger names of the children).
const (
	OperatorLyricEdit = "lyric_edit"
	OperatorStyleEdit = "style_edit"
	OperatorMultiEdit = "multi_edit"
)

// Op kinds.
const (
	OpReplaceLine = "replace_line"
	OpSetStyle    = "set_style"
)

// Stable error codes (C260).
const (
	CodePatchInvalid          = "PATCH_INVALID"
	CodePatchOutOfBounds      = "PATCH_OUT_OF_BOUNDS"
	CodePatchNoChange         = "PATCH_NO_CHANGE"
	CodeCommentNotAddressable = "COMMENT_NOT_ADDRESSABLE"
	CodeRecipeUnknown         = "RECIPE_UNKNOWN"
	CodeLintFailed            = "LINT_FAILED"
)
