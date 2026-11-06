package generation_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/generation"
)

func TestRepaintAB_Window_Table(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name             string
		from, to, dur    int
		wantFrom, wantTo int
		truncated        bool
	}{
		{"short range at the start widens to 0..30", 80, 140, 1000, 0, 300, false},
		{"mid range is centred", 500, 560, 1000, 380, 680, false},
		{"range at the very start clamps", 0, 50, 1000, 0, 300, false},
		{"range at the very end clamps", 950, 1000, 1000, 700, 1000, false},
		{"40 s range is itself", 200, 600, 1000, 200, 600, false},
		{"exactly 30 s is itself", 200, 500, 1000, 200, 500, false},
		{"exactly 45 s is itself", 100, 550, 1000, 100, 550, false},
		{"80 s range is cut to its first 45 s", 100, 900, 1000, 100, 550, true},
		{"46 s range is truncated", 0, 460, 1000, 0, 450, true},
		{"take shorter than the window gives the whole take", 40, 80, 200, 0, 200, false},
		{"odd midpoint stays on the grid", 501, 560, 1000, 380, 680, false},
		{"reversed range collapses to its start", 600, 100, 1000, 450, 750, false},
		{"range past the end is clamped", 900, 1200, 1000, 700, 1000, false},
	} {
		got := generation.RepaintABWindow(c.from, c.to, c.dur)
		if got.FromDS != c.wantFrom || got.ToDS != c.wantTo || got.Truncated != c.truncated {
			t.Errorf("%s: RepaintABWindow(%d,%d,%d) = %+v, want %d..%d truncated=%v", c.name, c.from, c.to, c.dur, got, c.wantFrom, c.wantTo, c.truncated)
		}
	}
}
