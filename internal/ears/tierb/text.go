package tierb

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/textfold"
)

// wordRune reports whether r belongs to a word: letters, digits and combining marks (kept so a decomposed accent
// never splits a word).
func wordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r)
}

// Norm is NFC, lower case, every rune that is not a letter or digit (apostrophes included, so elisions split)
// turned into a space, spaces collapsed. Accents are kept.
func Norm(s string) string {
	return strings.Join(tokens(s), " ")
}

// Fold is Norm with accents removed (textfold.Fold, then lower case).
func Fold(s string) string {
	return strings.ToLower(textfold.Fold(Norm(s)))
}

// tokens splits s into its words, lower-cased, NFC.
func tokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(norm.NFC.String(s)), func(r rune) bool { return !wordRune(r) })
}

// RefWord is one word of a lyric line. Section is the yue2 section index, Line the index within the section's
// lyric lines and Word the index within the line.
type RefWord struct {
	Section int
	Line    int
	Word    int
	Text    string // as written (NFC), without surrounding punctuation
	Norm    string
	Fold    string
}

// RefWords returns the words of every lyric line of yue2.LyricsSections(lyrics), in order. Sections without
// lines are instrumental and contribute none.
func RefWords(lyrics string) []RefWord {
	return refWordsOf(yue2.LyricsSections(lyrics))
}

func refWordsOf(secs []yue2.Section) []RefWord {
	var out []RefWord
	for _, s := range secs {
		for li, line := range s.Lines {
			n := 0
			for _, t := range strings.FieldsFunc(norm.NFC.String(line), func(r rune) bool { return !wordRune(r) }) {
				out = append(out, RefWord{Section: s.Idx, Line: li, Word: n, Text: t, Norm: strings.ToLower(t), Fold: Fold(t)})
				n++
			}
		}
	}
	return out
}
