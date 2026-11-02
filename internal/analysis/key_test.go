package analysis

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/perftest"
)

// E1 step 0 (C136.1): the prototype measured, on I-IV-V-I triads with 6 partials at 1/n (the hardest spectrum tried:
// the plain peak-to-pitch-class vote, no subharmonics, put 12 of 24 keys on the wrong tonic because the third partial
// of every note is the fifth above it) and 60 s per signal:
//
//	no subharmonic vote (the draft): plain 12 exact + 12 within r/margin only as the dominant or the mediant minor
//	6 subharmonics, decay 0.7 (shipped): plain 24/24 exact (min r 0.91, min margin 0.15), -35 cents 23/24 exact + 1 null
//	  (margin 0.033), +20 cents 24/24; the same holds for partial decays 1/n^0.5, 1/n^2, an I-vi-IV-V progression and 10 partials
//
// Real recordings were not available to the prototype; the operator spot check (X2) is the real-music measurement.
// The bars below are the plan's (22 of 24 exact and every result within {exact, relative, fifth}; at -35 cents 20 of 24).
func keyRel(tonic int, minor bool, got Key) string {
	switch {
	case got.Key == nil:
		return "null"
	case *got.Tonic == tonic && (*got.Mode == "minor") == minor:
		return "exact"
	case minor && *got.Mode == "major" && *got.Tonic == (tonic+3)%12, !minor && *got.Mode == "minor" && *got.Tonic == (tonic+9)%12:
		return "relative"
	case (*got.Mode == "minor") == minor && (*got.Tonic == (tonic+7)%12 || *got.Tonic == (tonic+5)%12):
		return "fifth"
	}
	return "other"
}

func TestKey_SyntheticTriads(t *testing.T) {
	for _, tc := range []struct {
		name        string
		cents       float64
		wantExact   int
		short       bool // also run under -short (a subset of the keys)
		shortTonics []int
	}{
		{"plain", 0, 22, true, []int{0, 4, 9}},
		{"detuned -35 cents", -35, 20, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tonics := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
			if testing.Short() {
				if !tc.short {
					t.Skip("full 24-key run only without -short")
				}
				tonics = tc.shortTonics
			}
			counts := map[string]int{}
			results := make(chan string)
			total := 0
			for _, tonic := range tonics {
				for _, minor := range []bool{false, true} {
					total++
					go func() {
						got := ComputeKey(NewPCM(triadSignal(tonic, minor, tc.cents, 60, 5), sigRate))
						rel := keyRel(tonic, minor, got)
						if rel != "exact" {
							t.Logf("%s: got %s (r %.3f, margin %.3f)", KeyName(tonic, minor), rel, got.R, got.Margin)
						}
						results <- rel
					}()
				}
			}
			for range total {
				counts[<-results]++
			}
			want := tc.wantExact * total / 24
			if counts["exact"] < want {
				t.Errorf("exact %d of %d, want >= %d (%v)", counts["exact"], total, want, counts)
			}
			if counts["other"] > 0 {
				t.Errorf("%d results outside {exact, relative, fifth}: %v", counts["other"], counts)
			}
		})
	}
}

func TestKey_CamelotAndNames(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		tonic int
		minor bool
		name  string
		cam   string
	}{
		{0, false, "C major", "8B"}, {9, true, "A minor", "8A"}, {7, false, "G major", "9B"}, {4, true, "E minor", "9A"},
		{11, false, "B major", "1B"}, {8, true, "G# minor", "1A"}, {1, false, "Db major", "3B"}, {3, true, "Eb minor", "2A"},
		{6, false, "F# major", "2B"}, {1, true, "C# minor", "12A"}, {4, false, "E major", "12B"}, {2, true, "D minor", "7A"},
	} {
		if got := KeyName(tc.tonic, tc.minor); got != tc.name {
			t.Errorf("KeyName(%d, %v) = %q, want %q", tc.tonic, tc.minor, got, tc.name)
		}
		if got := KeyCamelot(tc.tonic, tc.minor); got != tc.cam {
			t.Errorf("KeyCamelot(%d, %v) = %q, want %q", tc.tonic, tc.minor, got, tc.cam)
		}
	}
}

