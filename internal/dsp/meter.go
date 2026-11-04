package dsp

import (
	"context"
	"errors"
)

// ErrBudget is returned by Meter.Add once the work done exceeds the budget (TRANSFORM_BUDGET_EXCEEDED).
var ErrBudget = errors.New("dsp: work budget exceeded")

// Meter is the deterministic work meter of C203.6: every operator and primitive takes a *Meter (nil = unmetered in tests)
// and adds the units it is about to spend. One unit is one float64 multiply-add equivalent.
type Meter struct {
	Max int64 // the budget in units; 0 or less means unlimited
	// Ctx, when set, is polled every 4096 units: Add returns its error once it is done.
	Ctx  context.Context
	used int64
}

// Add records units of work and returns ErrBudget once the total exceeds Max. A nil Meter accepts everything.
func (m *Meter) Add(units int64) error {
	if m == nil {
		return nil
	}
	before := m.used
	m.used += units
	if m.Max > 0 && m.used > m.Max {
		return ErrBudget
	}
	if m.Ctx != nil && m.used/4096 != before/4096 {
		return m.Ctx.Err()
	}
	return nil
}

// Used is the total units added so far.
func (m *Meter) Used() int64 {
	if m == nil {
		return 0
	}
	return m.used
}

// Units is the table of work units per primitive (C203.6); one unit is one float64 multiply-add equivalent.
var Units unitTable

type unitTable struct{}

func log2i(n int) int64 {
	var l int64
	for p := 1; p < n; p <<= 1 {
		l++
	}
	return l
}

// FFT is the cost of one complex FFT of n points: 5 n log2 n.
func (unitTable) FFT(n int) int64 { return 5 * int64(n) * log2i(n) }

// PVFrame is the cost of one phase-vocoder frame of size n: 3 FFT(n) + 40 n.
func (u unitTable) PVFrame(n int) int64 { return 3*u.FFT(n) + 40*int64(n) }

// ResampleSample is the cost of one output sample of the resampler: 2 zeros.
func (unitTable) ResampleSample(zeros int) int64 { return 2 * int64(zeros) }

// ConvBlock is the cost of one block of ConvolveUP: partitions * fft * 4 + 2 FFT(fft).
func (u unitTable) ConvBlock(partitions, fft int) int64 {
	return int64(partitions)*int64(fft)*4 + 2*u.FFT(fft)
}
