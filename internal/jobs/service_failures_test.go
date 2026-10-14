package jobs

import (
	"fmt"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// readScenario is a rig with finished, running and awaiting work, thawed with failing connectors.
func readFrozen(t *testing.T) (*frozen, seeded, seeded) {
	t.Helper()
	r := newRig(t, withOpts(func(o *RunnerOptions) { o.MeterKey = meterKey }))
	r.exec(`INSERT INTO env_fingerprints(env_id, engine, pkg_version, code_commit, weights_json, decoder, backend, gpu_class, image_digest, detail_json, first_seen_at)
		VALUES('env-1', 'yue2', '1', 'c', '{"a":"b"}', 'd', 'fake', 'A5000', 'build:x', '{"gpu_name":"g"}', ?)`, store.FormatTime(t0))
	r.ing.env = "env-1"
	done := r.seedBatch(batchSpec{principal: "agent:tok1"})
	spec := r.submitOne(done.Jobs[0])
	r.running(done.Jobs[0], "w1")
	out := r.deliver(spec, deliverOpts{})
	st := completed(out, 60000)
	st.WorkerID = "w1"
	r.setStatus(r.refOf(done.Jobs[0], 1), st)
	for range 4 {
		r.advance(11 * time.Second)
		r.step()
	}
	r.wantState(done.Jobs[0], StateCompleted)
	r.exec(`INSERT INTO worker_meter(worker_key, started_at, first_seen_at, last_seen_at, uptime_s, status_last) VALUES(?,?,?,?,60,'RUNNING')`,
		workerKey(meterKey, "w1"), store.FormatTime(t0.Add(-time.Minute)), store.FormatTime(t0), store.FormatTime(t0))
	live := r.seedBatch(batchSpec{jobs: 2, principal: "agent:tok1"})
	r.submitOne(live.Jobs[0])
	r.exec(`INSERT INTO provider_trips(id, backend, tripped_at, reason, details_json, actions_json) VALUES('trip-1', 'fake', ?, 'stuck_running', '{}', '[]')`, store.FormatTime(t0))
	r.exec(`INSERT INTO audit_log(id, at, principal_kind, principal_id, principal_name, transport, client, op, method, args_sha256, status, cost_micro_usd, idempotency, created_ids_json, duration_ms)
		VALUES('a1', '2026-10-14T12:00:05Z', 'system', 'system:reconciler', 'reconciler', 'runner', '', 'reconciler:purge_queue', 'SYSTEM', 'x', 200, 0, 'none', '[]', 0)`)
	return r.freeze(), done, live
}

// TestService_EveryDatabaseCallMayFail runs every read and cancel of the services with each of its database
// calls failing in turn: the answer is an error (never a panic or a half answer), and the next call works.
func TestService_EveryDatabaseCallMayFail(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("heavy: one rig per database call")
	}
	fz, done, live := readFrozen(t)
	type call struct {
		name string
		run  func(r *rig, svc *Service, adm *ProviderAdmin) error
	}
	yes := false
	calls := []call{
		{"ListJobs", func(r *rig, s *Service, _ *ProviderAdmin) error {
			_, err := s.ListJobs(t.Context(), humanP, JobQuery{TrackID: "x", Since: t0, Limit: 1})
			return err
		}},
		{"ListJobs2", func(r *rig, s *Service, _ *ProviderAdmin) error {
			_, err := s.ListJobs(t.Context(), humanP, JobQuery{Limit: 2})
			return err
		}},
		{"Job", func(r *rig, s *Service, _ *ProviderAdmin) error {
			_, err := s.Job(t.Context(), humanP, done.Jobs[0])
			return err
		}},
		{"Job live", func(r *rig, s *Service, _ *ProviderAdmin) error {
			_, err := s.Job(t.Context(), humanP, live.Jobs[0])
			return err
		}},
		{"ListBatches", func(r *rig, s *Service, _ *ProviderAdmin) error {
			_, err := s.ListBatches(t.Context(), humanP, BatchQuery{Until: t0.Add(time.Hour), Limit: 1})
			return err
		}},
		{"Batch", func(r *rig, s *Service, _ *ProviderAdmin) error {
			_, err := s.Batch(t.Context(), humanP, live.Batch)
			return err
		}},
		{"CancelJob", func(r *rig, s *Service, _ *ProviderAdmin) error {
			_, err := s.CancelJob(t.Context(), humanP, live.Jobs[1], "x")
			return err
		}},
		{"CancelBatch", func(r *rig, s *Service, _ *ProviderAdmin) error {
			_, err := s.CancelBatch(t.Context(), humanP, live.Batch, "x")
			return err
		}},
		{"Wait", func(r *rig, s *Service, _ *ProviderAdmin) error {
			_, err := s.Wait(t.Context(), humanP, done.Jobs[0], StateQueued, 0)
			return err
		}},
		{"Status", func(r *rig, _ *Service, a *ProviderAdmin) error { _, err := a.Status(t.Context(), humanP); return err }},
		{"Update", func(r *rig, _ *Service, a *ProviderAdmin) error {
			_, err := a.Update(t.Context(), humanP, UpdateIn{Armed: &yes, ResetTrip: true, Reason: "x"})
			return err
		}},
		{"Meter", func(r *rig, _ *Service, _ *ProviderAdmin) error {
			_, _, err := r.recon().OverheadSeconds(t.Context(), r.st.R, done.Batch)
			return err
		}},
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			build := func(t *testing.T, ctl *failCtl) (*rig, *Service, *ProviderAdmin) {
				r := fz.thaw(t, ctl)
				rec := r.recon()
				adm := r.admin(provider.ModeFake, true, func(o *ProviderOptions) { o.Sweep = rec.Snapshot })
				return r, r.service(t.Context()), adm
			}
			count := &failCtl{rows: true, scans: true}
			r, svc, adm := build(t, count)
			count.arm(0)
			if err := c.run(r, svc, adm); err != nil {
				t.Fatalf("the clean call: %v", err)
			}
			total := count.calls()
			count.disarm()
			if total < 1 {
				t.Fatalf("the call made %d database calls", total)
			}
			for k := int64(1); k <= total; k++ {
				t.Run(fmt.Sprintf("call %d", k), func(t *testing.T) {
					t.Parallel()
					ctl := &failCtl{rows: true, scans: true}
					r, svc, adm := build(t, ctl)
					ctl.arm(k)
					_ = c.run(r, svc, adm) // an error or a result: never a panic
					ctl.disarm()
					if err := c.run(r, svc, adm); err != nil {
						t.Errorf("the call after the failure: %v", err)
					}
				})
			}
		})
	}
}
