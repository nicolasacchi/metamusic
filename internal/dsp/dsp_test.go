package dsp

import (
	"errors"
	"testing"
)

func TestMeter(t *testing.T) {
	t.Parallel()
	var nilMeter *Meter
	if err := nilMeter.Add(1 << 40); err != nil || nilMeter.Used() != 0 {
		t.Error("a nil meter must accept everything")
	}
	m := &Meter{Max: 100}
	if err := m.Add(60); err != nil {
		t.Fatal(err)
	}
	if err := m.Add(41); !errors.Is(err, ErrBudget) {
		t.Errorf("Add past the budget = %v, want ErrBudget", err)
	}
	if err := (&Meter{}).Add(1 << 40); err != nil {
		t.Errorf("an unlimited meter refused work: %v", err)
	}
}

func TestNextPow2(t *testing.T) {
	t.Parallel()
	for in, want := range map[int]int{0: 1, 1: 1, 2: 2, 3: 4, 4096: 4096, 4097: 8192} {
		if got := NextPow2(in); got != want {
			t.Errorf("NextPow2(%d) = %d, want %d", in, got, want)
		}
	}
}
