package align_test

import (
	"math"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/align"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
)

// train is single-sample clicks at the given times with the given heights.
func train(rate int, seconds float64, times, amps []float64) []float32 {
	x := make([]float32, dsptest.Frames(rate, seconds))
	for i, t := range times {
		if j := int(math.Floor(t*float64(rate) + 0.5)); j >= 0 && j < len(x) {
			x[j] = float32(amps[i])
		}
	}
	return x
}

// beats is the click times of a steady pulse: first + k*60/bpm below seconds; amp(k) is the height of click k.
func beats(first, bpm, seconds float64, amp func(k int) float64) (times, amps []float64) {
	for k := 0; ; k++ {
		t := first + float64(k)*60/bpm
		if t >= seconds {
			return
		}
		times, amps = append(times, t), append(amps, amp(k))
	}
}

// circ is the distance of a and b modulo period.
func circ(a, b, period float64) float64 {
	d := math.Mod(a-b, period)
	if d < 0 {
		d += period
	}
	return math.Min(d, period-d)
}

// frameS is one envelope frame (the fixed hop of 128 samples) in seconds.
func frameS(rate int) float64 { return 128 / float64(rate) }

func hasWarning(info align.LocalInfo, w string) bool { return slices.Contains(info.Warnings, w) }

// TestBeatPhase_Clicks: 120 BPM in 4/4, first beat at 0.31 s. The envelope frame is 8 ms at 16 kHz and 2.7 ms at 48 kHz
// (hop 128 at any rate, D-T1-20); with a quarter-frame phase grid the phase is within 1.5 frames of the truth (the plan's 15 ms
// is the loose bound this test keeps beside it).
func TestBeatPhase_Clicks(t *testing.T) {
	t.Parallel()
	for _, rate := range []int{16000, 48000} {
		secs := 20.0
		if rate == 48000 {
			secs = 12
		}
		tol := 1.5 * frameS(rate)
		// no accent
		ts, as := beats(0.31, 120, secs, func(int) float64 { return 1 })
		x := train(rate, secs, ts, as)
		g, info := align.LocalGrid(x, rate, 120, 4, secs/2, 4)
		if d := circ(info.BeatPhaseS, 0.31, 0.5); d > tol || d > 0.015 {
			t.Errorf("%d Hz: beat phase %.4f, want 0.31 within %.4f (off by %.4f)", rate, info.BeatPhaseS, tol, d)
		}
		if info.Confidence < 3 {
			t.Errorf("%d Hz: confidence %.2f, want >= 3", rate, info.Confidence)
		}
		if math.Abs(info.LocalBPM-120) > 0.1 || g.BPM != info.LocalBPM {
			t.Errorf("%d Hz: local tempo %.3f (grid %.3f)", rate, info.LocalBPM, g.BPM)
		}
		if hasWarning(info, align.WarnBeatPhaseWeak) {
			t.Errorf("%d Hz: clean clicks are weak: %v", rate, info.Warnings)
		}
		// accent on beat 2 of each bar: the bar's downbeat is that beat, 0.5 s after the first click
		ts, as = beats(0.31, 120, secs, func(k int) float64 {
			if k%4 == 1 {
				return 1
			}
			return 0.5
		})
		g, info = align.LocalGrid(train(rate, secs, ts, as), rate, 120, 4, secs/2, 4)
		if d := circ(g.FirstDownbeatS, 0.31+0.5, 2); d > tol || d > 0.015 {
			t.Errorf("%d Hz: downbeat %.4f, want %.4f (off by %.4f)", rate, g.FirstDownbeatS, 0.81, d)
		}
		if math.Abs(g.FirstDownbeatS-secs/2) > 1 {
			t.Errorf("%d Hz: the downbeat %.3f is not the one nearest the centre", rate, g.FirstDownbeatS)
		}
		if info.DownbeatConfidence < 0.25 || hasWarning(info, align.WarnDownbeatAmbiguous) {
			t.Errorf("%d Hz: accent read as ambiguous: %.2f %v", rate, info.DownbeatConfidence, info.Warnings)
		}
	}
}

