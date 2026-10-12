package library

import (
	"strings"
	"unicode"
)

// maxFTSTokens bounds the number of query tokens.
const maxFTSTokens = 8

// ftsQuery turns free user text into a safe FTS5 expression: Unicode
// letter/digit runs, at most 8, each emitted as a quoted prefix term and
// joined by implicit AND. User input never reaches FTS5 syntax. Empty input
// (or input without letters or digits) returns "".
func ftsQuery(q string) string {
	toks := strings.FieldsFunc(q, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(toks) > maxFTSTokens {
		toks = toks[:maxFTSTokens]
	}
	for i, t := range toks {
		toks[i] = `"` + t + `"*`
	}
	return strings.Join(toks, " ")
}
