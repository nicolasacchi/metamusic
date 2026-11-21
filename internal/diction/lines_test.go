package diction_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/diction"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb/tierbtest"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// pure builds the lines of lyrics heard through o without a database.
func pure(t testing.TB, lyrics string, o tierbtest.Opts, marks map[diction.LineKey]diction.DictionMark) (string, []diction.DictionLine) {
	t.Helper()
	raw, err := tierb.Parse(tierbtest.FromLyrics(lyrics, o))
	if err != nil {
		t.Fatal(err)
	}
	res := tierb.Diction(tierb.RefWords(lyrics), raw)
	var results []tierb.LineResult
	for _, s := range res.Sections {
		results = append(results, s.Lines...)
	}
	if results == nil {
		results = []tierb.LineResult{}
	}
	return diction.BuildLines(diction.LinesInput{Sections: yue2.LyricsSections(lyrics), Ref: tierb.RefWords(lyrics), Results: results, Marks: marks})
}

const twoLines = "[Verse]\nla chiave del mare\nsotto il cielo"

// TestLines_Example: the C334.4 phrases. "la kiave del male" is close on chiave (sim 0.667) and mare (0.75); "sotto cielo" drops
// il, which sits between two heard words and so gets an interpolated time.
func TestLines_Example(t *testing.T) {
	t.Parallel()
	basis, lines := pure(t, twoLines, tierbtest.Opts{Subs: map[string]string{"chiave": "kiave", "mare": "male"}, Drops: map[string]bool{"il": true}}, nil)
	if basis != diction.BasisASR || len(lines) != 2 {
		t.Fatalf("basis %s, %d lines", basis, len(lines))
	}
	l0 := lines[0]
	if l0.Text != "la chiave del mare" || l0.Class != diction.ClassMixed || l0.ClassSource != "asr" || l0.Timing == nil || *l0.Timing != "heard" {
		t.Errorf("line 0 = %+v", l0)
	}
	if l0.CER == nil || *l0.CER != 0.167 {
		t.Errorf("cer = %v, want 0.167", l0.CER)
	}
	got := []string{}
	for _, w := range l0.Words {
		got = append(got, fmt.Sprintf("%s:%s:%s", w.Text, w.Match, w.Heard))
	}
	if want := "la:exact: chiave:close:kiave del:exact: mare:close:male"; strings.Join(got, " ") != want {
		t.Errorf("words = %q, want %q", strings.Join(got, " "), want)
	}
	il := lines[1].Words[1]
	if il.Text != "il" || il.Match != "dropped" || il.Timing == nil || *il.Timing != "interpolated" || il.StartS == nil || *il.StartS >= *il.EndS {
		t.Errorf("il = %+v", il)
	}
	if il.StartS != nil && (*il.StartS < *lines[1].Words[0].EndS || *il.EndS > *lines[1].Words[2].StartS) {
		t.Errorf("il [%v, %v] is outside its neighbours", *il.StartS, *il.EndS)
	}
}

// TestLines_InterpolatedDropped: a run of dropped words splits the gap by syllables; edges and other sections stay untimed.
func TestLines_InterpolatedDropped(t *testing.T) {
	t.Parallel()
	// "sole" (2 syllables) and "mare" (2) dropped between "la" and "luna"; the first word of the section dropped has no left
	// neighbour and the whole second section is unheard
	lyrics := "[Verse]\nnotte la sole mare luna\n[Chorus]\nsotto il cielo"
	_, lines := pure(t, lyrics, tierbtest.Opts{Drops: map[string]bool{"notte": true, "sole": true, "mare": true}, DropSections: map[int]bool{1: true}}, nil)
	w := lines[0].Words
	if w[0].Timing != nil || w[0].Match != "dropped" {
		t.Errorf("a dropped first word has no left neighbour: %+v", w[0])
	}
	s, m := w[2], w[3]
	if s.Timing == nil || *s.Timing != "interpolated" || m.Timing == nil {
		t.Fatalf("sole %+v mare %+v", s, m)
	}
	if *s.EndS != *m.StartS || *s.StartS != *w[1].EndS || *m.EndS != *w[4].StartS {
		t.Errorf("the two words must tile the gap: la.end %v sole [%v,%v] mare [%v,%v] luna.start %v", *w[1].EndS, *s.StartS, *s.EndS, *m.StartS, *m.EndS, *w[4].StartS)
	}
	if d1, d2 := *s.EndS-*s.StartS, *m.EndS-*m.StartS; d1-d2 > 0.011 || d2-d1 > 0.011 {
		t.Errorf("two syllables each, so equal shares: %v vs %v", d1, d2)
	}
	for _, w := range lines[1].Words {
		if w.Timing != nil || w.StartS != nil {
			t.Errorf("an unheard section is untimed: %+v", w)
		}
	}
	if lines[1].Timing != nil || lines[1].StartS != nil {
		t.Errorf("an unheard line is untimed: %+v", lines[1])
	}
}

