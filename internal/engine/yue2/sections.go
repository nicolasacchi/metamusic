package yue2

import (
	"regexp"
	"strconv"
	"strings"
)

// Section is one lyric section.
type Section struct {
	Idx   int      `json:"idx"`
	Kind  string   `json:"kind"`  // intro|verse|pre-chorus|chorus|bridge|interlude|instrumental|solo|outro|other
	Label string   `json:"label"` // "verse 1" (kind + per-kind occurrence number; single-occurrence kinds unnumbered)
	Raw   string   `json:"raw"`   // "[Verse 1]"
	Lines []string `json:"lines"` // non-empty lyric lines, trimmed
}

var headerRe = regexp.MustCompile(`^\s*\[([^\]]+)\]\s*$`)
var letterHyphenRe = regexp.MustCompile(`(\p{L})-(\p{L})`)
var digitsRe = regexp.MustCompile(`[0-9]+`)

var kindTable = map[string]string{
	"intro": "intro", "verse": "verse", "pre chorus": "pre-chorus", "prechorus": "pre-chorus",
	"chorus": "chorus", "bridge": "bridge", "interlude": "interlude", "instrumental": "instrumental",
	"instrumental break": "instrumental", "solo": "solo", "outro": "outro",
}

// sectionKind maps a header/comment text ("Verse 1", "Outro – Piano + fade") to a kind. known is
// false when the text is not in the synonym table (kind is then "other").
func sectionKind(text string) (kind string, known bool) {
	s := strings.ToLower(text)
	s = letterHyphenRe.ReplaceAllString(s, "$1 $2")
	if i := strings.IndexAny(s, "–—:(+"); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(s, " -"); i >= 0 {
		s = s[:i]
	}
	s = digitsRe.ReplaceAllString(s, "")
	s = strings.Join(strings.Fields(s), " ")
	if k, ok := kindTable[s]; ok {
		return k, true
	}
	return "other", false
}

// labelKinds returns display labels: kind alone when it occurs once, else "kind n".
func labelKinds(kinds []string) []string {
	total := map[string]int{}
	for _, k := range kinds {
		total[k]++
	}
	seen := map[string]int{}
	out := make([]string, len(kinds))
	for i, k := range kinds {
		seen[k]++
		if total[k] == 1 {
			out[i] = k
		} else {
			out[i] = k + " " + strconv.Itoa(seen[k])
		}
	}
	return out
}

// LyricsSections splits lyrics at "[Header]" lines. Text before the first header becomes an "other"
// section only if non-empty.
func LyricsSections(lyrics string) []Section {
	var secs []Section
	cur := -1
	for _, line := range strings.Split(strings.ReplaceAll(lyrics, "\r\n", "\n"), "\n") {
		if m := headerRe.FindStringSubmatch(line); m != nil {
			k, _ := sectionKind(m[1])
			secs = append(secs, Section{Idx: len(secs), Kind: k, Raw: strings.TrimSpace(line)})
			cur = len(secs) - 1
			continue
		}
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if cur < 0 {
			secs = append(secs, Section{Idx: 0, Kind: "other"})
			cur = 0
		}
		secs[cur].Lines = append(secs[cur].Lines, t)
	}
	kinds := make([]string, len(secs))
	for i, s := range secs {
		kinds[i] = s.Kind
	}
	for i, l := range labelKinds(kinds) {
		secs[i].Label = l
	}
	return secs
}
