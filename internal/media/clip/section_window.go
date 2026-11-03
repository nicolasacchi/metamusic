package clip

import (
	"math"
)

// SectionPreRollS is how much of the music before a section start a section window includes (D-M5-24).
const SectionPreRollS = 2.0

// Section window length bounds in seconds (D-M5-24).
const (
	sectionMinS = 10.0
	sectionMaxS = 45.0
)

// SectionSpan is a section's [StartS, EndS) on one track, in seconds.
type SectionSpan struct{ StartS, EndS float64 }

func (s SectionSpan) valid() bool {
	return !math.IsNaN(s.StartS) && !math.IsNaN(s.EndS) && !math.IsInf(s.StartS, 0) && !math.IsInf(s.EndS, 0) && s.StartS >= 0 && s.EndS > s.StartS
}

// ResolveSectionWindows returns the two equal-length duel windows of a section-locked duel (C258, D-M5-24): both are
// L = clamp(min(lenA, lenB) + 2 s, 10 s, 45 s) long on the 100 ms grid (and never longer than the shorter track), each
// starts 2 s before its own section (not before 0) and is shifted back when it would run past its track's end. Pure.
// CLIP_WINDOW_INVALID (422) when a span is empty, starts beyond its track or a track is too short for the window.
func ResolveSectionWindows(a, b SectionSpan, durA, durB float64) (wa, wb Window, err error) {
	da, db := durationMS(durA), durationMS(durB)
	lo, hi := int64(sectionMinS*1000), int64(sectionMaxS*1000)
	if !a.valid() || !b.valid() {
		return wa, wb, windowErr(min(da, db), lo, hi, "a section must have 0 <= start < end")
	}
	if da <= 0 || db <= 0 || roundGrid(a.StartS) >= da || roundGrid(b.StartS) >= db {
		return wa, wb, windowErr(min(da, db), lo, hi, "the section starts beyond the end of the track")
	}
	l := roundGrid(min(a.EndS-a.StartS, b.EndS-b.StartS) + SectionPreRollS)
	l = min(max(l, lo), hi)
	l = min(l, min(da, db))
	if l < lo {
		return wa, wb, windowErr(min(da, db), lo, hi, "a track is too short for a section window")
	}
	place := func(s SectionSpan, d int64) Window {
		start := max(0, roundGrid(s.StartS-SectionPreRollS))
		if start+l > d {
			start = floorGrid(d - l)
		}
		return Window{StartMS: start, EndMS: start + l}
	}
	return place(a, da), place(b, db), nil
}
