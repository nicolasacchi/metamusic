package yue2

import (
	"strings"
	"unicode"
)

var stopIndex = func() map[string][]string {
	idx := map[string][]string{}
	for code, words := range stopWords {
		for _, w := range words {
			idx[w] = append(idx[w], code)
		}
	}
	return idx
}()

// DetectLanguage returns an ISO 639-1 code for the lyrics, or "" when unknown. Lowercase word tokens
// (section headers excluded) are scored against fixed stop-word lists; the top language wins when it
// has at least 5 hits and at least 1.5 times the runner-up's.
func DetectLanguage(lyrics string) string {
	score := map[string]int{}
	for _, line := range strings.Split(lyrics, "\n") {
		if headerRe.MatchString(line) {
			continue
		}
		words := strings.FieldsFunc(strings.ToLower(line), func(r rune) bool { return !unicode.IsLetter(r) })
		for _, w := range words {
			for _, code := range stopIndex[w] {
				score[code]++
			}
		}
	}
	best, bestN, second := "", 0, 0
	for code, n := range score {
		switch {
		case n > bestN || (n == bestN && code < best):
			second, best, bestN = bestN, code, n
		case n > second:
			second = n
		}
	}
	if bestN < 5 || float64(bestN) < 1.5*float64(second) {
		return ""
	}
	return best
}
