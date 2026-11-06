package ir_test

import "testing"

// skipHeavy skips a numerically heavy test in the fast loop under the race detector (about 10x slower for pure float code
// with no goroutines to race): it runs in the non-race tiers (make dsp, the CI shards) and in full runs.
func skipHeavy(t *testing.T) {
	t.Helper()
	if raceEnabled && testing.Short() {
		t.Skip("heavy DSP test: skipped with -short under the race detector")
	}
}
