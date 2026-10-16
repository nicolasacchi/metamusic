package analysis

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/perftest"
)

const tsr = 11025

// lcg is a deterministic noise source (no shared state: one per call).
type lcg struct{ s uint64 }

func (l *lcg) next() float64 {
	l.s = l.s*6364136223846793005 + 1442695040888963407
	return float64(l.s>>11)/float64(1<<52) - 1 // [-1, 1)
}

// clicks renders a kick (decaying 60 Hz sine) on every beat and a hat (short noise burst) on every offbeat.
// jitterS shifts each event by a deterministic pseudo-random amount in [-jitterS, jitterS].
func clicks(bpm, seconds, jitterS float64) []float32 {
	n := int(seconds * tsr)
	x := make([]float32, n)
	r := &lcg{s: 12345}
	period := 60 / bpm
	for k := 0; ; k++ {
		for half := range 2 {
			t := (float64(k) + float64(half)/2) * period
			if t >= seconds-0.2 {
				if half == 1 {
					return x
				}
				continue
			}
			t += jitterS * r.next()
			i0 := int(math.Max(0, t*tsr))
			if half == 0 {
				for i := 0; i < tsamples(0.12) && i0+i < n; i++ {
					tt := float64(i) / tsr
					x[i0+i] += float32(0.9 * math.Exp(-tt*35) * math.Sin(2*math.Pi*60*tt))
				}
			} else {
				prev := 0.0
				for i := 0; i < tsamples(0.03) && i0+i < n; i++ {
					v := r.next()
					hp := v - prev // crude high-pass
					prev = v
					x[i0+i] += float32(0.25 * hp * math.Exp(-float64(i)/(0.008*tsr)))
				}
			}
		}
	}
}

func near(got, want, tol float64) bool { return math.Abs(got-want) <= tol }

func TestTempo_SyntheticClicks(t *testing.T) {
	t.Parallel()
	secs := 60.0
	if testing.Short() {
		secs = 30 // keeps make check fast under -race; the full 60 s run is in make test
	}
	cases := []struct {
		bpm, jitter, tol float64
	}{{70, 0, 1.5}, {90, 0, 1.5}, {120, 0, 1.5}, {140, 0, 1.5}, {120, 0.020, 3}}
	for _, c := range cases {
		t.Run("", func(t *testing.T) {
			t.Parallel()
			got := ComputeTempo(clicks(c.bpm, secs, c.jitter), tsr)
			if got.BPM == nil {
				t.Fatalf("bpm %v jitter %v: no tempo (confidence %.2f)", c.bpm, c.jitter, got.Confidence)
			}
			ok := near(*got.BPM, c.bpm, c.tol) || near(*got.BPM, c.bpm*2, 2*c.tol) || near(*got.BPM, c.bpm/2, c.tol/2+0.5)
			if !ok {
				t.Errorf("bpm %v jitter %v: got %.2f (candidates %+v)", c.bpm, c.jitter, *got.BPM, got.Candidates)
			}
			if !near(*got.BPM, c.bpm, c.tol) { // an octave answer must say so and keep the truth as a candidate
				if !got.OctaveAmbiguous {
					t.Errorf("bpm %v: octave answer %.2f without OctaveAmbiguous", c.bpm, *got.BPM)
				} else if !slices.ContainsFunc(got.Candidates, func(k TempoCandidate) bool { return near(k.BPM, c.bpm, c.tol) }) {
					t.Errorf("bpm %v: truth not among candidates %+v", c.bpm, got.Candidates)
				}
			}
			if got.Confidence < 2.0 {
				t.Errorf("bpm %v: confidence %.2f < 2", c.bpm, got.Confidence)
			}
			if len(got.Candidates) == 0 || len(got.Candidates) > 3 {
				t.Errorf("candidates %d", len(got.Candidates))
			}
			if !near(got.AnalyzedSeconds, secs, 0.01) {
				t.Errorf("analyzed %.2f", got.AnalyzedSeconds)
			}
		})
	}
}

func TestTempo_NoiseNoBPM(t *testing.T) {
	t.Parallel()
	r := &lcg{s: 99}
	noise := make([]float32, 30*tsr)
	for i := range noise {
		noise[i] = float32(0.3 * r.next())
	}
	chord := make([]float32, 30*tsr)
	for i := range chord {
		tt := float64(i) / tsr
		chord[i] = float32(0.2 * (math.Sin(2*math.Pi*220*tt) + math.Sin(2*math.Pi*277.18*tt) + math.Sin(2*math.Pi*329.63*tt)))
	}
	for name, x := range map[string][]float32{"noise": noise, "chord": chord, "silence": make([]float32, 30*tsr)} {
		got := ComputeTempo(x, tsr)
		if got.BPM != nil {
			t.Errorf("%s: bpm %.2f (confidence %.2f), want none", name, *got.BPM, got.Confidence)
		}
	}
}

func TestTempo_ShortFile(t *testing.T) {
	t.Parallel()
	got := ComputeTempo(clicks(120, 19, 0), tsr)
	if got.BPM != nil || got.Candidates == nil {
		t.Fatalf("short file: %+v", got)
	}
	if got := ComputeTempo(nil, tsr); got.BPM != nil {
		t.Fatalf("empty: %+v", got)
	}
}

func TestTempo_Deterministic(t *testing.T) {
	t.Parallel()
	x := clicks(100, 21, 0.005)
	a, _ := json.Marshal(ComputeTempo(x, tsr))
	b, _ := json.Marshal(ComputeTempo(slices.Clone(x), tsr))
	if !bytes.Equal(a, b) {
		t.Fatalf("not deterministic:\n%s\n%s", a, b)
	}
}

func TestTempo_Golden(t *testing.T) {
	t.Parallel()
	got, err := json.MarshalIndent(ComputeTempo(clicks(100, 25, 0), tsr), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	const path = "testdata/tempo_clicks100.golden.json"
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden mismatch (run with -update)\n got %s\nwant %s", got, want)
	}
}

func TestPerf_Tempo(t *testing.T) {
	budget := perftest.Budget(t, 2*time.Second)
	x := clicks(110, 240, 0)
	sw := perftest.Start()
	got := ComputeTempo(x, tsr)
	if el := sw.Elapsed(); el > budget {
		t.Fatalf("240 s took %v, budget %v", el, budget)
	}
	if got.BPM == nil {
		t.Fatal("no tempo")
	}
}

func TestTempo_CapsAnalyzedSeconds(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("long signal")
	}
	got := ComputeTempo(clicks(110, 300, 0), tsr)
	if !near(got.AnalyzedSeconds, 240, 0.01) {
		t.Fatalf("analyzed %.2f, want 240", got.AnalyzedSeconds)
	}
}

func tsamples(s float64) int { return int(s * tsr) }
