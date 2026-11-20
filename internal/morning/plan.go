// Package morning is the Morning Crate: a blind, loudness-matched tape of
// unjudged takes. This file set (plan, gain, slate) is the pure core and
// imports only the standard library.
package morning

import (
	"cmp"
	"math"
	"math/rand/v2"
	"slices"
	"time"
)

// Candidate sources, in selection order.
const (
	SourceManual     = "manual"
	SourceNightShift = "night_shift"
	SourceJob        = "job"
)

// Exclusion reasons.
const (
	ExcludeOverBudget = "over_budget"
	ExcludeTruncated  = "truncated"
	ExcludeNoLoudness = "no_loudness"
	ExcludeChannels   = "channels"
	ExcludeNoAudio    = "no_audio"
)

// Options bound a crate.
type Options struct {
	Minutes          int
	SideMinutes      int
	MaxTakes         int
	Anchors          int
	SlateMS          int
	MaxSideBytes     int64
	IncludeTruncated bool
}

// DefaultOptions are the defaults of the crate settings.
func DefaultOptions() Options {
	return Options{Minutes: 40, SideMinutes: 15, MaxTakes: 14, Anchors: 1, SlateMS: 1500, MaxSideBytes: 200 << 20}
}

// SideByteRatio is the assumed FLAC size over raw 24-bit PCM size, used to
// keep a side under MaxSideBytes before it is built (measured and recorded
// by the side builder).
const SideByteRatio = 0.62

// Candidate is one take that may enter a crate. A track without a measured
// loudness has LUFS and TruePeak both zero (or NaN).
type Candidate struct {
	TrackID      string
	RootID       string
	MasterSHA    string
	MasterPath   string
	DurationS    float64
	TotalSamples int64
	SampleRate   int
	Channels     int
	Source       string
	CreatedAt    time.Time
	LUFS         float64
	TruePeak     float64
	Truncated    bool
}

// Chapter is one take placed on a side. Positions are frames at SampleRate
// relative to the start of the side: the slate occupies [Start, AudioStart),
// the audio [AudioStart, End).
type Chapter struct {
	N          int
	TrackID    string
	Anchor     bool
	Start      int64
	AudioStart int64
	End        int64
	GainCDB    int
	Limited    bool
}

// Side is one file: consecutive chapters.
type Side struct {
	Index    int
	Chapters []Chapter
	Samples  int64
}

// Excluded names a candidate that did not make the crate and why.
type Excluded struct {
	TrackID string
	Reason  string
}

// Plan is the outcome of Select.
type Plan struct {
	Seed             uint64
	Sides            []Side
	Excluded         []Excluded
	AdjacencyRelaxed bool
}

// Input is everything Select reads. Anchors5 holds the Owner's five-star
// takes, AnchorsFinal the takes with status final; the first fills the anchor
// slots, the second only what is left.
type Input struct {
	Candidates   []Candidate
	Anchors5     []Candidate
	AnchorsFinal []Candidate
}

type picked struct {
	c      Candidate
	anchor bool
	audio  int64
}

// Select chooses, orders and divides takes into sides. It is deterministic in
// (in, opts, seed).
func Select(in Input, opts Options, seed uint64) Plan {
	plan := Plan{Seed: seed}
	slate := int64(opts.SlateMS) * SampleRate / 1000
	budget := int64(opts.Minutes) * 60 * SampleRate
	sideCap := int64(opts.SideMinutes) * 60 * SampleRate

	usable := func(c Candidate) (int64, string) {
		switch {
		case c.Truncated && !opts.IncludeTruncated:
			return 0, ExcludeTruncated
		case noLoudness(c):
			return 0, ExcludeNoLoudness
		case c.Channels > 2:
			return 0, ExcludeChannels
		}
		n := audioSamples(c)
		if n <= 0 {
			return 0, ExcludeNoAudio
		}
		total := slate + n
		if total > sideCap || float64(total)*6*SideByteRatio > float64(opts.MaxSideBytes) {
			return 0, ExcludeOverBudget
		}
		return n, ""
	}

	candRoots := map[string]bool{}
	for _, c := range in.Candidates {
		candRoots[rootKey(c)] = true
	}
	excluded := map[string]bool{}
	exclude := func(c Candidate, why string) {
		if excluded[c.TrackID] {
			return
		}
		excluded[c.TrackID] = true
		plan.Excluded = append(plan.Excluded, Excluded{c.TrackID, why})
	}

	var chosen []picked
	used := map[string]bool{}
	usedRoots := map[string]bool{}
	var elapsed int64 // frames, same measure as the side layout

	// Anchors first; their time is reserved before any candidate.
	anchorRNG := rand.New(rand.NewPCG(seed, 1))
	for _, pool := range [][]Candidate{in.Anchors5, in.AnchorsFinal} {
		if len(chosen) >= opts.Anchors {
			break
		}
		ordered := slices.Clone(pool)
		slices.SortStableFunc(ordered, func(a, b Candidate) int { return cmp.Compare(a.TrackID, b.TrackID) })
		anchorRNG.Shuffle(len(ordered), func(i, j int) { ordered[i], ordered[j] = ordered[j], ordered[i] })
		// Roots absent from the candidates first (stable keeps the shuffle).
		slices.SortStableFunc(ordered, func(a, b Candidate) int {
			return cmp.Compare(b2i(candRoots[rootKey(a)]), b2i(candRoots[rootKey(b)]))
		})
		for _, c := range ordered {
			if len(chosen) >= opts.Anchors || len(chosen) >= opts.MaxTakes {
				break
			}
			if used[c.TrackID] || excluded[c.TrackID] || usedRoots[rootKey(c)] {
				continue
			}
			n, why := usable(c)
			if why != "" {
				exclude(c, why)
				continue
			}
			if elapsed+slate+n > budget {
				exclude(c, ExcludeOverBudget)
				continue
			}
			chosen = append(chosen, picked{c, true, n})
			used[c.TrackID], usedRoots[rootKey(c)] = true, true
			elapsed += slate + n
		}
	}

	// Candidates in source order, newest first within a source.
	cands := slices.Clone(in.Candidates)
	slices.SortStableFunc(cands, func(a, b Candidate) int {
		if r := cmp.Compare(sourceRank(a.Source), sourceRank(b.Source)); r != 0 {
			return r
		}
		if r := b.CreatedAt.Compare(a.CreatedAt); r != 0 {
			return r
		}
		return cmp.Compare(a.TrackID, b.TrackID)
	})
	for _, c := range cands {
		if used[c.TrackID] || excluded[c.TrackID] {
			continue
		}
		n, why := usable(c)
		if why == "" && (len(chosen) >= opts.MaxTakes || elapsed+slate+n > budget) {
			why = ExcludeOverBudget
		}
		if why != "" {
			exclude(c, why)
			continue
		}
		chosen = append(chosen, picked{c, false, n})
		used[c.TrackID] = true
		elapsed += slate + n
	}

	order, relaxed := blindOrder(chosen, rand.New(rand.NewPCG(seed, 0)))
	plan.AdjacencyRelaxed = relaxed

	var side *Side
	var pos int64
	for i, p := range order {
		total := slate + p.audio
		if side == nil || pos+total > sideCap || float64(pos+total)*6*SideByteRatio > float64(opts.MaxSideBytes) {
			plan.Sides = append(plan.Sides, Side{Index: len(plan.Sides)})
			side = &plan.Sides[len(plan.Sides)-1]
			pos = 0
		}
		cdb, limited := Gain(p.c.LUFS, p.c.TruePeak)
		side.Chapters = append(side.Chapters, Chapter{
			N: i + 1, TrackID: p.c.TrackID, Anchor: p.anchor,
			Start: pos, AudioStart: pos + slate, End: pos + total,
			GainCDB: cdb, Limited: limited,
		})
		pos += total
		side.Samples = pos
		// side points into the slice; appending a new side re-points it.
	}
	return plan
}

