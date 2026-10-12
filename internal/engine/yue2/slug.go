package yue2

import (
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

var foldTable = strings.NewReplacer(
	"ß", "ss", "ẞ", "ss", "æ", "ae", "Æ", "AE", "œ", "oe", "Œ", "OE", "ø", "o", "Ø", "O",
	"ł", "l", "Ł", "L", "đ", "d", "Đ", "D", "þ", "th", "Þ", "TH",
)

// Slug is an ASCII [a-z0-9_] filename-safe form of s (D-M1-3): NFKD, combining marks removed, a fixed
// fold table, lower-case, runs of anything else collapsed to "_", trimmed, at most 80 bytes.
func Slug(s string) string {
	t := transform.Chain(norm.NFKD, runes.Remove(runes.In(unicode.Mn)))
	folded, _, err := transform.String(t, s)
	if err != nil {
		folded = s
	}
	folded = strings.ToLower(foldTable.Replace(folded))
	var b strings.Builder
	under := true // suppress leading "_"
	for _, r := range folded {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			under = false
		} else if !under {
			b.WriteByte('_')
			under = true
		}
	}
	out := strings.TrimRight(b.String(), "_")
	if len(out) > 80 {
		out = strings.TrimRight(out[:80], "_")
	}
	return out
}
