package dsp

import (
	"context"
	"errors"
	"testing"
)

func TestMeter_Budget(t *testing.T) {
	t.Parallel()
	m := &Meter{Max: 1000}
	for range 10 {
		if err := m.Add(100); err != nil {
			t.Fatalf("within budget: %v", err)
		}
	}
	if err := m.Add(1); !errors.Is(err, ErrBudget) {
		t.Errorf("past the budget: %v", err)
	}
	if m.Used() != 1001 {
		t.Errorf("used %d", m.Used())
	}
}

func TestMeter_Context(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	m := &Meter{Ctx: ctx}
	if err := m.Add(4000); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := m.Add(90); err != nil { // 4090: no poll boundary crossed yet
		t.Errorf("polled before 4096 units: %v", err)
	}
	if err := m.Add(10); !errors.Is(err, context.Canceled) {
		t.Errorf("after the boundary: %v", err)
	}
}

func TestUnits(t *testing.T) {
	t.Parallel()
	if got := Units.FFT(1024); got != 5*1024*10 {
		t.Errorf("FFT(1024) = %d", got)
	}
	if got := Units.PVFrame(4096); got != 3*Units.FFT(4096)+40*4096 {
		t.Errorf("PVFrame = %d", got)
	}
	if Units.ResampleSample(32) != 64 || Units.ConvBlock(3, 8192) != 3*8192*4+2*Units.FFT(8192) {
		t.Error("resample or convolution units")
	}
}
