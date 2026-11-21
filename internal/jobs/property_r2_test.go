package jobs

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider/fake"
)

// TestProperty_FallbackNeverLosesAVerifiedResult (C399.13): over seeded timelines the app link dies and comes back at random, the
// worker dies mid-upload, the runner crashes, the provider purges its results and time jumps past every window; every take whose
// worker reached either channel completes exactly once, its uploads were received through a verified route, and nothing of a
// presigned URL or the bucket credentials reaches a row. The ledger invariants of the property suite run after every commit.
func TestProperty_FallbackNeverLosesAVerifiedResult(t *testing.T) {
	t.Parallel()
	var viaApp, viaBucket, mixed atomic.Int64
	t.Run("seeds", func(t *testing.T) {
		for seed := uint64(1); seed <= 8; seed++ {
			t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
				t.Parallel()
				e, w := newR2Env(t, seed, "within", func(o *fake.Options) {})
				e.setCaps(r2Caps)
				e.capsOn = true
				if _, err := e.cur.Step(e.ctx); err != nil {
					t.Fatal(err)
				}
				e.pass()
				jobs := e.seedBatch("fake:slow=10", 3, 120).Jobs
				for tick := 0; tick < 400 && !e.allTerminal(); tick++ {
					switch e.rng.IntN(12) {
					case 0:
						w.link.putDown.Store(true)
					case 1:
						w.link.putDown.Store(false)
					case 2, 5:
						w.link.dieAfter.Store(int64(1 + e.rng.IntN(4)))
					case 3:
						e.setCrash(faultPoints[e.rng.IntN(len(faultPoints))])
					case 4:
						e.r.clk.Advance(35 * time.Minute) // past the provider's retention
					}
					if e.rng.IntN(8) == 0 {
						w.bkt.FailNext(1+e.rng.IntN(2), 503) // absorbed by the worker's own retries or the next rescue
					}
					e.r.clk.Advance(time.Duration(5+e.rng.IntN(120)) * time.Second)
					if e.step() {
						e.r.clk.Advance(100 * time.Second)
						e.takeOver()
					}
					if tick%3 == 0 {
						e.pass()
					}
				}
				// the world heals: the app comes back and the timeline runs out
				w.link.putDown.Store(false)
				w.link.dieAfter.Store(0)
				e.drive(600, 30*time.Second, e.allTerminal)
				for _, j := range jobs {
					if got := e.r.state(j); got != StateCompleted {
						t.Errorf("seed %d: job %s ended %s (%v)", seed, j, got, e.r.events(j))
						continue
					}
					primary, fb := e.r.viaCounts(j)
					viaApp.Add(int64(primary))
					viaBucket.Add(int64(fb))
					if primary > 0 && fb > 0 {
						mixed.Add(1)
					}
					if primary+fb < 3 {
						t.Errorf("seed %d: job %s completed with %d received uploads", seed, j, primary+fb)
					}
				}
				if n := e.tracks(); n != len(jobs) {
					t.Errorf("seed %d: %d tracks for %d takes", seed, n, len(jobs))
				}
				if n := e.r.count(`SELECT COUNT(*) FROM uploads WHERE state IN ('received','consumed') AND via IS NULL`); n != 0 {
					t.Errorf("seed %d: %d received uploads without a route", seed, n)
				}
				e.noFallbackSecrets(w)
			})
		}
	})
	// the timelines must have exercised both routes, or the property says nothing
	if viaApp.Load() == 0 || viaBucket.Load() == 0 {
		t.Errorf("routes exercised: %d app uploads, %d bucket uploads", viaApp.Load(), viaBucket.Load())
	}
	t.Logf("%d app uploads, %d bucket uploads, %d takes over both routes", viaApp.Load(), viaBucket.Load(), mixed.Load())
}
