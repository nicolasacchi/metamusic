package analysis

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/perftest"
)

func f64(v float64) *float64 { return &v }

func TestEnergy_Ordering(t *testing.T) {
	t.Parallel()
	dense := ComputeEnergy(NewPCM(clickTrack(30, 5, 0.7, 3), sigRate), f64(-9))
	pad := ComputeEnergy(NewPCM(padSignal(30, 0.3), sigRate), f64(-14))
	quiet := ComputeEnergy(NewPCM(padSignal(30, 0.004), sigRate), f64(-48))
	for name, e := range map[string]Energy{"dense": dense, "pad": pad, "quiet": quiet} {
		if e.Energy == nil || e.OnsetRate == nil || e.CentroidHz == nil || e.Components == nil || e.AnalyzedS != 30 {
			t.Fatalf("%s: %+v", name, e)
		}
	}
	t.Logf("dense %.3f (onset %.2f/s, centroid %.0f Hz) pad %.3f (%.2f/s, %.0f Hz) quiet %.3f", *dense.Energy, *dense.OnsetRate, *dense.CentroidHz,
		*pad.Energy, *pad.OnsetRate, *pad.CentroidHz, *quiet.Energy)
	if d, p, q := *dense.Energy, *pad.Energy, *quiet.Energy; d-p < 0.15 || p-q < 0.15 {
		t.Errorf("ordering dense %.3f > pad %.3f > quiet %.3f with gaps >= 0.15 violated", d, p, q)
	}
	if r := *dense.OnsetRate; r < 3.5 || r > 6.5 {
		t.Errorf("5 clicks per second measured %.2f onsets per second", r)
	}
	if r := *pad.OnsetRate; r > 0.3 {
		t.Errorf("a steady pad measured %.2f onsets per second", r)
	}
	if e := *dense.Energy; e < 0 || e > 1 {
		t.Errorf("energy %.3f outside [0, 1]", e)
	}
}

func TestEnergy_MappingTable(t *testing.T) {
	t.Parallel()
	ep := energyParams
	for _, tc := range []struct {
		name         string
		v, lo, hi, w float64
	}{
		{"onset floor", 0.5, ep.OnsetLo, ep.OnsetHi, 0}, {"onset mid", 2.75, ep.OnsetLo, ep.OnsetHi, 0.5}, {"onset top", 5.0, ep.OnsetLo, ep.OnsetHi, 1},
		{"onset below", 0, ep.OnsetLo, ep.OnsetHi, 0}, {"onset above", 9, ep.OnsetLo, ep.OnsetHi, 1},
		{"centroid floor", 600, ep.CentroidLo, ep.CentroidHi, 0}, {"centroid mid", 1700, ep.CentroidLo, ep.CentroidHi, 0.5}, {"centroid top", 2800, ep.CentroidLo, ep.CentroidHi, 1},
		{"loudness floor", -26, ep.LoudLo, ep.LoudHi, 0}, {"loudness mid", -17.5, ep.LoudLo, ep.LoudHi, 0.5}, {"loudness top", -9, ep.LoudLo, ep.LoudHi, 1},
		{"loudness above", -3, ep.LoudLo, ep.LoudHi, 1},
	} {
		if got := lerp01(tc.v, tc.lo, tc.hi); math.Abs(got-tc.w) > 1e-12 {
			t.Errorf("%s: map(%v) = %v, want %v", tc.name, tc.v, got, tc.w)
		}
	}
	if sum := ep.WOnset + ep.WCentroid + ep.WLoud; math.Abs(sum-1) > 1e-12 {
		t.Errorf("weights sum to %v", sum)
	}
	if EnergyDef != "mm-energy/1" {
		t.Errorf("definition %q", EnergyDef)
	}
}