// TestBeatPhase_Noise: white noise has no pulse, so the comb is no better than the average comb.
func TestBeatPhase_Noise(t *testing.T) {
	t.Parallel()
	const rate = 16000
	for seed := uint64(1); seed <= 3; seed++ {
		x := f32(dsptest.Noise(seed, 20*rate, 0.3))
		_, info := align.LocalGrid(x, rate, 120, 4, 10, 8)
		if info.Confidence >= 1.5 {
			t.Errorf("seed %d: noise confidence %.2f, want < 1.5", seed, info.Confidence)
		}
		if !hasWarning(info, align.WarnBeatPhaseWeak) {
			t.Errorf("seed %d: no beat_phase_weak: %v", seed, info.Warnings)
		}
	}
}

// kicks is a 60 Hz decaying burst at each time, the stand-in for a kick drum.
func kicks(rate int, seconds float64, times []float64) []float32 {
	x := make([]float32, dsptest.Frames(rate, seconds))
	for _, t := range times {
		j0 := int(t*float64(rate) + 0.5)
		for i := 0; i < rate/10 && j0+i < len(x); i++ {
			x[j0+i] += float32(0.8 * math.Sin(2*math.Pi*60*float64(i)/float64(rate)) * dsp.Exp(-float64(i)/(0.02*float64(rate))))
		}
	}
	return x
}

// TestBeatPhase_FourOnFloor: a kick on every beat gives the downbeat heuristic nothing to choose by, and it says so.
func TestBeatPhase_FourOnFloor(t *testing.T) {
	t.Parallel()
	const rate = 16000
	ts, _ := beats(0.31, 120, 20, nil2)
	_, info := align.LocalGrid(kicks(rate, 20, ts), rate, 120, 4, 10, 4)
	if !hasWarning(info, align.WarnDownbeatAmbiguous) {
		t.Errorf("four on the floor not flagged ambiguous (confidence %.2f): %v", info.DownbeatConfidence, info.Warnings)
	}
	if info.DownbeatConfidence >= 0.25 {
		t.Errorf("downbeat confidence %.2f", info.DownbeatConfidence)
	}
	if d := circ(info.BeatPhaseS, 0.31, 0.5); d > 0.015 {
		t.Errorf("beat phase off by %.4f", d)
	}
}

func nil2(int) float64 { return 1 }

// TestBeatPhase_OneDrop: a kick on beat 3 only is read as the bar's downbeat (wrong by construction), and the position
// is exactly bpb/2, which is reported as a suspected one-drop.
func TestBeatPhase_OneDrop(t *testing.T) {
	t.Parallel()
	const rate = 16000
	// the first beat of the audio is beat 1 at 0.0 s; the kicks are on beat 3 of each bar
	var ts []float64
	for k := 2; ; k += 4 {
		tt := float64(k) * 0.5
		if tt >= 20 {
			break
		}
		ts = append(ts, tt)
	}
	// hats keep the pulse audible on the other beats, far weaker below 150 Hz than the kick
	var hats, ha []float64
	for k := 0; float64(k)*0.5 < 20; k++ {
		hats, ha = append(hats, float64(k)*0.5), append(ha, 0.2)
	}
	x := kicks(rate, 20, ts)
	for i, v := range train(rate, 20, hats, ha) {
		x[i] += v
	}
	g, info := align.LocalGrid(x, rate, 120, 4, 10.2, 4)
	if info.DownbeatPosition != 2 {
		t.Errorf("downbeat position %d, want 2 (beat 3)", info.DownbeatPosition)
	}
	if !hasWarning(info, align.WarnDownbeatOneDrop) {
		t.Errorf("one-drop not suspected (confidence %.2f): %v", info.DownbeatConfidence, info.Warnings)
	}
	if hasWarning(info, align.WarnDownbeatAmbiguous) {
		t.Errorf("one-drop flagged ambiguous: %v", info.Warnings)
	}
	if d := circ(g.FirstDownbeatS, 1, 2); d > 0.015 {
		t.Errorf("downbeat %.4f", g.FirstDownbeatS)
	}
}

