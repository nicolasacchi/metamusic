package yue2

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

var bpmRe = regexp.MustCompile(`^(?:(\d{2,3})\s*bpm|bpm\s*(\d+))$`)

// StyleTerms splits a style string into normalised terms (max 20, input order) and a BPM hint
// (0 when absent or outside 40..250). BPM terms are not returned as terms.
func StyleTerms(style string) (terms []string, bpm int) {
	parts := strings.FieldsFunc(style, func(r rune) bool { return r == ',' || r == ';' || r == '|' || r == '\n' || r == '\r' })
	seen := map[string]bool{}
	for _, p := range parts {
		t := strings.ToLower(strings.Join(strings.Fields(p), " "))
		if t == "" {
			continue
		}
		if m := bpmRe.FindStringSubmatch(t); m != nil {
			d := m[1]
			if d == "" {
				d = m[2]
			}
			if n, err := strconv.Atoi(d); err == nil && n >= 40 && n <= 250 && bpm == 0 {
				bpm = n
			}
			continue
		}
		if utf8.RuneCountInString(t) > 40 || seen[t] {
			continue
		}
		seen[t] = true
		if len(terms) < 20 {
			terms = append(terms, t)
		}
	}
	return terms, bpm
}
