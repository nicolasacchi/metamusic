package archtest

import (
	"strings"
	"testing"
)

// TestArchitectureDoc_M6N pins the M6N section X1 wrote and the four documented parity exceptions of the track (C389.3), so a later
// edit cannot drop them silently.
func TestArchitectureDoc_M6N(t *testing.T) {
	t.Parallel()
	sec := docSection(t, "Night shift, mandates, Morning Crate and Critic Bench (M6N)")
	for _, want := range []string{"off until the Owner creates a mandate", "estimate ceiling", "via = mandate", "Blind", "Wilson", "OWNER_ONLY", "HUMAN_ONLY", "FORBIDDEN_SCOPE", "TestAcceptance_M6N_NightFeedsMorningCrate"} {
		if !strings.Contains(sec, want) {
			t.Errorf("M6N section lost %q", want)
		}
	}
	ex := docSection(t, "Documented parity exceptions")
	for _, want := range []string{"create_mandate", "update_night_shift", "submit_morning_crate_events", "reveal_morning_crate", "exclude:human-only", "exclude:person-only"} {
		if !strings.Contains(ex, want) {
			t.Errorf("parity exceptions lost %q", want)
		}
	}
}
