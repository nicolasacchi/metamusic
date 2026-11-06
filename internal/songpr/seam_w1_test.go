package songpr_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/songpr"
)

// realWorldLyrics are lyric shapes real recipes carry (structure only, synthetic words): blank lines between and inside
// sections, CRLF line ends, text before the first header, an empty section, back-to-back headers, odd spacing and case,
// repeated sections, and a header-like tag inside a lyric line.
var realWorldLyrics = map[string]string{
	"plain":              "[Verse 1]\nla one\nla two\n[Chorus]\nhook one\nhook two\n[Outro]\nend one",
	"blank lines":        "[Verse 1]\n\nla one\n\nla two\n\n\n[Chorus]\n\nhook one\n\n[Outro]\n\nend one\n\n",
	"crlf":               "[Verse 1]\r\nla one\r\nla two\r\n\r\n[Chorus]\r\nhook one\r\nhook two\r\n",
	"preamble":           "la before\nla also before\n[Verse 1]\nla one\n[Chorus]\nhook one",
	"empty section":      "[Intro]\n[Verse 1]\nla one\n[Instrumental]\n\n[Chorus]\nhook one\nhook two",
	"back to back":       "[Verse 1]\n[Chorus]\nhook one\n[Chorus]\nhook again",
	"spacing and case":   "  [verse 1]  \n   la one  \n\t la two\n[ CHORUS ]\nhook one\n[Outro - fade]\nend one",
	"repeated chorus":    "[Verse 1]\na1\na2\n[Chorus]\nh1\nh2\n[Verse 2]\nb1\nb2\n[Chorus]\nh1\nh2\n[Bridge]\nx1\n[Chorus]\nh1\nh2",
	"inline tag in line": "[Verse 1]\n[whisper] la one\nla two\n[Chorus]\nhook one",
	"single line":        "[Verse 1]\nonly line",
	"unicode":            "[Verse 1]\nè à ì ù\n\n[Chorus]\ncanta più forte",
}

// TestCompile_LineIndexMatchesLyricsSections is the S1/W1 seam: a replace_line op carries SectionOut.idx plus the index of the line
// among the section's non-empty lines (SectionOut.lines), both read from yue2.LyricsSections. For every section and line of every
// shape, replacing (idx, line) changes exactly that line of exactly that section of yue2.LyricsSections, nothing else.
func TestCompile_LineIndexMatchesLyricsSections(t *testing.T) {
	t.Parallel()
	for name, lyrics := range realWorldLyrics {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			secs := yue2.LyricsSections(lyrics)
			tested := 0
			for _, sec := range secs {
				for j, before := range sec.Lines {
					c, err := songpr.Compile(songpr.CompileInput{
						Base:          yue2.Request{ID: "seam", Style: "pop, warm, female vocal, italian, 96bpm", Lyrics: lyrics, Cot: "full", Seed: 7},
						RecipeKnown:   true,
						Ops:           []songpr.Op{{Op: songpr.OpReplaceLine, SectionIdx: sec.Idx, LineIdx: j, Text: "replacement"}},
						Addressed:     []songpr.Addressed{{CommentID: "c", Kind: "note", State: "open", OnLineage: true}},
						ExtraSections: []int{sec.Idx},
					})
					if err != nil {
						t.Fatalf("section %d line %d: %v", sec.Idx, j, err)
					}
					tested++
					want := yue2.LyricsSections(lyrics)
					want[sec.Idx].Lines = append([]string(nil), want[sec.Idx].Lines...)
					want[sec.Idx].Lines[j] = "replacement"
					got := yue2.LyricsSections(c.Request.Lyrics)
					if d := cmp.Diff(want, got); d != "" {
						t.Errorf("section %d line %d: the edit landed elsewhere (-want +got):\n%s", sec.Idx, j, d)
					}
					if len(c.Diff.Lyrics) != 1 || c.Diff.Lyrics[0].SectionIdx != sec.Idx || c.Diff.Lyrics[0].LineIdx != j || strings.TrimSpace(c.Diff.Lyrics[0].Before) != before {
						t.Errorf("section %d line %d: diff = %+v, want before %q", sec.Idx, j, c.Diff.Lyrics, before)
					}
				}
			}
			if tested == 0 {
				t.Fatal("no editable line in the shape")
			}
			// one past the last line of a section is refused, never silently spilled into the next section
			last := secs[len(secs)-1]
			_, err := songpr.Compile(songpr.CompileInput{
				Base: yue2.Request{ID: "seam", Style: "pop, warm, female vocal, italian, 96bpm", Lyrics: lyrics, Cot: "full", Seed: 7}, RecipeKnown: true,
				Ops:       []songpr.Op{{Op: songpr.OpReplaceLine, SectionIdx: last.Idx, LineIdx: len(last.Lines), Text: "replacement"}},
				Addressed: []songpr.Addressed{{CommentID: "c", Kind: "note", State: "open", OnLineage: true}}, ExtraSections: []int{last.Idx},
			})
			if err == nil {
				t.Errorf("a line index past the section (%d of %d lines) was accepted", len(last.Lines), len(last.Lines))
			}
		})
	}
}
