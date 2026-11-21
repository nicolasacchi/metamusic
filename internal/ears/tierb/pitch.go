package tierb

import (
	"math"
	"sort"
)

// Sung-note segmentation (C336.3).
const (
	pitchMinConf   = 50  // a frame is voiced when its cents are positive and its confidence is at least this
	pitchMedianWin = 5   // median filter width, in frames
	pitchBandCents = 60  // a note holds the frames within this many cents of its running median
	pitchMinFrames = 4   // a note lasts at least this many frames (80 ms at the contract hop)
	centsPerSemi   = 100 // pitch.cents is 100 x the MIDI note number
)

// Note is one sung note segmented from the pitch contour: Pitch is a MIDI note number, StartS and DurS are seconds from the
// start of the audio.
type Note struct {
	StartS float64
	DurS   float64
	Pitch  int
}

// SungNotes segments the vocal pitch contour into notes (C336.3): voiced frames (cents > 0, conf >= 50) are smoothed by a median
// filter over five frames, and a note is a run of at least four voiced frames within +-60 cents of the run's median, its pitch
// round(median / 100) and its weight its duration. Unvoiced frames end a note. Pure and deterministic.
func SungNotes(p Pitch) []Note {
	hop := p.HopS
	if hop <= 0 {
		hop = PitchHopS
	}
	n := min(len(p.Cents), len(p.Conf))
	voiced := func(i int) bool { return p.Cents[i] > 0 && p.Conf[i] >= pitchMinConf }

	// median filter over the voiced frames of a five-frame window
	sm := make([]float64, n)
	var win []float64
	for i := range n {
		if !voiced(i) {
			continue
		}
		win = win[:0]
		for j := max(0, i-pitchMedianWin/2); j <= min(n-1, i+pitchMedianWin/2); j++ {
			if voiced(j) {
				win = append(win, float64(p.Cents[j]))
			}
		}
		sm[i] = median(win)
	}

	var out []Note
	var seg []float64
	start := 0
	flush := func() {
		if len(seg) >= pitchMinFrames {
			out = append(out, Note{StartS: float64(start) * hop, DurS: float64(len(seg)) * hop,
				Pitch: int(math.Round(median(seg) / centsPerSemi))})
		}
		seg = seg[:0]
	}
	for i := range n {
		switch {
		case !voiced(i):
			flush()
		case len(seg) > 0 && math.Abs(sm[i]-median(seg)) > pitchBandCents:
			flush()
			start, seg = i, append(seg, sm[i])
		default:
			if len(seg) == 0 {
				start = i
			}
			seg = append(seg, sm[i])
		}
	}
	flush()
	return out
}

// median of a non-empty slice; the input is not modified.
func median(v []float64) float64 {
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	if len(c)%2 == 1 {
		return c[len(c)/2]
	}
	return (c[len(c)/2-1] + c[len(c)/2]) / 2
}