func TestKey_ResultFields(t *testing.T) {
	t.Parallel()
	got := ComputeKey(NewPCM(triadSignal(9, true, 0, 30, 5), sigRate))
	if got.Key == nil || *got.Key != "A minor" || *got.Camelot != "8A" || *got.Tonic != 9 || *got.Mode != "minor" {
		t.Fatalf("A minor triads: %+v", got)
	}
	if got.R < 0.45 || got.Margin < 0.04 || len(got.Alternatives) != 3 || got.AnalyzedS != 30 {
		t.Errorf("fields: %+v", got)
	}
	if math.Abs(got.TuningCents) > 5 {
		t.Errorf("tuning %.1f cents on an A = 440 signal", got.TuningCents)
	}
	flat := ComputeKey(NewPCM(triadSignal(9, true, -35, 30, 5), sigRate))
	if flat.TuningCents < -40 || flat.TuningCents > -30 {
		t.Errorf("tuning %.1f cents, want about -35", flat.TuningCents)
	}
}

func TestKey_NoiseNil(t *testing.T) {
	t.Parallel()
	got := ComputeKey(NewPCM(whiteNoise(60, 0.3, 1), sigRate))
	if got.Key != nil || got.Camelot != nil || got.Tonic != nil || got.Mode != nil {
		t.Errorf("white noise gave a key: %+v", got)
	}
	// a cluster of 12 sines a semitone apart, 25 cents off the grid: no pitch class stands out
	n := 60 * sigRate
	x := make([]float32, n)
	for i := range x {
		var v float64
		for k := range 12 {
			v += math.Sin(2 * math.Pi * 220 * math.Pow(2, (float64(k)+0.25)/12) * float64(i) / sigRate)
		}
		x[i] = float32(0.05 * v)
	}
	if got := ComputeKey(NewPCM(x, sigRate)); got.Key != nil {
		t.Errorf("a chromatic cluster gave a key: %+v", got)
	}
}

func TestKey_Percussive(t *testing.T) {
	t.Parallel()
	if got := ComputeKey(NewPCM(clickTrack(60, 4, 0.7, 5), sigRate)); got.Key != nil {
		t.Errorf("a click track gave a key: %+v", got)
	}
}

func TestKey_ShortFile(t *testing.T) {
	t.Parallel()
	got := ComputeKey(NewPCM(triadSignal(0, false, 0, 19, 5), sigRate))
	if got.Key != nil || got.AnalyzedS != 19 || got.Alternatives == nil {
		t.Errorf("19 s: %+v", got)
	}
	if got := ComputeKey(PCM{}); got.Key != nil || got.Alternatives == nil {
		t.Errorf("empty: %+v", got)
	}
	// silence is a null key, not an error
	if got := ComputeKey(NewPCM(make([]float32, 30*sigRate), sigRate)); got.Key != nil {
		t.Errorf("silence: %+v", got)
	}
}

func TestKey_Deterministic(t *testing.T) {
	t.Parallel()
	x := triadSignal(2, false, 0, 30, 5)
	a, _ := json.Marshal(ComputeKey(NewPCM(x, sigRate)))
	b, _ := json.Marshal(ComputeKey(NewPCM(x, sigRate)))
	if string(a) != string(b) {
		t.Errorf("two runs differ:\n%s\n%s", a, b)
	}
}

func TestKey_NaNIsZeroed(t *testing.T) {
	t.Parallel()
	x := triadSignal(0, false, 0, 30, 5)
	clean := ComputeKey(NewPCM(x, sigRate))
	y := append([]float32(nil), x...)
	y[1000], y[50000] = float32(math.NaN()), float32(math.Inf(1))
	got := ComputeKey(NewPCM(y, sigRate))
	if got.Key == nil || *got.Key != "C major" || clean.Key == nil {
		t.Errorf("NaN input: %+v", got)
	}
}

func TestKey_Golden(t *testing.T) {
	t.Parallel()
	got, _ := json.MarshalIndent(ComputeKey(NewPCM(triadSignal(9, true, -20, 40, 5), sigRate)), "", "  ")
	goldenCheck(t, "testdata/key_triads_aminor.golden.json", append(got, '\n'))
}

func TestPerf_Key(t *testing.T) {
	budget := perftest.Budget(t, 3*time.Second)
	// serial on purpose: the budget is process CPU time
	x := triadSignal(7, false, 0, 240, 5)
	sw := perftest.Start()
	got := ComputeKey(NewPCM(x, sigRate))
	el := sw.Elapsed()
	t.Logf("240 s of key analysis: %v CPU (budget %v)", el, budget)
	if el > budget {
		t.Errorf("240 s of key analysis took %v CPU, budget %v", el, budget)
	}
	if got.Key == nil || *got.Key != "G major" {
		t.Errorf("key = %+v", got)
	}
}

func goldenCheck(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil { //nolint:gosec // golden file
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if string(got) != string(want) {
		t.Fatalf("golden mismatch (run with -update)\n got %s\nwant %s", got, want)
	}
}

var _ = fmt.Sprint
