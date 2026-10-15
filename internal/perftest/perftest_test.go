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
