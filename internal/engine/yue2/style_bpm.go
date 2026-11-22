package yue2

import (
	"regexp"
	"strconv"
	"strings"
)

var bpmDigitsRe = regexp.MustCompile(`\d+`)

// WithBPM rewrites the BPM hint of a style string (M6N C373.4). It follows exactly the term rule of StyleTerms: the term it
// replaces is the one StyleTerms reads as the hint (the first BPM term inside 40..250), and only that term's digits change,
// so spelling ("BPM 92", "100bpm"), case and separators survive. A style without such a term, or a bpm outside 40..250,
// returns (style, false).
func WithBPM(style string, bpm int) (string, bool) {
	if bpm < 40 || bpm > 250 {
		return style, false
	}
	start := 0
	for i := 0; i <= len(style); i++ {
		if i < len(style) {
			switch style[i] {
			case ',', ';', '|', '\n', '\r':
			default:
				continue
			}
		}
		part := style[start:i]
		if loc := bpmTermDigits(part); loc != nil {
			return style[:start+loc[0]] + strconv.Itoa(bpm) + style[start+loc[1]:], true
		}
		start = i + 1
	}
	return style, false
}

// bpmTermDigits locates the digits of part when StyleTerms would read it as the BPM hint, else nil.
func bpmTermDigits(part string) []int {
	t := strings.ToLower(strings.Join(strings.Fields(part), " "))
	m := bpmRe.FindStringSubmatch(t)
	if m == nil {
		return nil
	}
	d := m[1]
	if d == "" {
		d = m[2]
	}
	if n, err := strconv.Atoi(d); err != nil || n < 40 || n > 250 {
		return nil
	}
	return bpmDigitsRe.FindStringIndex(part)
}
