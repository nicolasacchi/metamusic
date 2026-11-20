package score

import (
	"errors"
	"math"
	"testing"
)

func TestScore_SecondsAtTempoMap(t *testing.T) {
	t.Parallel()
	s := &Score{PPQ: PPQ, Tempos: []Tempo{{0, 500000}, {1920, 1000000}}}
	for _, c := range []struct {
		tick int64
		want float64
	}{{0, 0}, {480, 0.5}, {1920, 2.0}, {2400, 3.0}, {3840, 6.0}} {
		if got := s.SecondsAt(c.tick); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("SecondsAt(%d) = %v, want %v", c.tick, got, c.want)
		}
	}
	// no tempo map: 120 quarters per minute
	if got := (&Score{PPQ: PPQ}).SecondsAt(960); math.Abs(got-1.0) > 1e-9 {
		t.Errorf("default tempo: %v", got)
	}
}

func TestScore_NormaliseCaps(t *testing.T) {
	t.Parallel()
	s := &Score{PPQ: PPQ, Voices: []Voice{{Notes: []Note{{Pitch: 64, Tick: 480}, {Pitch: 62, Tick: 0}, {Pitch: 60, Tick: 0}}}}}
	if err := s.Normalise(); err != nil {
		t.Fatal(err)
	}
	if n := s.Voices[0].Notes; n[0].Pitch != 60 || n[1].Pitch != 62 || n[2].Pitch != 64 {
		t.Errorf("not sorted by (tick, pitch): %+v", n)
	}
	many := &Score{PPQ: PPQ, Voices: make([]Voice, MaxVoices+1)}
	if err := many.Normalise(); !errors.Is(err, ErrTooManyVoices) {
		t.Errorf("voices: %v", err)
	}
	ev := &Score{PPQ: PPQ, Voices: []Voice{{Notes: make([]Note, MaxEvents+1)}}}
	if err := ev.Normalise(); !errors.Is(err, ErrTooManyEvents) {
		t.Errorf("events: %v", err)
	}
	long := &Score{PPQ: PPQ, Voices: []Voice{{Notes: []Note{{Pitch: 60, Tick: 0, Dur: 2400 * PPQ}}}}} // 2400 quarters = 20 min at 120
	if err := long.Normalise(); err != nil {
		t.Errorf("exactly 20 minutes must pass: %v", err)
	}
	long.Voices[0].Notes[0].Dur++
	if err := long.Normalise(); !errors.Is(err, ErrTooLong) {
		t.Errorf("too long: %v", err)
	}
}

func TestScore_BarStarts(t *testing.T) {
	t.Parallel()
	s := &Score{PPQ: PPQ, Tempos: []Tempo{{0, 500000}}, BarTicks: []int64{0, 1920, 3840}, Bars: 3}
	got := s.BarStartsS()
	want := []float64{0, 2, 4}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Errorf("bar %d: %v want %v", i, got[i], want[i])
		}
	}
}