// ChapterAt returns the chapter of s that contains frame (slate included).
func ChapterAt(s Side, frame int64) (Chapter, bool) {
	for _, c := range s.Chapters {
		if frame >= c.Start && frame < c.End {
			return c, true
		}
	}
	return Chapter{}, false
}

// blindOrder shuffles with the rules: an anchor is never first, and no two
// adjacent chapters share a root when that can be avoided. It tries seeded
// randomised greedy passes and keeps the first without a violation, else the
// one with the fewest.
func blindOrder(in []picked, rng *rand.Rand) (out []picked, relaxed bool) {
	if len(in) <= 1 {
		return slices.Clone(in), false
	}
	best, bestV := []picked(nil), math.MaxInt
	for range 400 {
		rest := slices.Clone(in)
		rng.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] })
		seq := make([]picked, 0, len(rest))
		v := 0
		for len(rest) > 0 {
			idx := -1
			for i, p := range rest { // first fully valid in shuffled order
				if len(seq) == 0 && p.anchor {
					continue
				}
				if len(seq) > 0 && rootKey(seq[len(seq)-1].c) == rootKey(p.c) {
					continue
				}
				idx = i
				break
			}
			if idx < 0 { // relax adjacency, keep anchor-not-first when possible
				for i, p := range rest {
					if len(seq) == 0 && p.anchor {
						continue
					}
					idx = i
					break
				}
				if idx < 0 {
					idx = 0
				}
				if len(seq) > 0 && rootKey(seq[len(seq)-1].c) == rootKey(rest[idx].c) {
					v++
				}
			}
			seq = append(seq, rest[idx])
			rest = slices.Delete(rest, idx, idx+1)
		}
		anchorFirst := seq[0].anchor
		if anchorFirst {
			v += len(in) // only when everything is an anchor
		}
		if v < bestV {
			best, bestV = seq, v
		}
		if v == 0 {
			break
		}
	}
	return best, bestV > 0 && hasAdjacent(best)
}

func hasAdjacent(s []picked) bool {
	for i := 1; i < len(s); i++ {
		if rootKey(s[i-1].c) == rootKey(s[i].c) {
			return true
		}
	}
	return false
}

// rootKey is the sibling identity of a take; a missing root is its own root.
func rootKey(c Candidate) string {
	if c.RootID == "" {
		return "track:" + c.TrackID
	}
	return c.RootID
}

func noLoudness(c Candidate) bool {
	bad := func(f float64) bool { return math.IsNaN(f) || math.IsInf(f, 0) }
	return bad(c.LUFS) || bad(c.TruePeak) || (c.LUFS == 0 && c.TruePeak == 0)
}

// audioSamples is the take's length in frames at SampleRate (the side format).
func audioSamples(c Candidate) int64 {
	if c.TotalSamples > 0 && c.SampleRate > 0 {
		if c.SampleRate == SampleRate {
			return c.TotalSamples
		}
		return int64(math.Round(float64(c.TotalSamples) * SampleRate / float64(c.SampleRate)))
	}
	return int64(math.Round(c.DurationS * SampleRate))
}

func sourceRank(s string) int {
	switch s {
	case SourceManual:
		return 0
	case SourceNightShift:
		return 1
	case SourceJob:
		return 2
	}
	return 3
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
