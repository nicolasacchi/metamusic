package morning

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

var t0 = time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)

func cand(id, root, source string, age int, secs float64) Candidate {
	return Candidate{
		TrackID: id, RootID: root, DurationS: secs, TotalSamples: int64(secs * SampleRate),
		SampleRate: SampleRate, Channels: 2, Source: source,
		CreatedAt: t0.Add(-time.Duration(age) * time.Minute), LUFS: -12, TruePeak: -2,
	}
}

func distinct(n int, source string) []Candidate {
	var cs []Candidate
	for i := range n {
		cs = append(cs, cand(fmt.Sprintf("t%02d", i), fmt.Sprintf("r%02d", i), source, i, 240))
	}
	return cs
}

func sideSizes(p Plan) []int {
	var out []int
	for _, s := range p.Sides {
		out = append(out, len(s.Chapters))
	}
	return out
}

func chapters(p Plan) []Chapter {
	var out []Chapter
	for _, s := range p.Sides {
		out = append(out, s.Chapters...)
	}
	return out
}

func opts() Options { return DefaultOptions() }

func TestSelect_FitsMinutes(t *testing.T) {
	o := opts()
	p := Select(Input{Candidates: distinct(10, SourceJob)}, o, 7)
	var total int64
	for _, s := range p.Sides {
		total += s.Samples
	}
	if n := len(chapters(p)); n != 9 {
		t.Fatalf("chapters = %d, want 9", n)
	}
	if got := float64(total) / SampleRate; got != 2173.5 {
		t.Fatalf("seconds = %v, want 2173.5", got)
	}
	if diff := cmp.Diff([]int{3, 3, 3}, sideSizes(p)); diff != "" {
		t.Fatal(diff)
	}

	o.Anchors = 1
	anchor := cand("a1", "ra", SourceManual, 0, 240)
	p = Select(Input{Candidates: distinct(10, SourceJob), Anchors5: []Candidate{anchor}}, o, 7)
	cs := chapters(p)
	if len(cs) != 9 {
		t.Fatalf("with anchor chapters = %d, want 9", len(cs))
	}
	na := 0
	for _, c := range cs {
		if c.Anchor {
			na++
		}
	}
	if na != 1 {
		t.Fatalf("anchors = %d", na)
	}
	if diff := cmp.Diff([]int{3, 3, 3}, sideSizes(p)); diff != "" {
		t.Fatal(diff)
	}
}

func TestSelect_AnchorsReservedFirst(t *testing.T) {
	o := opts()
	o.Anchors = 1
	o.Minutes = 8 // fits exactly one 240 s take plus slate... 2 takes = 483 s > 480 s
	cands := distinct(5, SourceManual)
	anchor := cand("a1", "ra", SourceJob, 0, 240)
	p := Select(Input{Candidates: cands, Anchors5: []Candidate{anchor}}, o, 3)
	cs := chapters(p)
	if len(cs) != 1 || !cs[0].Anchor || cs[0].TrackID != "a1" {
		t.Fatalf("anchor must take the reserved time: %+v", cs)
	}
}

func TestSelect_AnchorPrefersRootsOutsideCandidates(t *testing.T) {
	o := opts()
	o.Anchors = 1
	cands := distinct(3, SourceJob)
	in := Input{
		Candidates: cands,
		Anchors5:   []Candidate{cand("x", "r00", SourceJob, 0, 200), cand("y", "rfresh", SourceJob, 0, 200)},
	}
	for seed := uint64(1); seed <= 20; seed++ {
		p := Select(in, o, seed)
		for _, c := range chapters(p) {
			if c.Anchor && c.TrackID != "y" {
				t.Fatalf("seed %d: anchor %s, want y", seed, c.TrackID)
			}
		}
	}
	// Five-star pool first, final pool only fills what is left.
	in.AnchorsFinal = []Candidate{cand("z", "rz", SourceJob, 0, 200)}
	p := Select(in, o, 1)
	for _, c := range chapters(p) {
		if c.TrackID == "z" {
			t.Fatal("final-status anchor used although a 5-star one filled the slot")
		}
	}
}

func TestSelect_SiblingsApart(t *testing.T) {
	var cs []Candidate
	for i, root := range []string{"a", "a", "a", "a", "b", "b", "c", "c"} {
		cs = append(cs, cand(fmt.Sprintf("t%d", i), root, SourceJob, i, 100))
	}
	for seed := uint64(1); seed <= 100; seed++ {
		p := Select(Input{Candidates: cs}, opts(), seed)
		ch := chapters(p)
		if len(ch) != 8 || p.AdjacencyRelaxed {
			t.Fatalf("seed %d: %d chapters relaxed=%v", seed, len(ch), p.AdjacencyRelaxed)
		}
		root := map[string]string{}
		for _, c := range cs {
			root[c.TrackID] = c.RootID
		}
		for i := 1; i < len(ch); i++ {
			if root[ch[i].TrackID] == root[ch[i-1].TrackID] {
				t.Fatalf("seed %d: adjacent siblings at %d", seed, i)
			}
		}
	}
}