// TestLines_HumanMarkOverridesClass: clear and unclear replace the shown class; none withdraws; a critic mark never does.
func TestLines_HumanMarkOverridesClass(t *testing.T) {
	t.Parallel()
	opts := tierbtest.Opts{Subs: map[string]string{"chiave": "kiave", "mare": "male"}}
	cases := []struct {
		name    string
		mark    diction.DictionMark
		class   string
		source  string
		wantCER bool
	}{
		{"no mark", diction.DictionMark{}, "mixed", "asr", true},
		{"human clear", diction.DictionMark{Human: ptr("clear")}, "clear", "human", true},
		{"human unclear", diction.DictionMark{Human: ptr("unclear")}, "unclear", "human", true},
		{"human none", diction.DictionMark{Human: ptr("none")}, "mixed", "asr", true},
		{"critic clear", diction.DictionMark{Critic: ptr("clear")}, "mixed", "asr", true},
	}
	for _, c := range cases {
		_, lines := pure(t, twoLines, opts, map[diction.LineKey]diction.DictionMark{{Section: 0, Line: 0}: c.mark})
		if l := lines[0]; l.Class != c.class || l.ClassSource != c.source || (l.CER != nil) != c.wantCER || l.Mark != c.mark {
			t.Errorf("%s: class %s from %s, mark %+v", c.name, l.Class, l.ClassSource, l.Mark)
		}
	}
	// the recogniser classes by CER
	_, lines := pure(t, twoLines, tierbtest.Opts{}, nil)
	if lines[0].Class != "clear" {
		t.Errorf("a faithful line is clear, got %s", lines[0].Class)
	}
	_, lines = pure(t, twoLines, tierbtest.Opts{Subs: map[string]string{"chiave": "xyzxyz", "mare": "qqqq", "sotto": "zzzz"}}, nil)
	if lines[0].Class != "unclear" {
		t.Errorf("a wrecked line is unclear, got %s", lines[0].Class)
	}
}

// TestLines_ProportionalHasNoWordTimes: without a tier_b_diction row, a timed section splits equally among its lines, every time is
// an estimate and no word has one; a take with no timing at all has basis none.
func TestLines_ProportionalHasNoWordTimes(t *testing.T) {
	t.Parallel()
	lyrics := "[Verse]\nuno due\ntre quattro\n[Outro]\ncinque"
	in := diction.LinesInput{Sections: yue2.LyricsSections(lyrics), Ref: tierb.RefWords(lyrics),
		Timeline: []diction.TimedSection{{Idx: 0, StartS: 10, EndS: 20}}}
	basis, lines := diction.BuildLines(in)
	if basis != diction.BasisProportional || len(lines) != 3 {
		t.Fatalf("basis %s, %d lines", basis, len(lines))
	}
	if *lines[0].StartS != 10 || *lines[0].EndS != 15 || *lines[1].StartS != 15 || *lines[1].EndS != 20 || *lines[0].Timing != "estimated" {
		t.Errorf("lines = %+v %+v", lines[0], lines[1])
	}
	if lines[2].Timing != nil || lines[2].StartS != nil {
		t.Errorf("a section outside the timeline is untimed: %+v", lines[2])
	}
	for _, l := range lines {
		if len(l.Words) != 0 || l.Class != "unknown" || l.CER != nil {
			t.Errorf("proportional line carries words or a class: %+v", l)
		}
	}
	if basis, lines = diction.BuildLines(diction.LinesInput{Sections: in.Sections, Ref: in.Ref}); basis != diction.BasisNone || lines != nil {
		t.Errorf("no timing and no row is basis none, got %s", basis)
	}
}

