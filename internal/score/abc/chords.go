package abc

import "strings"

// ChordMode selects how chord symbols sound in the event stream.
type ChordMode string

const (
	ChordsNone  ChordMode = "none"
	ChordsBlock ChordMode = "block"
)

// chordIntervals are the semitone offsets of each canonical quality, close position from the root.
var chordIntervals = map[string][]int{
	"maj": {0, 4, 7}, "m": {0, 3, 7}, "7": {0, 4, 7, 10}, "maj7": {0, 4, 7, 11}, "m7": {0, 3, 7, 10},
	"dim": {0, 3, 6}, "dim7": {0, 3, 6, 9}, "aug": {0, 4, 8}, "sus2": {0, 2, 7}, "sus4": {0, 5, 7},
	"6": {0, 4, 7, 9}, "m6": {0, 3, 7, 9}, "9": {0, 4, 7, 10, 14}, "add9": {0, 4, 7, 14},
}

var qualityAlias = map[string]string{
	"": "maj", "maj": "maj", "M": "maj", "m": "m", "min": "m", "mi": "m", "-": "m", "7": "7", "dom7": "7",
	"maj7": "maj7", "M7": "maj7", "m7": "m7", "min7": "m7", "-7": "m7", "dim": "dim", "dim7": "dim7", "aug": "aug", "+": "aug",
	"sus2": "sus2", "sus4": "sus4", "sus": "sus4", "6": "6", "m6": "m6", "min6": "m6", "9": "9", "add9": "add9",
}

var noteClass = map[byte]int{'C': 0, 'D': 2, 'E': 4, 'F': 5, 'G': 7, 'A': 9, 'B': 11}

// parseChordSymbol reads the text between the quotes of a chord symbol. ok is false when it is not a chord (the caller
// decides whether that is an annotation or a warning).
func parseChordSymbol(text string, at Rat) (Chord, bool) {
	s := strings.TrimSpace(text)
	if s == "" {
		return Chord{}, false
	}
	pc, ok := noteClass[s[0]]
	if !ok {
		return Chord{}, false
	}
	i := 1
	if i < len(s) && s[i] == '#' {
		pc, i = pc+1, i+1
	} else if i < len(s) && s[i] == 'b' {
		pc, i = pc-1, i+1
	}
	q := s[i:]
	if j := strings.IndexByte(q, '/'); j >= 0 { // slash bass: the bass note is not sounded
		q = q[:j]
	}
	canon, ok := qualityAlias[q]
	if !ok {
		return Chord{}, false
	}
	return Chord{Root: (pc%12 + 12) % 12, Quality: canon, Raw: text, At: at}, true
}

// ChordNotes returns the MIDI pitches of a chord symbol in close position, its root in 48..59.
func ChordNotes(c Chord) []int {
	iv := chordIntervals[c.Quality]
	out := make([]int, len(iv))
	for i, x := range iv {
		out[i] = 48 + c.Root + x
	}
	return out
}
