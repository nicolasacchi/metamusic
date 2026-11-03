package dsp

import "errors"

// ErrBudget is returned by Meter.Add once the work done exceeds the budget (TRANSFORM_BUDGET_EXCEEDED).
var ErrBudget = errors.New("dsp: work budget exceeded")

// Meter is the deterministic work meter of C203.6: every operator and primitive takes a *Meter (nil = unmetered in tests)
// and adds the units it is about to spend. One unit is one float64 multiply-add equivalent.
type Meter struct {
	Max  int64 // the budget in units; 0 or less means unlimited
	used int64
}

// Add records units of work and returns ErrBudget once the total exceeds Max. A nil Meter accepts everything.
func (m *Meter) Add(units int64) error {
	if m == nil {
		return nil
	}
	m.used += units
	if m.Max > 0 && m.used > m.Max {
		return ErrBudget
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

// STUB(T1-D1): polling the process context every 4096 units and dsp.Units, the table of units per primitive (C203.6).
