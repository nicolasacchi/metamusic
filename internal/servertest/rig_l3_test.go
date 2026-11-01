package servertest_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestRig_L3Options: L3 adds FixtureL3's rows on top of L2 (and so needs L2); the L3 services are built; Settle drains the queue
// with the no-op features step by default and with the wired step under RealFeatures (a stub that reads as "nothing to do" until
// lane FC2), computing no feature row either way.
func TestRig_L3Options(t *testing.T) {
	t.Parallel()
	for name, o := range map[string]servertest.Options{
		"default":       {Fixture: true, M2c: true, M3: true, L1: true, L2: true, L3: true},
		"real features": {Fixture: true, M2c: true, M3: true, L1: true, L2: true, L3: true, RealFeatures: true},
	} {
		r := servertest.New(t, o)
		s := r.Services
		if s.Features == nil || s.Sequence == nil || s.NextActions == nil || s.Atlas == nil {
			t.Fatalf("%s: every L3 service must be built", name)
		}
		if n := l1Count(t, r, `SELECT COUNT(*) FROM songs`); n == 0 {
			t.Errorf("%s: FixtureL3 added no Songs", name)
		}
		rep := r.Settle(t)
		if rep.FeaturesComputed != 0 {
			t.Errorf("%s: FeaturesComputed = %d, want 0 until lane FC2", name, rep.FeaturesComputed)
		}
		if n := l1Count(t, r, `SELECT COUNT(*) FROM take_features`) + l1Count(t, r, `SELECT COUNT(*) FROM song_features`); n != 0 {
			t.Errorf("%s: %d feature rows after Settle, want 0 until lane FC2", name, n)
		}
	}
}
