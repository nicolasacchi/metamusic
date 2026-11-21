package tierb_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
)

// frames builds a contour from (midi, frames, conf) runs; midi 0 is unvoiced.
type run struct{ midi, n, conf int }

func contour(runs ...run) tierb.Pitch {
	p := tierb.Pitch{HopS: tierb.PitchHopS, Cents: []int{}, Conf: []int{}}
	for _, r := range runs {
		for range r.n {
			p.Cents = append(p.Cents, r.midi*100)
			p.Conf = append(p.Conf, r.conf)
		}
	}
	return p
}

func TestSungNotes_FromFrames(t *testing.T) {
	t.Parallel()
	p := contour(
		run{0, 5, 0},    // silence
		run{60, 10, 90}, // C4, 0.2 s, starts at 0.10 s
		run{62, 6, 90},  // D4
		run{0, 3, 0},
		run{64, 3, 90}, // too short: 60 ms
		run{0, 2, 0},
		run{65, 8, 40}, // not confident
		run{67, 5, 90}, // G4, 0.1 s
	)
	// a glitch frame of one octave up inside the C4 run is removed by the median filter
	p.Cents[8] = 7200
	got := tierb.SungNotes(p)
	want := []tierb.Note{
		{StartS: 0.10, DurS: 0.20, Pitch: 60},
		{StartS: 0.30, DurS: 0.12, Pitch: 62},
		{StartS: 0.74, DurS: 0.10, Pitch: 67},
	}
	if diff := cmp.Diff(want, got, cmp.Comparer(func(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 })); diff != "" {
		t.Errorf("SungNotes mismatch (-want +got):\n%s", diff)
	}
}

func TestSungNotes_SplitsOnLargeSteps(t *testing.T) {
	t.Parallel()
	// 4 frames at +0, then a slow glide that leaves the +-60 cent band starts a new note
	p := tierb.Pitch{HopS: 0.02, Cents: []int{6000, 6000, 6010, 6000, 6000, 6100, 6100, 6100, 6100, 6100}, Conf: []int{80, 80, 80, 80, 80, 80, 80, 80, 80, 80}}
	got := tierb.SungNotes(p)
	if len(got) != 2 || got[0].Pitch != 60 || got[1].Pitch != 61 {
		t.Errorf("notes = %+v, want C then C#", got)
	}
}

func TestSungNotes_Degenerate(t *testing.T) {
	t.Parallel()
	if got := tierb.SungNotes(tierb.Pitch{}); len(got) != 0 {
		t.Errorf("empty contour gave %v", got)
	}
	// mismatched lengths use the shorter; a zero hop falls back to the contract hop
	p := tierb.Pitch{Cents: []int{6000, 6000, 6000, 6000, 6000}, Conf: []int{90, 90, 90, 90}}
	if got := tierb.SungNotes(p); len(got) != 1 || got[0].DurS < 0.079 || got[0].DurS > 0.081 {
		t.Errorf("notes = %+v", got)
	}
}
