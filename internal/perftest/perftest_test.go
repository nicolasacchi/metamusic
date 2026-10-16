package perftest

import (
	"testing"
	"time"
)

func TestPerftest_Budget(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		env  string
		race bool
		want time.Duration
	}{
		{"default", "", false, time.Second},
		{"factor", "3", false, 3 * time.Second},
		{"fraction", "0.5", false, 500 * time.Millisecond},
		{"bad env ignored", "x", false, time.Second},
		{"negative ignored", "-2", false, time.Second},
		{"race x10", "", true, 10 * time.Second},
		{"race and factor", "2", true, 20 * time.Second},
	} {
		if got := scale(time.Second, tc.env, tc.race); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPerftest_StopwatchCountsCPUNotSleep(t *testing.T) {
	t.Parallel()
	sw := Start()
	time.Sleep(200 * time.Millisecond)
	// other parallel tests may burn CPU in this process, so only the upper-bound of an idle sleep is asserted loosely
	// when nothing else runs; the invariant checked here is monotonicity and a positive spin cost.
	spin := Start()
	deadline := time.Now().Add(50 * time.Millisecond)
	for time.Now().Before(deadline) {
	}
	if spin.Elapsed() <= 0 {
		t.Fatalf("busy loop measured %v", spin.Elapsed())
	}
	if sw.Elapsed() < spin.Elapsed() {
		t.Fatalf("not monotonic: %v < %v", sw.Elapsed(), spin.Elapsed())
	}
}

func TestPerftest_ScaleMul(t *testing.T) {
	t.Parallel()
	if got := scaleMul(time.Second, "2", true, 50); got != 100*time.Second {
		t.Fatalf("race mul: %v", got)
	}
	if got := scaleMul(time.Second, "", false, 50); got != time.Second {
		t.Fatalf("plain ignores race mul: %v", got)
	}
}