// TestBeatPhase_Drift_LocalRefine: the pulse is 0.4 % faster than the declared integer tempo; over 140 s a global phase
// at the declared tempo would be about 0.56 s off, the local grid is within 15 ms of the true beats at the transition.
func TestBeatPhase_Drift_LocalRefine(t *testing.T) {
	t.Parallel()
	const rate = 16000
	const declared, trueBPM, first = 120.0, 120 * 1.004, 0.31
	ts, as := beats(first, trueBPM, 150, func(int) float64 { return 1 })
	x := train(rate, 150, ts, as)
	g, info := align.LocalGrid(x, rate, declared, 4, 140, 8)
	if math.Abs(info.LocalBPM/trueBPM-1) > 0.001 {
		t.Errorf("local tempo %.3f, want %.3f within 0.1%%", info.LocalBPM, trueBPM)
	}
	period := 60 / trueBPM
	if d := circ(info.BeatPhaseS, first, period); d > 0.015 {
		t.Errorf("beat at the transition off by %.4f s", d)
	}
	// every beat of the refined grid within 2 beats of 140 s is near a true click
	for k := -2; k <= 2; k++ {
		bt := info.BeatPhaseS + float64(k)*g.BeatS()
		if d := circ(bt, first, period); d > 0.015 {
			t.Errorf("grid beat %d (%.3f s) off by %.4f s", k, bt, d)
		}
	}
	// the global estimate would have been wrong: the declared tempo accumulates 0.4 % over 140 s
	if drift := 140 * 0.004 / 1.004; drift < 0.5 {
		t.Fatalf("the scenario does not drift enough: %.3f s", drift)
	}
	if hasWarning(info, align.WarnTempoRefineRejected) {
		t.Errorf("0.4%% refinement rejected: %v", info.Warnings)
	}
}

// TestBeatPhase_RefineRejected: a pulse more than 2 % from the spec's tempo is not followed: the spec's tempo is used.
func TestBeatPhase_RefineRejected(t *testing.T) {
	t.Parallel()
	const rate = 16000
	ts, as := beats(0.31, 120*1.028, 20, func(int) float64 { return 1 })
	g, info := align.LocalGrid(train(rate, 20, ts, as), rate, 120, 4, 10, 4)
	if !hasWarning(info, align.WarnTempoRefineRejected) {
		t.Errorf("no tempo_refine_rejected: %v (bpm %.3f)", info.Warnings, info.LocalBPM)
	}
	if info.LocalBPM != 120 || g.BPM != 120 {
		t.Errorf("tempo %v / %v, want the spec's 120", info.LocalBPM, g.BPM)
	}
}

// TestBeatPhase_TempoChangeInWindow: a step from 120 to 132 BPM inside the window lowers the confidence below a steady
// pulse's and never panics.
func TestBeatPhase_TempoChangeInWindow(t *testing.T) {
	t.Parallel()
	const rate = 16000
	ts, as := beats(0.31, 120, 10, func(int) float64 { return 1 })
	ts2, as2 := beats(10.31, 132, 20, func(int) float64 { return 1 })
	_, info := align.LocalGrid(train(rate, 20, append(ts, ts2...), append(as, as2...)), rate, 120, 4, 10, 8)
	ts3, as3 := beats(0.31, 120, 20, func(int) float64 { return 1 })
	_, steady := align.LocalGrid(train(rate, 20, ts3, as3), rate, 120, 4, 10, 8)
	if !(info.Confidence < steady.Confidence) {
		t.Errorf("confidence with a tempo step %.2f, steady %.2f", info.Confidence, steady.Confidence)
	}
}