func TestSelect_AnchorNeverFirst(t *testing.T) {
	o := opts()
	o.Anchors = 2
	in := Input{
		Candidates: distinct(4, SourceJob),
		Anchors5:   []Candidate{cand("a1", "ra", SourceJob, 0, 100), cand("a2", "rb", SourceJob, 0, 100)},
	}
	for seed := uint64(1); seed <= 100; seed++ {
		ch := chapters(Select(in, o, seed))
		if len(ch) != 6 || ch[0].Anchor {
			t.Fatalf("seed %d: first=%+v n=%d", seed, ch[0], len(ch))
		}
	}
}

func TestSelect_AdjacencyRelaxed(t *testing.T) {
	cs := []Candidate{
		cand("t0", "a", SourceJob, 0, 100), cand("t1", "a", SourceJob, 1, 100),
		cand("t2", "a", SourceJob, 2, 100), cand("t3", "b", SourceJob, 3, 100),
	}
	p := Select(Input{Candidates: cs}, opts(), 5)
	if !p.AdjacencyRelaxed || len(chapters(p)) != 4 {
		t.Fatalf("relaxed=%v n=%d", p.AdjacencyRelaxed, len(chapters(p)))
	}
}

func TestSelect_Deterministic(t *testing.T) {
	in := Input{Candidates: distinct(12, SourceJob), Anchors5: []Candidate{cand("a1", "ra", SourceJob, 0, 200)}}
	a, b := Select(in, opts(), 99), Select(in, opts(), 99)
	if diff := cmp.Diff(a, b); diff != "" {
		t.Fatal(diff)
	}
	if diff := cmp.Diff(a, Select(in, opts(), 100)); diff == "" {
		t.Fatal("different seeds gave the same plan")
	}
}

func TestSelect_SourceOrder(t *testing.T) {
	o := opts()
	o.MaxTakes = 3
	cs := []Candidate{
		cand("job-new", "r1", SourceJob, 1, 100),
		cand("night-old", "r2", SourceNightShift, 50, 100),
		cand("night-new", "r3", SourceNightShift, 5, 100),
		cand("manual", "r4", SourceManual, 500, 100),
	}
	p := Select(Input{Candidates: cs}, o, 1)
	got := map[string]bool{}
	for _, c := range chapters(p) {
		got[c.TrackID] = true
	}
	want := map[string]bool{"manual": true, "night-new": true, "night-old": true}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatal(diff)
	}
	if diff := cmp.Diff([]Excluded{{"job-new", ExcludeOverBudget}}, p.Excluded); diff != "" {
		t.Fatal(diff)
	}
}

func TestSelect_Excluded(t *testing.T) {
	trunc := cand("trunc", "r1", SourceJob, 0, 100)
	trunc.Truncated = true
	quiet := cand("noloud", "r2", SourceJob, 0, 100)
	quiet.LUFS, quiet.TruePeak = 0, 0
	nan := cand("nan", "r3", SourceJob, 0, 100)
	nan.LUFS = math.NaN()
	multi := cand("multi", "r4", SourceJob, 0, 100)
	multi.Channels = 6
	long := cand("long", "r5", SourceJob, 0, 1000) // longer than one 15-minute side
	ok := cand("ok", "r6", SourceJob, 0, 100)
	in := Input{Candidates: []Candidate{trunc, quiet, nan, multi, long, ok}}

	p := Select(in, opts(), 1)
	want := []Excluded{
		// candidates are visited in (source, newest, track id) order
		{"long", ExcludeOverBudget}, {"multi", ExcludeChannels}, {"nan", ExcludeNoLoudness},
		{"noloud", ExcludeNoLoudness}, {"trunc", ExcludeTruncated},
	}
	if diff := cmp.Diff(want, p.Excluded); diff != "" {
		t.Fatal(diff)
	}
	if ch := chapters(p); len(ch) != 1 || ch[0].TrackID != "ok" {
		t.Fatalf("chapters = %+v", ch)
	}

	o := opts()
	o.IncludeTruncated = true
	if n := len(chapters(Select(Input{Candidates: []Candidate{trunc}}, o, 1))); n != 1 {
		t.Fatalf("IncludeTruncated: %d chapters", n)
	}
}

func TestSelect_Empty(t *testing.T) {
	p := Select(Input{}, opts(), 1)
	if len(p.Sides) != 0 || len(p.Excluded) != 0 {
		t.Fatalf("%+v", p)
	}
}

