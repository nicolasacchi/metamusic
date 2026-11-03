package acestep

import (
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

var (
	styleBPMRe  = regexp.MustCompile(`^(?:(\d{2,3})\s*bpm|bpm\s*(\d+))$`)
	headerNumRe = regexp.MustCompile(`[0-9]+`)
)

// lyricsTags maps a yue2 section kind to ACE-Step's tag spelling (lyrics_tags.json).
var lyricsTags = func() map[string]string {
	var f struct {
		Tags map[string]string `json:"tags"`
	}
	if err := json.Unmarshal(tagsJSON, &f); err != nil {
		panic(err)
	}
	return f.Tags
}()

// stripBPMTerms removes the BPM terms of a style string and joins the remaining terms with ", ".
func stripBPMTerms(style string) string {
	parts := strings.FieldsFunc(style, func(r rune) bool { return r == ',' || r == ';' || r == '|' || r == '\n' || r == '\r' })
	var kept []string
	for _, p := range parts {
		t := strings.Join(strings.Fields(p), " ")
		if t == "" || styleBPMRe.MatchString(strings.ToLower(t)) {
			continue
		}
		kept = append(kept, t)
	}
	return strings.Join(kept, ", ")
}

// mapLyrics rewrites yue2 section headers into ACE-Step's tags; text before the first header and headers of
// unknown kind are kept as they are.
func mapLyrics(lyrics string) string {
	var blocks []string
	for _, s := range yue2.LyricsSections(lyrics) {
		var b strings.Builder
		if s.Raw != "" {
			tag := s.Raw
			if t, ok := lyricsTags[s.Kind]; ok {
				tag = "[" + t
				if n := headerNumRe.FindString(s.Raw); n != "" {
					tag += " " + n
				}
				tag += "]"
			}
			b.WriteString(tag)
		}
		for i, l := range s.Lines {
			if i > 0 || s.Raw != "" {
				b.WriteByte('\n')
			}
			b.WriteString(l)
		}
		blocks = append(blocks, b.String())
	}
	return strings.Join(blocks, "\n\n")
}

// truncateRunes cuts s to n runes at a rune boundary.
func truncateRunes(s string, n int) (string, bool) {
	if utf8.RuneCountInString(s) <= n {
		return s, false
	}
	i := 0
	for c := 0; c < n; c++ {
		_, w := utf8.DecodeRuneInString(s[i:])
		i += w
	}
	return s[:i], true
}

// FromYuE2 translates a yue2 request into an ACE-Step text2music request (operator engine_port, C148.9). The
// result is a fully stamped request; warnings (abc_dropped, cot_ignored, duration_estimated, caption_truncated)
// come back as warning issues, and an untranslatable request as error issues.
func FromYuE2(y yue2.Request, newID string) (Request, []engine.Issue) {
	var is []engine.Issue
	caption, cut := truncateRunes(stripBPMTerms(y.Style), MaxCaptionLen)
	if cut {
		is = append(is, warn("caption_truncated", "caption", "caption cut to %d runes", MaxCaptionLen))
	}
	if y.ABC != nil {
		is = append(is, warn("abc_dropped", "abc", "the ABC score does not carry over"))
	}
	if y.Cot != "off" {
		is = append(is, warn("cot_ignored", "cot", "yue2 chain of thought has no ACE-Step equivalent"))
	}
	secs, _ := yue2.EstimateDuration(y)
	if math.IsNaN(secs) || secs < 0 {
		secs = 0
	}
	ds := min(max(10*int(math.Round(secs)), 300), 3600)
	is = append(is, warn("duration_estimated", "duration_ds", "duration estimated from the lyrics"))
	r := Request{ID: newID, Task: TaskText2Music, Caption: caption, Lyrics: mapLyrics(y.Lyrics), Instrumental: strings.TrimSpace(y.Lyrics) == "",
		DurationDS: ds, Seed: y.Seed, Thinking: true, ParamsVersion: CurrentParamsVersion(), Steps: TurboSteps}
	if _, bpm := yue2.StyleTerms(y.Style); bpm > 0 {
		r.BPM = &bpm
	}
	r.VocalLanguage = "unknown"
	if l := yue2.StyleLanguage(y.Style); l != "" {
		r.VocalLanguage = l
	} else if l := yue2.DetectLanguage(y.Lyrics); l != "" {
		r.VocalLanguage = l
	}
	if !slices.Contains(languageList(), r.VocalLanguage) {
		r.VocalLanguage = "unknown"
	}
	if errs := structural(r); HasErrors(errs) {
		return r, append(is, errs...)
	}
	return r, is
}
