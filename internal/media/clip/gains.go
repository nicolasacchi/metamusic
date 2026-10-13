package clip

import "math"

func roundCDB(db float64) int { return int(math.Round(db * 100)) }

// MatchGains computes the duel gains (D-M2b-2): both windows above SilentLUFS
// are attenuated to the quieter one, never more than MaxCutCDB (a clamp makes
// the status "limited" and leaves the target as measured). Either window at
// or below SilentLUFS disables matching ("window_silent").
func MatchGains(a, b Measure) (ga, gb int, target *float64, status string) {
	if a.IntegratedLUFS <= SilentLUFS || b.IntegratedLUFS <= SilentLUFS {
		return 0, 0, nil, "window_silent"
	}
	t := math.Min(a.IntegratedLUFS, b.IntegratedLUFS)
	status = "matched"
	gain := func(x float64) int {
		g := roundCDB(t - x)
		if g < MaxCutCDB {
			status = "limited"
			return MaxCutCDB
		}
		return g
	}
	ga, gb = gain(a.IntegratedLUFS), gain(b.IntegratedLUFS)
	return ga, gb, &t, status
}

// TargetGain is the standalone get_clip rule (attenuate only): a window at or
// below SilentLUFS is untouched ("window_silent"); a window already quieter
// than the target stays at gain 0 ("limited"); a cut deeper than MaxCutCDB is
// capped ("limited").
func TargetGain(m Measure, targetLUFS float64) (gcdb int, status string) {
	if m.IntegratedLUFS <= SilentLUFS {
		return 0, "window_silent"
	}
	want := roundCDB(targetLUFS - m.IntegratedLUFS)
	switch {
	case want > 0:
		return 0, "limited"
	case want < MaxCutCDB:
		return MaxCutCDB, "limited"
	}
	return want, "matched"
}
