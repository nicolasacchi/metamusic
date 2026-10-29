package recordview

import (
	"slices"
	"testing"
)

// TestEnergy_Curve: the gain is added, the floor is -70.0, values have one decimal, and a long series is bucket-mean
// downsampled to at most 120 points (bucket = ceil(len / 120)).
func TestEnergy_Curve(t *testing.T) {
	t.Parallel()
	if got := curve(nil, 3); got == nil || len(got) != 0 {
		t.Errorf("no analysis = %v, want []", got)
	}
	if got, want := curve([]float64{-14.0, -13.04, -70.0, -69.9}, -2.25), []float64{-16.3, -15.3, -70, -70}; !slices.Equal(got, want) {
		t.Errorf("curve = %v, want %v", got, want)
	}
	long := make([]float64, 300) // bucket 3: 100 points
	for i := range long {
		long[i] = -20 + float64(i%3)
	}
	got := curve(long, 0)
	if len(got) != 100 || got[0] != -19 || got[99] != -19 {
		t.Errorf("downsampled: %d points, first %v last %v", len(got), got[0], got[len(got)-1])
	}
	if n := len(curve(make([]float64, 121), 0)); n != 61 { // bucket 2 -> ceil(121/2) = 61 points
		t.Errorf("121 values -> %d points, want 61", n)
	}
	if n := len(curve(make([]float64, 120), 0)); n != 120 {
		t.Errorf("120 values -> %d points, want 120", n)
	}
	if n := len(curve(make([]float64, 10_000), 0)); n > energyPoints {
		t.Errorf("10 000 values -> %d points, want at most %d", n, energyPoints)
	}
}
