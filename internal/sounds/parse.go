package sounds

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/textfold"
)

// bpmPartRe is the local twin of yue2's unexported bpmRe: a style part that StyleTerms treats as a BPM term (and so
// never as a descriptive term). TestParse_BPMSkipMatchesStyleTerms pins it to yue2.StyleTerms.
var bpmPartRe = regexp.MustCompile(`^(?:(\d{2,3})\s*bpm|bpm\s*(\d+))$`)

var allDigits = regexp.MustCompile(`^[0-9 ]+$`)

func splitStyle(style string) []string {
	return strings.FieldsFunc(style, func(r rune) bool { return r == ',' || r == ';' || r == '|' || r == '\n' || r == '\r' })
}

// NormTerm is the form a taxonomy term is stored and matched in (D-L1-33, C93.1): textfold.Norm, '-' and '/' replaced
// by a space, whitespace re-collapsed.
func NormTerm(s string) string {
	s = textfold.Norm(s)
	s = strings.NewReplacer("-", " ", "/", " ").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

// Parse matches a style string against the effective taxonomy (D-L1-16, C93.3). It is pure.
func Parse(ix *Index, style string) Parsed {
	var p Parsed
	_, p.BPM = yue2.StyleTerms(style)
	seen := map[Match]bool{}
	unseen := map[string]bool{}
	for _, raw := range splitStyle(style) {
		norm := textfold.Norm(raw)
		if norm == "" || bpmPartRe.MatchString(norm) {
			continue
		}
		part := NormTerm(raw)
		if utf8.RuneCountInString(part) > MaxPartRunes {
			part = strings.TrimSpace(string([]rune(part)[:MaxPartRunes]))
		}
		if part == "" {
			continue
		}
		words := strings.Fields(part)
		matched := false
		for i := 0; i < len(words); {
			n := min(MaxPhraseWords, len(words)-i)
			hit := 0
			for ; n >= 1; n-- {
				if _, ok := ix.phrases[strings.Join(words[i:i+n], " ")]; ok {
					hit = n
					break
				}
			}
			if hit == 0 {
				i++
				continue
			}
			phrase := strings.Join(words[i:i+hit], " ")
			for _, fv := range ix.phrases[phrase] {
				m := Match{Term: phrase, Facet: fv.Facet, Value: fv.Value}
				if !seen[m] {
					seen[m] = true
					p.Matches = append(p.Matches, m)
				}
				if fv.Facet == "language" && p.Language == "" {
					p.Language = fv.Value
				}
			}
			matched = true
			i += hit
		}
		if !matched && utf8.RuneCountInString(part) <= MaxUnmappedRunes && !allDigits.MatchString(part) &&
			!unseen[part] && len(p.Unmapped) < MaxUnmapped {
			unseen[part] = true
			p.Unmapped = append(p.Unmapped, part)
		}
	}
	return p
}
