package clip

import (
	"fmt"
	"math"

	"github.com/nicolasacchi/metamusic/internal/problem"
)

// floorGrid is x (ms) rounded down to the 100 ms grid.
func floorGrid(ms int64) int64 { return ms / GridMS * GridMS }

// roundGrid rounds seconds to the nearest 100 ms (half away from zero), in ms.
func roundGrid(s float64) int64 { return int64(math.Round(s*10)) * GridMS }

// durationMS is the track duration floored to the grid.
func durationMS(durationS float64) int64 {
	if durationS <= 0 || math.IsNaN(durationS) || math.IsInf(durationS, 0) {
		return 0
	}
	return floorGrid(int64(math.Floor(durationS*1000 + 1e-6)))
}

func windowErr(d, lo, hi int64, why string) error {
	return problem.E(422, "CLIP_WINDOW_INVALID", why, map[string]any{
		"duration_s": float64(d) / 1000, "min_s": float64(lo) / 1000, "max_s": float64(hi) / 1000,
	})
}

// DefaultWindow is the window used when the caller names none: the whole
// track when it is at most 45 s, else 45 s starting near 30 % of the track.
func DefaultWindow(durationS float64) Window {
	d := durationMS(durationS)
	l := min(int64(MaxClipMS), d)
	start := min(floorGrid(d*3/10), d-l)
	return Window{StartMS: start, EndMS: start + l}
}

// ResolveWindow validates a requested window against a duration (the shorter
// track's for a duel). With D = the duration floored to 100 ms, the allowed
// length L is min(30 s, D) <= L <= min(45 s, D). from and to are rounded to
// the nearest 100 ms; a nil from selects DefaultWindow, a nil to runs from
// `from` for min(45 s, D - from) (then validated).
func ResolveWindow(durationS float64, fromS, toS *float64) (Window, error) {
	d := durationMS(durationS)
	lo, hi := min(int64(MinClipMS), d), min(int64(MaxClipMS), d)
	if d <= 0 {
		return Window{}, windowErr(d, lo, hi, "the track has no audio to clip")
	}
	if fromS == nil && toS == nil {
		return DefaultWindow(durationS), nil
	}
	var from int64
	if fromS != nil {
		if math.IsNaN(*fromS) || math.IsInf(*fromS, 0) || *fromS < 0 {
			return Window{}, windowErr(d, lo, hi, "from_s must be a finite number of seconds >= 0")
		}
		from = roundGrid(*fromS)
	} else {
		from = DefaultWindow(durationS).StartMS
	}
	if from >= d {
		return Window{}, windowErr(d, lo, hi, "from_s is beyond the end of the track")
	}
	var to int64
	if toS != nil {
		if math.IsNaN(*toS) || math.IsInf(*toS, 0) {
			return Window{}, windowErr(d, lo, hi, "to_s must be a finite number of seconds")
		}
		to = roundGrid(*toS)
	} else {
		to = from + min(int64(MaxClipMS), d-from)
	}
	if to > d {
		return Window{}, windowErr(d, lo, hi, "to_s is beyond the end of the track")
	}
	if l := to - from; l < lo || l > hi {
		return Window{}, windowErr(d, lo, hi, fmt.Sprintf("the window must be between %.1f and %.1f seconds long", float64(lo)/1000, float64(hi)/1000))
	}
	return Window{StartMS: from, EndMS: to}, nil
}
