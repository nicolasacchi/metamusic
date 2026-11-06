package generation

// Repaint A/B window (C149.5, D-E2-38). A duel window is the clip window: 30..45 s on the 100 ms grid. A repaint range is
// often shorter, so the window around it is widened to 30 s; a longer range up to 45 s is used as it is; a longer one is cut
// to its first 45 s and reported as ab_window_truncated. Pure: the SPA feeds the result to the existing get_clip and
// create_duel operations, no new operation exists.

const (
	abMinDS = 300 // the shortest duel window, 30 s
	abMaxDS = 450 // the longest duel window, 45 s
)

// ABWindow is a duel window in deciseconds (the 100 ms grid of clip.ResolveWindow).
type ABWindow struct {
	FromDS, ToDS int
	// Truncated is true when the repaint range was longer than 45 s and only its first 45 s are compared; the estimate-style
	// warning WarnABWindowTruncated is the caller's to show.
	Truncated bool
}

// RepaintABWindow is the duel window of a repaint of [fromDS, toDS) in a take of durationDS: a range shorter than 30 s is
// centred in a 30 s window, shifted to stay inside [0, durationDS]; 30..45 s is the range itself; longer is the first 45 s.
// A take shorter than the window gives the whole take (the clip service then refuses it as too short, which is the honest
// answer). Inputs outside [0, durationDS] are clamped; an empty or reversed range yields the window at the start.
func RepaintABWindow(fromDS, toDS, durationDS int) ABWindow {
	durationDS = max(durationDS, 0)
	fromDS = min(max(fromDS, 0), durationDS)
	toDS = min(max(toDS, fromDS), durationDS)
	n := toDS - fromDS
	switch {
	case n > abMaxDS:
		return ABWindow{FromDS: fromDS, ToDS: fromDS + abMaxDS, Truncated: true}
	case n >= abMinDS:
		return ABWindow{FromDS: fromDS, ToDS: toDS}
	}
	if durationDS <= abMinDS {
		return ABWindow{FromDS: 0, ToDS: durationDS}
	}
	start := (fromDS+toDS)/2 - abMinDS/2
	start = min(max(start, 0), durationDS-abMinDS)
	return ABWindow{FromDS: start, ToDS: start + abMinDS}
}
