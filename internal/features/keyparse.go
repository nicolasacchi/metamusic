package features

import (
	"regexp"
	"strings"
)

var (
	// "in <key>": the tonic letter may be in either case.
	styleInRe = regexp.MustCompile(`(?i:\bin)\s+([A-Ga-g])([#♯b♭]?)\s*(?i:(major|maj|minor|min))\b`)
	// Whole-term key: an upper-case tonic first, then a mode word.
	styleTermRe = regexp.MustCompile(`^([A-G])([#♯b♭]?)\s*(?i:(major|maj|minor|min))\b`)
	// Whole-term shorthand: exactly a tonic plus "m".
	styleMinorRe = regexp.MustCompile(`^([A-G])([#♯b♭]?)m$`)
	abcKeyRe     = regexp.MustCompile(`^([A-Ga-g])([#b]?)\s*([A-Za-z]*)`)
)

var letterPC = map[byte]int{'C': 0, 'D': 2, 'E': 4, 'F': 5, 'G': 7, 'A': 9, 'B': 11}

func tonicOf(letter, accidental string) int {
	pc := letterPC[strings.ToUpper(letter)[0]]
	switch accidental {
	case "#", "♯":
		pc++
	case "b", "♭":
		pc--
	}
	return (pc + 12) % 12
}

func modeOfWord(w string) string {
	if strings.HasPrefix(strings.ToLower(w), "maj") {
		return "major"
	}
	return "minor"
}

// ParseStyleKey reads a key from a style string (C135.3): the first comma-separated term that is "in <key>" or
// starts with an upper-case tonic and a mode word, or is exactly a tonic plus "m". It never reads lyrics.
func ParseStyleKey(style string) (tonicPC int, mode string, ok bool) {
	for _, term := range strings.Split(style, ",") {
		term = strings.TrimSpace(term)
		if m := styleInRe.FindStringSubmatch(term); m != nil {
			return tonicOf(m[1], m[2]), modeOfWord(m[3]), true
		}
		if m := styleTermRe.FindStringSubmatch(term); m != nil {
			return tonicOf(m[1], m[2]), modeOfWord(m[3]), true
		}
		if m := styleMinorRe.FindStringSubmatch(term); m != nil {
			return tonicOf(m[1], m[2]), "minor", true
		}
	}
	return 0, "", false
}

// ParseABCKey reads the first K: header line of an ABC score (C135.3). Modes map to the nearest major or minor with
// the written tonic; K:none, K:HP and a missing header are not ok. Only the first K: line counts.
func ParseABCKey(abc string) (tonicPC int, mode string, ok bool) {
	for _, line := range strings.Split(abc, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "K:") {
			continue
		}
		v := strings.TrimSpace(line[2:])
		if i := strings.IndexByte(v, '%'); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		m := abcKeyRe.FindStringSubmatch(v)
		if m == nil {
			return 0, "", false
		}
		w := strings.ToLower(m[3])
		switch {
		case w == "":
			mode = "major"
		case w == "m":
			mode = "minor"
		case len(w) >= 3 && containsMode(w[:3], "maj", "ion", "lyd", "mix"):
			mode = "major"
		case len(w) >= 3 && containsMode(w[:3], "min", "aeo", "dor", "phr", "loc"):
			mode = "minor"
		default:
			return 0, "", false
		}
		return tonicOf(m[1], m[2]), mode, true
	}
	return 0, "", false
}

func containsMode(p string, set ...string) bool {
	for _, s := range set {
		if p == s {
			return true
		}
	}
	return false
}
