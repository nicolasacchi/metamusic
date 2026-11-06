package ops

import "testing"

// d4SkipHeavy skips a numerically heavy test in the fast loop under the race detector (about 10x slower for pure float code
// with no goroutines to race): it runs in the non-race tiers (make dsp, the CI shards) and in full runs.
func d4SkipHeavy(t *testing.T) {
	t.Helper()
	if d4RaceEnabled && testing.Short() {
		t.Skip("heavy DSP test: skipped with -short under the race detector")
	}
}
