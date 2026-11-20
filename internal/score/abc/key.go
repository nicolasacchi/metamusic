package abc

import (
	"strings"
)

// Key is a parsed K: field.
type Key struct {
	Raw         string
	Tonic       string // "C", "F#", "Bb"
	Mode        string // major minor mixolydian dorian phrygian lydian locrian
	Sharps      int    // -7..7, the key signature as a count of sharps (flats negative)
	Accidentals [7]int // semitone offset per letter, index 0 = A ... 6 = G (signature plus explicit accidentals)
}

var (
	sharpOrder   = []byte("FCGDAEB")
	flatOrder    = []byte("BEADGCF")
	letterFifths = map[byte]int{'C': 0, 'G': 1, 'D': 2, 'A': 3, 'E': 4, 'B': 5, 'F': -1}
	modeOffset   = map[string]int{"major": 0, "minor": -3, "mixolydian": -1, "dorian": -2, "phrygian": -4, "lydian": 1, "locrian": -5}
	modeWords    = map[string]string{"maj": "major", "ion": "major", "min": "minor", "m": "minor", "aeo": "minor", "mix": "mixolydian",
		"dor": "dorian", "phr": "phrygian", "lyd": "lydian", "loc": "locrian"}
)

func modeOf(word string) (string, bool) {
	w := strings.ToLower(word)
	if w == "m" {
		return "minor", true
	}
	if len(w) >= 3 {
		m, ok := modeWords[w[:3]]
		return m, ok
	}
	return "", false
}

func letterIdx(b byte) int { return int(b-'A') % 7 }

// signature fills Accidentals from a sharps count.
func signature(sharps int) (acc [7]int) {
	if sharps > 0 {
		for i := 0; i < sharps && i < 7; i++ {
			acc[letterIdx(sharpOrder[i])] = 1
		}
	} else {
		for i := 0; i < -sharps && i < 7; i++ {
			acc[letterIdx(flatOrder[i])] = -1
		}
	}
	return acc
}

// parseKey reads the value of a K: field. ok is false when no tonic can be read; warnings are codes.
func parseKey(raw string) (k Key, ok bool, warns []string) {
	k = Key{Raw: raw, Tonic: "C", Mode: "major"}
	v := raw
	if i := strings.IndexByte(v, '"'); i >= 0 {
		v = v[:i]
	}
	f := strings.Fields(v)
	if len(f) == 0 || strings.EqualFold(f[0], "none") {
		k.Accidentals = signature(0)
		return k, true, nil
	}
	first := f[0]
	rest := f[1:]
	if strings.EqualFold(first, "HP") {
		k.Tonic, k.Sharps = "D", 2
		k.Accidentals = signature(2)
		return k, true, []string{"key_highland"}
	}
	c := first[0]
	if c < 'A' || c > 'G' {
		return k, false, nil
	}
	tonic := string(c)
	j := 1
	acc := 0
	if j < len(first) && first[j] == '#' {
		acc, tonic, j = 1, tonic+"#", j+1
	} else if j < len(first) && first[j] == 'b' {
		acc, tonic, j = -1, tonic+"b", j+1
	}
	mode := "major"
	if r := first[j:]; r != "" {
		m, ok := modeOf(r)
		if !ok {
			return k, false, nil
		}
		mode = m
	} else if len(rest) > 0 {
		if m, ok := modeOf(rest[0]); ok && !strings.EqualFold(rest[0], "exp") {
			mode = m
			rest = rest[1:]
		}
	}
	sharps := letterFifths[c] + 7*acc + modeOffset[mode]
	var warn []string
	if sharps > 7 || sharps < -7 {
		warn = append(warn, "key_extreme")
		sharps = max(-7, min(7, sharps))
	}
	k.Tonic, k.Mode, k.Sharps = tonic, mode, sharps
	k.Accidentals = signature(sharps)
	explicit := false
	for _, t := range rest {
		switch {
		case strings.EqualFold(t, "exp"):
			explicit = true
			k.Accidentals = [7]int{}
		case len(t) == 2 && (t[0] == '^' || t[0] == '_' || t[0] == '=') && isLetter(t[1]):
			l := strings.ToUpper(t[1:])[0]
			if l >= 'A' && l <= 'G' {
				k.Accidentals[letterIdx(l)] = map[byte]int{'^': 1, '_': -1, '=': 0}[t[0]]
			}
		}
	}
	_ = explicit
	return k, true, warn
}

func isLetter(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }
