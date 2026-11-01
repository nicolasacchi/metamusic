package servertest_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestRig_L3Options: L3 adds FixtureL3's rows on top of L2 (and so needs L2); the L3 services are built; Settle drains the queue
// with the no-op features step by default (no feature row) and derives every take and Song row under RealFeatures.
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
		takes, songs := l1Count(t, r, `SELECT COUNT(*) FROM take_features`), l1Count(t, r, `SELECT COUNT(*) FROM song_features`)
		if !o.RealFeatures {
			if rep.FeaturesComputed != 0 || takes+songs != 0 {
				t.Errorf("%s: FeaturesComputed = %d, %d feature rows, want none with the no-op step", name, rep.FeaturesComputed, takes+songs)
			}
			continue
		}
		if rep.FeaturesComputed == 0 || takes != l1Count(t, r, `SELECT COUNT(*) FROM tracks`) || songs < 14 {
			t.Errorf("%s: FeaturesComputed = %d, %d take rows, %d Song rows", name, rep.FeaturesComputed, takes, songs)
		}
	}
}
