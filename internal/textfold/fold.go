// Package textfold folds text to ASCII-friendly forms. It is a leaf: it imports only golang.org/x/text.
package textfold

import (
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

var table = strings.NewReplacer(
	"ß", "ss", "ẞ", "ss", "æ", "ae", "Æ", "AE", "œ", "oe", "Œ", "OE", "ø", "o", "Ø", "O",
	"ł", "l", "Ł", "L", "đ", "d", "Đ", "D", "þ", "th", "Þ", "TH",
)

// Fold applies NFKD, strips combining marks (Mn) and applies the D-M1-3 table. Case is kept.
func Fold(s string) string {
	t := transform.Chain(norm.NFKD, runes.Remove(runes.In(unicode.Mn)))
	folded, _, err := transform.String(t, s)
	if err != nil {
		folded = s
	}
	return table.Replace(folded)
}
