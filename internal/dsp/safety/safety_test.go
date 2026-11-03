package safety

import "testing"

// TestFinish_CeilingClamped pins the ceiling seam of C225.5: 0 means -1.0 dBTP, master may ask for as low as -3.0, and no kind
// can raise it above -1.0.
func TestFinish_CeilingClamped(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ in, want float64 }{
		{0, -1.0}, {-1.0, -1.0}, {-2.0, -2.0}, {-3.0, -3.0}, {-5.0, -3.0}, {-0.5, -1.0}, {0.5, -1.0}, {6, -1.0},
	} {
		if got := (FinishIn{CeilingDBTP: c.in}).Ceiling(); got != c.want {
			t.Errorf("ceiling(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
