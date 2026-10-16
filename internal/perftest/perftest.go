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

// BudgetRace is Budget for a path whose measured -race cost is not the usual 10x: raceMul replaces the 10 (a
// SQLite-heavy fold over 10 000 rows measured 36x: 83 ms plain, 3 s under -race).
func BudgetRace(tb testing.TB, d time.Duration, raceMul float64) time.Duration {
	tb.Helper()
	if testing.Short() {
		tb.Skip("perf test skipped under -short")
	}
	return scaleMul(d, os.Getenv("MM_PERF_FACTOR"), raceEnabled, raceMul)
}

func scale(d time.Duration, env string, race bool) time.Duration { return scaleMul(d, env, race, 10) }

func scaleMul(d time.Duration, env string, race bool, raceMul float64) time.Duration {
	f := 1.0
	if v, err := strconv.ParseFloat(env, 64); err == nil && v > 0 {
		f = v
	}
	if race {
		f *= raceMul
	}
	return time.Duration(float64(d) * f)
}

// Stopwatch measures the process CPU time (user+system, all threads) between Start and Elapsed. A budget is a cost
// budget, not a latency SLO: wall time on a shared machine also counts the scheduler's delay (a loaded host measured a
// 10 000-subject recompute at 115 s against 50 s, and 25 s alone), CPU time does not. Only meaningful for a test that
// runs serially (no t.Parallel) in its package, since CPU of concurrent tests in the process is included.
type Stopwatch struct{ t0 time.Duration }

// Start begins a measurement.
func Start() Stopwatch { return Stopwatch{cpuNow()} }

// Elapsed is the CPU time consumed since Start.
func (s Stopwatch) Elapsed() time.Duration { return cpuNow() - s.t0 }