// TestBeatPhase_Edges: short, silent and degenerate input answers with the unrefined grid, never panics.
func TestBeatPhase_Edges(t *testing.T) {
	t.Parallel()
	const rate = 16000
	for _, tc := range []struct {
		name string
		x    []float32
		bpm  float64
		bpb  int
	}{
		{"empty", nil, 120, 4}, {"below one window", make([]float32, 1000), 120, 4}, {"silence", make([]float32, 3*rate), 120, 4},
		{"zero tempo", make([]float32, rate), 0, 4}, {"zero bpb", make([]float32, rate), 120, 0}, {"one beat per bar", train(rate, 4, []float64{0.5, 1, 1.5}, []float64{1, 1, 1}), 120, 1},
	} {
		g, info := align.LocalGrid(tc.x, rate, tc.bpm, tc.bpb, 1, 4)
		if g.BeatsPerBar < 1 {
			t.Errorf("%s: bpb %d", tc.name, g.BeatsPerBar)
		}
		if len(tc.x) < 1024 || tc.bpm == 0 || tc.name == "silence" {
			if info.Confidence != 0 || !hasWarning(info, align.WarnBeatPhaseWeak) {
				t.Errorf("%s: confidence %v warnings %v", tc.name, info.Confidence, info.Warnings)
			}
		}
	}
	// a centre outside the input is clamped inside it
	ts, as := beats(0.31, 120, 6, func(int) float64 { return 1 })
	x := train(rate, 6, ts, as)
	for _, c := range []float64{-5, 0, 3, 6, 50} {
		if _, info := align.LocalGrid(x, rate, 120, 4, c, 8); info.Confidence < 1 {
			t.Errorf("centre %v: confidence %.2f", c, info.Confidence)
		}
	}
}

// TestBeatPhase_Deterministic: the same call answers the same bits twice and pins the answers on a fixed scenario
// (amd64, GOAMD64=v1); the pinned scenario also runs under GODEBUG=cpu.fma=off (TestDeterminism_FMAOff).
func TestBeatPhase_Golden(t *testing.T) {
	dsptest.RequireGolden(t)
	got := beatGolden()
	again := beatGolden()
	for k := range got {
		if got[k] != again[k] {
			t.Fatalf("%s differs between two calls", k)
		}
	}
	const path = "testdata/beat-golden.txt"
	if *update {
		writeGolden(t, path, "# sha256 of the bit patterns of LocalGrid's answers on fixed scenarios. linux/amd64, GOAMD64=v1.\n", got)
	}
	want := readGolden(t, path)
	for k, h := range got {
		if h != want[k] {
			t.Errorf("%s: hash %s, golden %s", k, h, want[k])
		}
	}
}

func beatGolden() map[string]string {
	const rate = 16000
	ts, as := beats(0.31, 120*1.004, 30, func(k int) float64 {
		if k%4 == 1 {
			return 1
		}
		return 0.5
	})
	x := train(rate, 30, ts, as)
	for i, v := range dsptest.Noise(5, len(x), 0.02) {
		x[i] += float32(v)
	}
	g, info := align.LocalGrid(x, rate, 120, 4, 20, 8)
	return map[string]string{
		"drift": hashFloats([]float64{g.BPM, g.FirstDownbeatS, info.BeatPhaseS, info.Confidence, info.DownbeatConfidence, float64(info.DownbeatPosition)}),
	}
}

// TestDeterminism_FMAOff re-executes the pinned beat and onset goldens under GODEBUG=cpu.fma=off and requires the same bits.
func TestDeterminism_FMAOff(t *testing.T) {
	dsptest.RequireGolden(t)
	dsptest.ReexecFMAOff(t, `^(TestOnset_PinnedForT1|TestBeatPhase_Golden)$`)
}