// FuzzLines: for random lyrics and recognition errors, there is one line per lyric line, words are in order, and no word time is
// negative or backwards.
func FuzzLines(f *testing.F) {
	f.Add(int64(1), 4)
	f.Add(int64(7), 9)
	f.Fuzz(func(t *testing.T, seed int64, n int) {
		rnd := rand.New(rand.NewSource(seed)) // #nosec G404 -- deterministic test data
		words := []string{"sole", "mare", "chiave", "il", "la", "notte", "cielo", "amore", "di", "luna"}
		var b strings.Builder
		subs, drops := map[string]string{}, map[string]bool{}
		for _, w := range words {
			switch rnd.Intn(4) {
			case 0:
				subs[w] = w[:max(1, len(w)-1)] + "x"
			case 1:
				drops[w] = true
			}
		}
		lines := 0
		for s := 0; s < 1+abs(n)%4; s++ {
			fmt.Fprintf(&b, "[Verse %d]\n", s+1)
			for l := 0; l < 1+rnd.Intn(3); l++ {
				k := 1 + rnd.Intn(5)
				var ws []string
				for range k {
					ws = append(ws, words[rnd.Intn(len(words))])
				}
				b.WriteString(strings.Join(ws, " ") + "\n")
				lines++
			}
		}
		_, got := pure(t, b.String(), tierbtest.Opts{Subs: subs, Drops: drops}, nil)
		if len(got) != lines {
			t.Fatalf("%d lines, want %d", len(got), lines)
		}
		for _, l := range got {
			prev := -1.0
			for i, w := range l.Words {
				if w.I != i {
					t.Fatalf("word index %d at %d", w.I, i)
				}
				if w.StartS != nil {
					if *w.StartS < 0 || *w.EndS < *w.StartS {
						t.Fatalf("bad word time %+v", w)
					}
					if w.Timing != nil && *w.Timing == "interpolated" && *w.StartS < prev-0.011 {
						t.Fatalf("interpolated word goes backwards: %+v after %v", w, prev)
					}
					prev = *w.EndS
				}
			}
		}
	})
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// TestTrackDiction_PagedByBytes: a lyric of about a megabyte pages in chunks of at most 60 000 bytes; the pages concatenate to
// every line once, in order, and only the first carries the lexicon hits.
func TestTrackDiction_PagedByBytes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var b strings.Builder
	b.WriteString("[Verse]\n")
	const total = 14000
	for i := range total {
		fmt.Fprintf(&b, "riga numero %d del mare sotto il cielo di notte\n", i)
	}
	if b.Len() < 600_000 {
		t.Fatalf("synthetic lyric is only %d bytes", b.Len())
	}
	id := e.addTake(b.String())
	e.addTimed(id, "plan", 0, 5, 3000)
	cursor, seen, pages := "", 0, 0
	for {
		page, err := e.svc.Lines(context.Background(), id, cursor)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(page)
		if len(raw) > diction.PageBytes {
			t.Fatalf("page %d is %d bytes", pages, len(raw))
		}
		for _, l := range page.Lines {
			if l.LineIdx != seen {
				t.Fatalf("line %d where %d expected", l.LineIdx, seen)
			}
			seen++
		}
		if pages > 0 && len(page.LexiconHits) != 0 {
			t.Errorf("hits on page %d", pages)
		}
		pages++
		if (page.NextCursor == nil) == page.Truncated {
			t.Fatalf("truncated %v with cursor %v", page.Truncated, page.NextCursor)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if seen != total || pages < 10 {
		t.Errorf("%d lines in %d pages", seen, pages)
	}
	for _, bad := range []string{"%%%", "bDoxMA", "bm9wZTox"} { // not base64; the lexicon kind; garbage
		if _, err := e.svc.Lines(context.Background(), id, bad); err == nil {
			t.Errorf("cursor %q accepted", bad)
		}
	}
	if _, err := e.svc.Lines(context.Background(), id, "ZDo5OTk5OTk"); err == nil { // d:999999
		t.Error("a cursor past the end was accepted")
	}
}

// TestTrackDiction_ProportionalBasis: through the service, a take with only plan timing has basis proportional and estimated times; a
// take with nothing is 409 ANALYSIS_UNAVAILABLE; a take with a row is basis asr, with the lexicon hits it earned.
func TestTrackDiction_ProportionalBasis(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	id := e.addTake("[Verse]\nuno due\ntre quattro")
	_, err := e.svc.Lines(ctx, id, "")
	var pe *problem.Error
	if !errors.As(err, &pe) || pe.Status != 409 || pe.Code != "ANALYSIS_UNAVAILABLE" || pe.Details["analyzer"] != "tier_b" {
		t.Fatalf("a take with no timing: %v", err)
	}
	if _, err := e.svc.Lines(ctx, "no-such-take", ""); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("unknown take: %v", err)
	}
	e.addTimed(id, "plan", 0, 0, 8)
	page, err := e.svc.Lines(ctx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	if page.Basis != "proportional" || page.Lang != "it" && page.Lang != "und" || len(page.Lines) != 2 || *page.Lines[1].StartS != 4 || page.Thresholds.Clear != 0.15 || page.Thresholds.Mixed != 0.35 {
		t.Errorf("page = %+v", page)
	}

	e.sixTakes()
	e.advance(6e9)
	own := e.addTake(chiave)
	e.addDiction(own, chiave, tierbtest.Opts{})
	page, err = e.svc.Lines(ctx, own, "")
	if err != nil {
		t.Fatal(err)
	}
	if page.Basis != "asr" || page.Lang != "it" || len(page.LexiconHits) != 2 || page.LexiconHits[0].Word != "chiave" || page.LexiconHits[0].Mangled != 4 {
		t.Errorf("page = %+v", page)
	}
}

// TestGETNeverWritesDB_Diction: reading lines and the lexicon changes no row.
func TestGETNeverWritesDB_Diction(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ids := e.sixTakes()
	count := func() string {
		var s string
		if err := e.st.R.QueryRow(`SELECT (SELECT count(*) FROM analyses) || '/' || (SELECT count(*) FROM diction_marks) || '/' || (SELECT count(*) FROM lexicon_overrides) ||
			'/' || (SELECT count(*) FROM sections) || '/' || (SELECT count(*) FROM tracks)`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := count()
	if _, err := e.svc.Lines(context.Background(), ids[0], ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Lexicon(context.Background(), diction.LexiconQuery{}); err != nil {
		t.Fatal(err)
	}
	if after := count(); after != before {
		t.Errorf("rows changed: %s -> %s", before, after)
	}
}

// TestLines_UnmeasuredLineHasNoWords: a line the recogniser never scored shows unknown with no word verdicts or invented times.
func TestLines_UnmeasuredLineHasNoWords(t *testing.T) {
	t.Parallel()
	lyrics := "[Verse]\nla chiave del mare\nsotto il cielo\nla la luna"
	raw, err := tierb.Parse(tierbtest.FromLyrics(lyrics, tierbtest.Opts{}))
	if err != nil {
		t.Fatal(err)
	}
	res := tierb.Diction(tierb.RefWords(lyrics), raw)
	var results []tierb.LineResult
	for i, l := range res.Sections[0].Lines {
		if i != 1 {
			results = append(results, l)
		}
	}
	_, lines := diction.BuildLines(diction.LinesInput{Sections: yue2.LyricsSections(lyrics), Ref: tierb.RefWords(lyrics), Results: results})
	if len(lines) != 3 || lines[1].Class != diction.ClassUnknown || len(lines[1].Words) != 0 || lines[1].StartS != nil {
		t.Errorf("line 1 = %+v", lines[1])
	}
	for _, w := range lines[2].Words {
		if w.Match == "dropped" {
			t.Errorf("heard line has a dropped word: %+v", w)
		}
	}
}
