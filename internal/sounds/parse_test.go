package sounds_test

import (
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/sounds"
)

func seedIndex() *sounds.Index {
	var rows []sounds.TermRow
	for _, s := range sounds.SeedV1() {
		rows = append(rows, sounds.TermRow{Version: 1, Term: s.Term, Facet: s.Facet, Value: s.Value, Op: "add"})
	}
	return sounds.BuildIndex(1, rows)
}

func has(p sounds.Parsed, facet, value string) bool {
	for _, m := range p.Matches {
		if m.Facet == facet && m.Value == value {
			return true
		}
	}
	return false
}

func TestParse_Table(t *testing.T) {
	t.Parallel()
	ix := seedIndex()
	type row struct {
		name, style string
		must        [][2]string // facet, value
		not         [][2]string
		unmapped    []string
		bpm         int
		lang        string
	}
	rows := []row{
		{name: "basic", style: "reggae, male vocal, warm, 76bpm", must: [][2]string{{"genre_family", "reggae"}, {"vocal", "male"}, {"mood", "warm"}}, bpm: 76},
		{name: "longest phrase", style: "laid-back roots reggae groove", must: [][2]string{{"genre_family", "reggae"}}},
		{name: "hip-hop hyphen", style: "Hip-Hop", must: [][2]string{{"genre_family", "hip-hop"}}},
		{name: "hip hop space", style: "hip  hop", must: [][2]string{{"genre_family", "hip-hop"}}},
		{name: "hip hop upper", style: "HIP HOP", must: [][2]string{{"genre_family", "hip-hop"}}},
		{name: "female vocals", style: "female vocals", must: [][2]string{{"vocal", "female"}}},
		{name: "female vocal", style: "female vocal", must: [][2]string{{"vocal", "female"}}},
		{name: "synthpop twice", style: "synthpop", must: [][2]string{{"genre_family", "pop"}, {"genre_family", "electronic"}}},
		{name: "two phrases in a part", style: "90s eurodance", must: [][2]string{{"era", "90s"}, {"genre_family", "dance"}}},
		{name: "whole words only", style: "popular", not: [][2]string{{"genre_family", "pop"}}, unmapped: []string{"popular"}},
		{name: "italian", style: "italian", must: [][2]string{{"language", "it"}}, lang: "it"},
		{name: "jazz bar", style: "smoky late-night jazz bar ambience", must: [][2]string{{"genre_family", "jazz"}}},
		{name: "long unknown part is not unmapped", style: strings.Repeat("x", 59) + "y"},
		{name: "short unknown part", style: "zzzz blorp", unmapped: []string{"zzzz blorp"}},
		{name: "bpm skipped", style: "120 bpm", bpm: 120},
		{name: "bpm form 2", style: "bpm 95, jazz", must: [][2]string{{"genre_family", "jazz"}}, bpm: 95},
		{name: "semicolon and newline", style: "jazz; blues\nreggae|funk", must: [][2]string{{"genre_family", "jazz"}, {"genre_family", "blues"}, {"genre_family", "reggae"}, {"genre_family", "funk-soul"}}},
		{name: "empty", style: ""},
		{name: "dedupe", style: "jazz, jazz, Jazz", must: [][2]string{{"genre_family", "jazz"}}},
		{name: "rap is two facets", style: "rap", must: [][2]string{{"vocal", "rap"}, {"genre_family", "hip-hop"}}},
		{name: "digits only not unmapped", style: "1234"},
		{name: "duplicate unmapped once", style: "blorpish, blorpish", unmapped: []string{"blorpish"}},
		{name: "five-word phrase window", style: "alpha beta gamma delta epsilon zeta jazz", must: [][2]string{{"genre_family", "jazz"}}},
		{name: "accents folded", style: "Español", must: [][2]string{{"language", "es"}}, lang: "es"},
	}
	if len(rows) != 24 {
		t.Fatalf("table has %d rows, want 24", len(rows))
	}
	for _, r := range rows {
		p := ix2parse(ix, r.style)
		for _, m := range r.must {
			if !has(p, m[0], m[1]) {
				t.Errorf("%s: missing %v in %+v", r.name, m, p.Matches)
			}
		}
		for _, m := range r.not {
			if has(p, m[0], m[1]) {
				t.Errorf("%s: unexpected %v", r.name, m)
			}
		}
		if strings.Join(p.Unmapped, "|") != strings.Join(r.unmapped, "|") {
			t.Errorf("%s: unmapped %v, want %v", r.name, p.Unmapped, r.unmapped)
		}
		if p.BPM != r.bpm || p.Language != r.lang {
			t.Errorf("%s: bpm %d lang %q, want %d %q", r.name, p.BPM, p.Language, r.bpm, r.lang)
		}
	}
	// "synthpop" is matched once per mapping, "jazz, jazz, Jazz" once in total
	if n := len(sounds.Parse(ix, "jazz, jazz, Jazz").Matches); n != 1 {
		t.Errorf("dedupe: %d matches", n)
	}
	// the matched phrase of the longest rule is the whole `roots reggae`
	for _, m := range sounds.Parse(ix, "laid-back roots reggae groove").Matches {
		if m.Facet == "genre_family" && m.Term != "roots reggae" {
			t.Errorf("longest match: term %q", m.Term)
		}
	}
	// a 5-word window: a 6-word phrase in the index would never match, a 5-word one does
	five := sounds.BuildIndex(1, []sounds.TermRow{
		{Version: 1, Term: "a b c d e", Facet: "mood", Value: "dark", Op: "add"},
		{Version: 1, Term: "a b c d e f", Facet: "mood", Value: "warm", Op: "add"}})
	p := sounds.Parse(five, "a b c d e f")
	if len(p.Matches) != 1 || p.Matches[0].Value != "dark" {
		t.Errorf("phrases longer than 5 words must be cut: %+v", p.Matches)
	}
}

