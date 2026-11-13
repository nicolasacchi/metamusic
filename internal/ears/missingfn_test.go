package ears

import (
	"errors"
	"testing"
)

// MissingFn is asked on every call, so a Tier A that was missing at boot starts working when the probe recovers.
func TestMissingFn_TracksRecovery(t *testing.T) {
	t.Parallel()
	down := true
	s := New(Options{Analyzer: nilTier{}, MissingFn: func() error {
		if down {
			return errors.New("pending")
		}
		return nil
	}})
	if !s.tierAMissing() {
		t.Fatal("pending probe must report Tier A missing")
	}
	down = false
	if s.tierAMissing() {
		t.Fatal("recovered probe must clear Tier A missing")
	}
}

type nilTier struct{ TierA }
