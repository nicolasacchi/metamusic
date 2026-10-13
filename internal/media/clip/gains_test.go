package clip_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/media/clip"
)

func TestMatchGains(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		a, b   float64
		ga, gb int
		target *float64
		status string
	}{
		{"louder a is attenuated", -10, -20, -1000, 0, f(-20), "matched"},
		{"louder b is attenuated", -20, -10, 0, -1000, f(-20), "matched"},
		{"equal", -20, -20, 0, 0, f(-20), "matched"},
		{"limited at -24 dB", -10, -40, -2400, 0, f(-40), "limited"},
		{"window silent", -65, -14, 0, 0, nil, "window_silent"},
		{"silence threshold is inclusive", -14, -60, 0, 0, nil, "window_silent"},
		{"fractional gain rounds to centi-dB", -12.634, -20, -737, 0, f(-20), "matched"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ga, gb, target, status := clip.MatchGains(clip.Measure{IntegratedLUFS: tc.a}, clip.Measure{IntegratedLUFS: tc.b})
			if ga != tc.ga || gb != tc.gb || status != tc.status {
				t.Errorf("= (%d, %d, %q), want (%d, %d, %q)", ga, gb, status, tc.ga, tc.gb, tc.status)
			}
			switch {
			case tc.target == nil && target != nil:
				t.Errorf("target = %v, want nil", *target)
			case tc.target != nil && (target == nil || *target != *tc.target):
				t.Errorf("target = %v, want %v", target, *tc.target)
			}
		})
	}
}

func TestTargetGain(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		i, tgt float64
		gcdb   int
		status string
	}{
		{"attenuate to a quieter target", -20, -30, -1000, "matched"},
		{"never boost", -20, -14, 0, "limited"},
		{"cut is capped", -12, -40, -2400, "limited"},
		{"cut within the cap", -12, -20, -800, "matched"},
		{"silent window", -65, -30, 0, "window_silent"},
		{"already at target", -20, -20, 0, "matched"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g, status := clip.TargetGain(clip.Measure{IntegratedLUFS: tc.i}, tc.tgt)
			if g != tc.gcdb || status != tc.status {
				t.Errorf("= (%d, %q), want (%d, %q)", g, status, tc.gcdb, tc.status)
			}
		})
	}
}