func TestSelect_ChapterGeometry(t *testing.T) {
	p := Select(Input{Candidates: distinct(3, SourceJob)}, opts(), 2)
	if len(p.Sides) != 1 {
		t.Fatalf("sides = %d", len(p.Sides))
	}
	var pos int64
	for i, c := range p.Sides[0].Chapters {
		audio := int64(240 * SampleRate)
		if c.N != i+1 || c.Start != pos || c.AudioStart != pos+72000 || c.End != c.AudioStart+audio {
			t.Fatalf("chapter %d = %+v (pos %d)", i, c, pos)
		}
		if c.GainCDB != -400 && c.GainCDB != 400 && c.GainCDB != -100 {
			// -12 LUFS, -2 dBTP: min(-4, 1) = -4 dB, not limited
			t.Fatalf("gain = %d", c.GainCDB)
		}
		pos = c.End
	}
	if p.Sides[0].Samples != pos {
		t.Fatalf("side samples %d != %d", p.Sides[0].Samples, pos)
	}
}

func TestSelect_ByteCapSplitsSides(t *testing.T) {
	o := opts()
	o.MaxSideBytes = 150 << 20 // ~ 2 takes of 240 s (each ~ 43 MB estimated... 3 takes = 129 MB; 4 = 172 MB)
	o.SideMinutes = 60
	o.Minutes = 60
	p := Select(Input{Candidates: distinct(10, SourceJob)}, o, 1)
	for _, s := range p.Sides {
		if float64(s.Samples)*6*SideByteRatio > float64(o.MaxSideBytes) {
			t.Fatalf("side %d over the byte cap", s.Index)
		}
	}
	if len(p.Sides) < 2 {
		t.Fatalf("expected a split, sides = %v", sideSizes(p))
	}
}

func TestSelect_ResamplesLength(t *testing.T) {
	c := cand("t", "r", SourceJob, 0, 10)
	c.SampleRate, c.TotalSamples = 44100, 441000
	ch := chapters(Select(Input{Candidates: []Candidate{c}}, opts(), 1))
	if got := ch[0].End - ch[0].AudioStart; got != 480000 {
		t.Fatalf("audio frames = %d, want 480000", got)
	}
}

func TestChapterAt(t *testing.T) {
	s := Side{Chapters: []Chapter{
		{N: 1, Start: 0, AudioStart: 72000, End: 1000000},
		{N: 2, Start: 1000000, AudioStart: 1072000, End: 2000000},
	}}
	for _, tc := range []struct {
		frame int64
		n     int
		ok    bool
	}{{0, 1, true}, {999999, 1, true}, {1000000, 2, true}, {1999999, 2, true}, {2000000, 0, false}, {-1, 0, false}} {
		c, ok := ChapterAt(s, tc.frame)
		if ok != tc.ok || c.N != tc.n {
			t.Errorf("ChapterAt(%d) = %d,%v want %d,%v", tc.frame, c.N, ok, tc.n, tc.ok)
		}
	}
}

func TestSelect_BudgetUsesFrames(t *testing.T) {
	c := cand("a", "r1", SourceJob, 0, 0)
	c.DurationS = 0
	c.TotalSamples = 600 * SampleRate // 10 min, but DurationS says 0
	d := cand("b", "r2", SourceJob, 1, 0)
	d.DurationS = 0
	d.TotalSamples = 600 * SampleRate
	o := opts()
	o.Minutes = 15
	p := Select(Input{Candidates: []Candidate{c, d}}, o, 1)
	if n := len(chapters(p)); n != 1 {
		t.Fatalf("chapters = %d, want 1 (budget must use frames)", n)
	}
	if len(p.Excluded) != 1 || p.Excluded[0].Reason != ExcludeOverBudget {
		t.Fatalf("excluded = %+v", p.Excluded)
	}
}

func TestSelect_ExcludedOncePerTrack(t *testing.T) {
	bad := cand("bad", "r1", SourceJob, 0, 100)
	bad.Truncated = true
	ok := cand("ok", "r2", SourceJob, 0, 100)
	in := Input{Candidates: []Candidate{bad, ok}, Anchors5: []Candidate{bad}, AnchorsFinal: []Candidate{bad}}
	p := Select(in, opts(), 1)
	if diff := cmp.Diff([]Excluded{{"bad", ExcludeTruncated}}, p.Excluded); diff != "" {
		t.Fatal(diff)
	}
}

func TestSelect_EmptyRootIsUnique(t *testing.T) {
	var cs []Candidate
	for i := range 4 {
		cs = append(cs, cand(fmt.Sprintf("t%d", i), "", SourceJob, i, 100))
	}
	for seed := uint64(1); seed <= 20; seed++ {
		if p := Select(Input{Candidates: cs}, opts(), seed); p.AdjacencyRelaxed {
			t.Fatalf("seed %d: empty roots treated as siblings", seed)
		}
	}
}
