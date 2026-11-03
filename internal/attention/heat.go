// Package attention turns raw listen events into an attention heat strip and hotspots (C256, D-M5-30). Everything is
// computed on read; no table exists. ComputeHeat is pure: the caller selects the events (human and device principals
// only, never agents) and attaches the track id.
package attention

import (
	"sort"

	"github.com/nicolasacchi/metamusic/internal/anchor"
	"github.com/nicolasacchi/metamusic/internal/listening"
)

// Bins is the number of bins of a heat strip (C250).
const Bins = 120

const (
	maxBins       = 600
	rewindSlackMS = 1000 // a seek counts as a rewind when it lands more than this before its origin
	skipJumpMS    = 5000 // a forward seek counts as a skip when it jumps more than this
	hotspotMinH   = 2    // a bin belongs to a hotspot span when h >= this
	rewindWeight  = 3
	maxHotspots   = 3
	maxColdBins   = 3
	coverageNumer = 1 // a segment covers a bin when it overlaps at least coverageNumer/coverageDenom of it
	coverageDenom = 2
)

// Hotspot is a run of adjacent bins people rewound to or replayed.
type Hotspot struct {
	FromS float64 `json:"from_s"`
	ToS   float64 `json:"to_s"`
	Score int     `json:"score"`
	Label *string `json:"label"` // timed section label at FromS; null when the take has no timed sections
}

// Heat is the heat strip of one take.
type Heat struct {
	TrackID        string    `json:"track_id"`
	DurationS      float64   `json:"duration_s"`
	Bins           int       `json:"bins"`
	BinS           float64   `json:"bin_s"`
	Sessions       int       `json:"sessions"`
	Plays          int       `json:"plays"`
	CompletionRate *float64  `json:"completion_rate"`
	Heard          []int     `json:"heard"`   // distinct sessions with a segment covering the bin
	Replays        []int     `json:"replays"` // segments covering the bin beyond the first, per session, summed
	Rewinds        []int     `json:"rewinds"` // seeks that landed in the bin from further ahead
	Skips          []int     `json:"skips"`   // skips and long forward jumps that started in the bin
	Hotspots       []Hotspot `json:"hotspots"`
	Cold           []int     `json:"cold"` // up to three bins people left most often
}

type seg struct{ from, to int64 }

func isDiagnostic(kind string) bool {
	switch kind {
	case "start", "segment", "seek", "complete", "skip":
		return false
	}
	return true
}

// ComputeHeat bins the events of one take of the given duration. Diagnostic kinds never count. Without a duration the
// strip is empty (Bins 0). Subsonic scrobbles carry no seeks or skips: they contribute start, segment and complete only.
func ComputeHeat(durationMS int64, ev []listening.Event, bins int) Heat {
	if bins <= 0 {
		bins = Bins
	}
	bins = min(bins, maxBins)
	h := Heat{Heard: []int{}, Replays: []int{}, Rewinds: []int{}, Skips: []int{}, Hotspots: []Hotspot{}, Cold: []int{}}
	if durationMS <= 0 {
		return h
	}
	h.DurationS = float64(durationMS) / 1000
	h.Bins = bins
	h.BinS = h.DurationS / float64(bins)
	h.Heard, h.Replays, h.Rewinds, h.Skips = make([]int, bins), make([]int, bins), make([]int, bins), make([]int, bins)
	edge := func(b int) int64 { return durationMS * int64(b) / int64(bins) }
	binOf := func(p int64) int {
		if p < 0 {
			return 0
		}
		if p >= durationMS {
			return bins - 1
		}
		return min(int(p*int64(bins)/durationMS), bins-1)
	}

	sessions := map[string]bool{}
	segs := map[string][]seg{}
	var starts, completes int
	for _, e := range ev {
		if isDiagnostic(e.Kind) {
			continue
		}
		sessions[e.SessionID] = true
		switch e.Kind {
		case "start":
			starts++
		case "complete":
			completes++
		case "segment":
			if e.FromMS != nil && e.ToMS != nil && *e.ToMS > *e.FromMS {
				segs[e.SessionID] = append(segs[e.SessionID], seg{max(0, *e.FromMS), min(durationMS, *e.ToMS)})
			}
		case "seek":
			if e.FromMS == nil || e.ToMS == nil {
				continue
			}
			from, to := *e.FromMS, *e.ToMS
			switch {
			case to < from-rewindSlackMS:
				h.Rewinds[binOf(to)]++
			case to-from > skipJumpMS:
				h.Skips[binOf(from)]++
			}
		case "skip":
			if e.FromMS != nil {
				h.Skips[binOf(*e.FromMS)]++
			}
		}
	}
	h.Sessions, h.Plays = len(sessions), starts
	if starts > 0 {
		r := min(1, float64(completes)/float64(starts))
		h.CompletionRate = &r
	}
	for _, list := range segs {
		for b := 0; b < bins; b++ {
			e0, e1 := edge(b), edge(b+1)
			if e1 <= e0 {
				continue
			}
			cover := 0
			for _, s := range list {
				if ov := min(s.to, e1) - max(s.from, e0); ov > 0 && ov*coverageDenom >= (e1-e0)*coverageNumer {
					cover++
				}
			}
			if cover > 0 {
				h.Heard[b]++
				h.Replays[b] += cover - 1
			}
		}
	}
	h.hotspots()
	h.cold()
	return h
}

func (h *Heat) hotspots() {
	weight := func(b int) int { return rewindWeight*h.Rewinds[b] + h.Replays[b] }
	var spans []Hotspot
	for b := 0; b < h.Bins; {
		if weight(b) < hotspotMinH {
			b++
			continue
		}
		start, top := b, 0
		for ; b < h.Bins && weight(b) >= hotspotMinH; b++ {
			top = max(top, weight(b))
		}
		spans = append(spans, Hotspot{FromS: float64(start) * h.BinS, ToS: float64(b) * h.BinS, Score: top})
	}
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].Score > spans[j].Score })
	h.Hotspots = spans[:min(len(spans), maxHotspots)]
	if h.Hotspots == nil {
		h.Hotspots = []Hotspot{}
	}
}

func (h *Heat) cold() {
	var idx []int
	for b, n := range h.Skips {
		if n > 0 {
			idx = append(idx, b)
		}
	}
	sort.SliceStable(idx, func(i, j int) bool { return h.Skips[idx[i]] > h.Skips[idx[j]] })
	h.Cold = idx[:min(len(idx), maxColdBins)]
	if h.Cold == nil {
		h.Cold = []int{}
	}
}

// LabelHotspots sets each hotspot's label to the timed section at its start; untimed takes leave it null.
func (h *Heat) LabelHotspots(t anchor.Timing) {
	for i := range h.Hotspots {
		if p := anchor.Compute(t, h.Hotspots[i].FromS); p.Basis == anchor.BasisSection {
			l := p.Label
			h.Hotspots[i].Label = &l
		}
	}
}