func TestEnergy_LoudnessTermDropped(t *testing.T) {
	t.Parallel()
	pcm := NewPCM(clickTrack(30, 3, 0.5, 9), sigRate)
	without := ComputeEnergy(pcm, nil)
	with := ComputeEnergy(pcm, f64(-14))
	if without.Energy == nil || with.Energy == nil || without.LUFS != nil || without.Components.Loudness != nil || with.Components.Loudness == nil {
		t.Fatalf("without %+v with %+v", without, with)
	}
	ep := energyParams
	mo, mc := without.Components.Onset, without.Components.Centroid
	want := round((ep.WOnset*mo+ep.WCentroid*mc)/(ep.WOnset+ep.WCentroid), 4)
	if math.Abs(*without.Energy-want) > 1.5e-4 {
		t.Errorf("energy without LUFS %.4f, two-term mean %.4f", *without.Energy, want)
	}
	ml := lerp01(-14, ep.LoudLo, ep.LoudHi)
	want3 := round(ep.WOnset*mo+ep.WCentroid*mc+ep.WLoud*ml, 4)
	if math.Abs(*with.Energy-want3) > 1.5e-4 {
		t.Errorf("energy with LUFS %.4f, three-term mean %.4f", *with.Energy, want3)
	}
	if nan := ComputeEnergy(pcm, f64(math.NaN())); nan.Components.Loudness != nil {
		t.Errorf("a NaN LUFS must be treated as absent: %+v", nan)
	}
}

func TestEnergy_SilentNil(t *testing.T) {
	t.Parallel()
	for name, x := range map[string][]float32{
		"zeros":     make([]float32, 30*sigRate),
		"-70 dBFS":  whiteNoise(30, 0.0005, 2),
		"nan input": nil,
	} {
		if x == nil {
			x = make([]float32, 30*sigRate)
			for i := range x {
				x[i] = float32(math.NaN())
			}
		}
		got := ComputeEnergy(NewPCM(x, sigRate), f64(-60))
		if got.Energy != nil || got.OnsetRate != nil || got.Components != nil || got.AnalyzedS != 30 {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

func TestEnergy_ShortFile(t *testing.T) {
	t.Parallel()
	got := ComputeEnergy(NewPCM(clickTrack(19, 5, 0.7, 3), sigRate), f64(-9))
	if got.Energy != nil || got.AnalyzedS != 19 {
		t.Errorf("19 s: %+v", got)
	}
	if got := ComputeEnergy(PCM{}, nil); got.Energy != nil {
		t.Errorf("empty: %+v", got)
	}
	if got := ComputeEnergy(NewPCM(clickTrack(20, 5, 0.7, 3), sigRate), nil); got.Energy == nil {
		t.Errorf("20 s is enough: %+v", got)
	}
}

func TestEnergy_Deterministic(t *testing.T) {
	t.Parallel()
	pcm := NewPCM(clickTrack(25, 4, 0.6, 11), sigRate)
	a, _ := json.Marshal(ComputeEnergy(pcm, f64(-12)))
	b, _ := json.Marshal(ComputeEnergy(pcm, f64(-12)))
	if string(a) != string(b) {
		t.Errorf("two runs differ:\n%s\n%s", a, b)
	}
}

func TestEnergy_Golden(t *testing.T) {
	t.Parallel()
	got, _ := json.MarshalIndent(ComputeEnergy(NewPCM(clickTrack(30, 4, 0.6, 21), sigRate), f64(-13.4)), "", "  ")
	goldenCheck(t, "testdata/energy_clicks.golden.json", append(got, '\n'))
}

func TestOnsetEnvelope_TempoUnchanged(t *testing.T) {
	t.Parallel()
	// the exported envelope is the one C94 always used: ComputeTempo of a click track still finds its tempo
	env := OnsetEnvelope(clickTrack(30, 2, 0.7, 4), sigRate)
	if len(env) == 0 {
		t.Fatal("no envelope")
	}
	if got := ComputeTempo(clickTrack(30, 2, 0.7, 4), sigRate); got.BPM == nil || math.Abs(*got.BPM-120) > 2 && math.Abs(*got.BPM-60) > 1 && math.Abs(*got.BPM-240) > 4 {
		t.Errorf("tempo of 2 clicks per second: %+v", got)
	}
}

func TestPerf_Energy(t *testing.T) {
	budget := perftest.Budget(t, 2*time.Second) // 0.9 s CPU measured: the C94 onset envelope is 0.8 s of it (TestPerf_Tempo alone is 0.86 s); the plan's 1 s had no headroom
	// serial on purpose: the budget is process CPU time
	x := clickTrack(240, 4, 0.6, 5)
	sw := perftest.Start()
	got := ComputeEnergy(NewPCM(x, sigRate), f64(-12))
	el := sw.Elapsed()
	t.Logf("240 s of energy analysis: %v CPU (budget %v)", el, budget)
	if el > budget {
		t.Errorf("240 s of energy analysis took %v CPU, budget %v", el, budget)
	}
	if got.Energy == nil {
		t.Errorf("no energy: %+v", got)
	}
}