func ix2parse(ix *sounds.Index, s string) sounds.Parsed { return sounds.Parse(ix, s) }

func TestParse_CaseAndSpace(t *testing.T) {
	t.Parallel()
	ix := seedIndex()
	a, b, c := sounds.Parse(ix, "Hip-Hop"), sounds.Parse(ix, "hip  hop"), sounds.Parse(ix, "HIP HOP")
	if len(a.Matches) == 0 || len(a.Matches) != len(b.Matches) || len(b.Matches) != len(c.Matches) {
		t.Fatalf("%v %v %v", a.Matches, b.Matches, c.Matches)
	}
	for i := range a.Matches {
		if a.Matches[i] != b.Matches[i] || b.Matches[i] != c.Matches[i] {
			t.Errorf("match %d differs", i)
		}
	}
}

// TestParse_BPMSkipMatchesStyleTerms pins the local BPM matcher to yue2.StyleTerms: a part that StyleTerms treats as a
// BPM term (it is not returned as a term) is skipped by Parse, and nothing else is.
func TestParse_BPMSkipMatchesStyleTerms(t *testing.T) {
	t.Parallel()
	ix := sounds.BuildIndex(1, nil) // empty index: every non-skipped part becomes an unmapped term (when short)
	for _, part := range []string{
		"76bpm", "76 bpm", "bpm 76", "BPM 76", "120BPM", "120 BPM", "bpm120", "40bpm", "250bpm", "300bpm", "30bpm", "999 bpm",
		"bpm 5", "bpm 1000", "  90  bpm ", "90  bpm", "7bpm", "1000bpm", "76-bpm", "76 beats", "bpm", "76", "fast bpm", "bpm fast",
		"jazz", "76bpm jazz", "x 76bpm", "bpm 76 bpm", "100 bpms", "slow",
	} {
		terms, _ := yue2.StyleTerms(part)
		skippedByYue := len(terms) == 0
		got := sounds.Parse(ix, part)
		skippedByParse := len(got.Unmapped) == 0 && len(got.Matches) == 0
		// digits-only parts are not unmapped either, but "76" is not a BPM term: StyleTerms returns it
		if part == "76" {
			if skippedByYue {
				t.Errorf("premise: StyleTerms returns %q as a term", part)
			}
			continue
		}
		if skippedByYue != skippedByParse {
			t.Errorf("%q: StyleTerms skips=%v, Parse skips=%v (%+v)", part, skippedByYue, skippedByParse, got)
		}
	}
}
