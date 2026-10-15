// Package perftest is the test-only budget helper of the performance targets (C101.4). It must never be linked into the
// binary (cmd/metamusic/nohelpers_test.go lists it). A perf test never hard-codes a wall-clock literal: it asks Budget.
package perftest

import (
	"os"
	"strconv"
	"testing"
	"time"
)

// Budget returns d scaled for the machine and the instrumentation: MM_PERF_FACTOR (default 1, raised on shared
// runners) times 10 under -race, because the transpiled SQLite stack costs about 10x there. It skips the calling test
// under -short so `make check` stays fast.
func Budget(tb testing.TB, d time.Duration) time.Duration {
	tb.Helper()
	if testing.Short() {
		tb.Skip("perf test skipped under -short")
	}
	return scale(d, os.Getenv("MM_PERF_FACTOR"), raceEnabled)
}

func scale(d time.Duration, env string, race bool) time.Duration {
	f := 1.0
	if v, err := strconv.ParseFloat(env, 64); err == nil && v > 0 {
		f = v
	}
	if race {
		f *= 10
	}
	return time.Duration(float64(d) * f)
}
