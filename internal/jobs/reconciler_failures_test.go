package jobs

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// reconScenario brings a rig to the moment before a reconciler pass. script reapplies the provider's
// scripted answers on every rig thawed from it, prime the reconciler's in-memory observations.
type reconScenario struct {
	name   string
	build  func(t *testing.T) (r *rig, batch string)
	script func(r *rig)
	prime  func(rec *Reconciler)
}

func reconScenarios() []reconScenario {
	meterKeyOpt := withOpts(func(o *RunnerOptions) { o.MeterKey = meterKey })
	hours := map[time.Time][2]money.MicroUSD{at(13, 0): {25000, 25000}}
	return []reconScenario{
		{name: "trip on stuck workers",
			build: func(t *testing.T) (*rig, string) {
				r := newRig(t, meterKeyOpt)
				s := r.seedBatch(batchSpec{jobs: 2})
				r.submitOne(s.Jobs[0])
				return r, s.Batch
			},
			script: func(r *rig) {
				r.sweep(worker("w1", "RUNNING", upt(130), t0), worker("w2", "RUNNING", upt(130), t0))
			},
			prime: func(rec *Reconciler) { rec.stuckSince = t0.Add(-5 * time.Minute) }},
		{name: "approval expiry and quote",
			build: func(t *testing.T) (*rig, string) {
				r := newRig(t)
				s := r.seedBatch(batchSpec{jobs: 2, state: "awaiting_confirmation"})
				r.advance(25 * time.Hour)
				return r, s.Batch
			}},
		{name: "billing window",
			build: func(t *testing.T) (*rig, string) {
				r := newRig(t)
				return r, r.billBatch(at(12, 50), at(13, 20), 20000, at(21, 0)).Batch
			},
			script: func(r *rig) { r.hours(hours) }},
		{name: "billing window exceeding the reserve",
			build: func(t *testing.T) (*rig, string) {
				r := newRig(t)
				return r, r.billBatch(at(12, 50), at(13, 20), 20000, at(21, 0)).Batch
			},
			script: func(r *rig) {
				r.hours(map[time.Time][2]money.MicroUSD{at(13, 0): {70000, 60000}, at(15, 0): {1000, 1000}})
			}},
		{name: "late adjustment",
			build: func(t *testing.T) (*rig, string) {
				r := newRig(t)
				s := r.billBatch(at(12, 50), at(13, 20), 20000, at(20, 30))
				r.hours(hours)
				r.mustPass(r.recon())
				r.advance(20 * time.Minute)
				return r, s.Batch
			},
			script: func(r *rig) { r.hours(map[time.Time][2]money.MicroUSD{at(13, 0): {60000, 60000}}) }},
		{name: "hold release",
			build: func(t *testing.T) (*rig, string) {
				r := newRig(t)
				r.caps.Billing = "none"
				s := r.billBatch(at(10, 0), at(10, 30), 20000, at(10, 40))
				r.clk.Set(at(10, 30).Add(49 * time.Hour))
				return r, s.Batch
			},
			script: func(r *rig) { r.caps.Billing = "none" }},
		{name: "adopt a ghost",
			build: func(t *testing.T) (*rig, string) {
				c := newCrash(FaultAfterClaimCommit)
				r := newRig(t, withOpts(func(o *RunnerOptions) { o.Fault = c.fault }))
				s := r.seedBatch(batchSpec{})
				_, _ = r.run.Step(t.Context())
				r.advance(2 * LeaseLength)
				r.run.o.Fault = nil
				return r, s.Batch
			}},
		{name: "workers, drift and meter",
			build: func(t *testing.T) (*rig, string) {
				r := newRig(t, meterKeyOpt)
				s := r.seedBatch(batchSpec{})
				job := s.Jobs[0]
				r.submitOne(job)
				r.running(job, "w1")
				r.exec(`UPDATE batches SET state = 'finished', finished_at = ? WHERE id = ?`, store.FormatTime(r.clk.Now().Add(-2*time.Minute)), s.Batch)
				return r, s.Batch
			},
			script: func(r *rig) {
				idle := 90
				ep := providertestEndpoint(&idle)
				r.mu.Lock()
				r.sweepFn = func() (provider.SweepReport, error) {
					return provider.SweepReport{Workers: []provider.Worker{worker("w1", "RUNNING", upt(9000), t0)}, Endpoint: &ep,
						Drift: []provider.Drift{{Field: "flashboot", Want: "FLASHBOOT", Got: "NONE"}}, ObservedAt: r.clk.Now()}, nil
				}
				r.mu.Unlock()
			}},
		{name: "trip cancels live attempts",
			build: func(t *testing.T) (*rig, string) {
				r := newRig(t, meterKeyOpt)
				s := r.seedBatch(batchSpec{jobs: 2})
				r.submitOne(s.Jobs[0])
				r.exec(`INSERT INTO provider_trips(id, backend, tripped_at, reason, details_json, actions_json) VALUES('trip-x', 'fake', ?, 'crash_loop', '{}', '[]')`, store.FormatTime(r.clk.Now()))
				return r, s.Batch
			}},
	}
}

// TestReconciler_EveryDatabaseCallMayFail fails each database call of one reconciler pass in turn: the pass
// reports the error or carries on, nothing is half done (no money beyond the reserves, no live attempt on a
// terminal job), and a clean pass afterwards succeeds. This makes every `return err` of the reconciler reachable.
func TestReconciler_EveryDatabaseCallMayFail(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("heavy: one rig per database call of every scenario")
	}
	for _, sc := range reconScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			base, batch := sc.build(t)
			fz := base.freeze()
			run := func(t *testing.T, ctl *failCtl, k int64) (*rig, *Reconciler, error) {
				r := fz.thaw(t, ctl)
				if sc.script != nil {
					sc.script(r)
				}
				rec := r.recon()
				if sc.prime != nil {
					sc.prime(rec)
				}
				r.hold(r.run)
				ctl.arm(k)
				err := rec.Pass(t.Context())
				ctl.disarm()
				return r, rec, err
			}
			count := &failCtl{}
			_, _, err := run(t, count, 0)
			if err != nil {
				t.Fatalf("the clean pass: %v", err)
			}
			total := count.calls()
			if total < 3 {
				t.Fatalf("the pass made only %d database calls", total)
			}
			stride := int64(1)
			if raceEnabled {
				stride = 4
			}
			for k := int64(1); k <= total; k += stride {
				t.Run(fmt.Sprintf("call %d", k), func(t *testing.T) {
					t.Parallel()
					r, rec, _ := run(t, &failCtl{}, k)
					r.assertSafe(batch)
					r.hold(r.run)
					if err := rec.Pass(t.Context()); err != nil && !errors.Is(err, store.ErrLeaseLost) {
						t.Errorf("the pass after a failed call: %v", err)
					}
					r.assertSafe(batch)
				})
			}
		})
	}
}
