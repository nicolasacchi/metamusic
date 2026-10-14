package jobs

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

// failureScenarios bring a rig to the moment before an interesting tick. They return what is needed to
// finish the batch afterwards.
type failureScenario struct {
	name  string
	build func(t *testing.T, mods ...rigOpt) (*rig, string) // returns the rig (right before the tick) and the batch id
}

func failureScenarios() []failureScenario {
	withAudit := func(r *rig) *rig { r.run.o.Audit = audit.NewStore(r.st, r.newID); return r }
	shapes := withOpts(func(o *RunnerOptions) {
		o.NeverRan = NeverRanShapes{TTLExpiryStates: []provider.RemoteState{provider.TimedOut}, CancelledWhileQueued: true}
		o.MeterKey = []byte("meter-key")
	})
	return []failureScenario{
		{"claim and submit", func(t *testing.T, mods ...rigOpt) (*rig, string) {
			r := withAudit(newRig(t, append([]rigOpt{shapes}, mods...)...))
			return r, r.seedBatch(batchSpec{jobs: 2}).Batch
		}},
		{"submit 5xx ghost", func(t *testing.T, mods ...rigOpt) (*rig, string) {
			r := withAudit(newRig(t, append([]rigOpt{shapes}, mods...)...))
			b := r.seedBatch(batchSpec{})
			r.submitErr(&httpErr{Code: "UPSTREAM", Status: 502})
			r.onSubmitOnce()
			return r, b.Batch
		}},
		{"submit 429", func(t *testing.T, mods ...rigOpt) (*rig, string) {
			r := withAudit(newRig(t, append([]rigOpt{shapes}, mods...)...))
			b := r.seedBatch(batchSpec{})
			r.submitErr(&httpErr{Code: "RATE_LIMITED", Status: 429})
			r.onSubmitOnce()
			return r, b.Batch
		}},
		{"submit 401", func(t *testing.T, mods ...rigOpt) (*rig, string) {
			r := withAudit(newRig(t, append([]rigOpt{shapes}, mods...)...))
			b := r.seedBatch(batchSpec{})
			r.submitErr(&httpErr{Code: "UNAUTHORIZED", Status: 401})
			r.onSubmitOnce()
			return r, b.Batch
		}},
		{"completion and ingest", func(t *testing.T, mods ...rigOpt) (*rig, string) {
			r, b, job, spec := generating(t, append([]rigOpt{shapes}, mods...)...)
			withAudit(r)
			out := r.deliver(spec, deliverOpts{})
			r.setStatus(r.refOf(job, 1), completed(out, 60000))
			r.advance(11 * time.Second)
			return r, b.Batch
		}},
		{"provider failure", func(t *testing.T, mods ...rigOpt) (*rig, string) {
			r, b, job, _ := generating(t, append([]rigOpt{shapes}, mods...)...)
			withAudit(r)
			r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(9000)})
			r.advance(11 * time.Second)
			return r, b.Batch
		}},
		{"cancel", func(t *testing.T, mods ...rigOpt) (*rig, string) {
			r, b, job, _ := generating(t, append([]rigOpt{shapes}, mods...)...)
			withAudit(r)
			r.cancelIntent(job)
			return r, b.Batch
		}},
		{"queue expiry", func(t *testing.T, mods ...rigOpt) (*rig, string) {
			r := withAudit(newRig(t, append([]rigOpt{shapes}, mods...)...))
			b := r.seedBatch(batchSpec{})
			job := b.Jobs[0]
			r.submitOne(job)
			r.advance(2*time.Hour + time.Second)
			r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.TimedOut, Raw: "TIMED_OUT", ExecutionMS: ms(0)})
			return r, b.Batch
		}},
		{"lost with evidence", func(t *testing.T, mods ...rigOpt) (*rig, string) {
			r, b, job, _ := generating(t, append([]rigOpt{shapes}, mods...)...)
			withAudit(r)
			r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.NotFound, Raw: "NOT_FOUND"})
			r.advance(11 * time.Second)
			return r, b.Batch
		}},
		{"requeue", func(t *testing.T, mods ...rigOpt) (*rig, string) {
			r, b, job, _ := generating(t, append([]rigOpt{shapes}, mods...)...)
			withAudit(r)
			r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Queued, Raw: "IN_QUEUE"})
			r.advance(11 * time.Second)
			return r, b.Batch
		}},
		{"artifact mismatch", func(t *testing.T, mods ...rigOpt) (*rig, string) {
			r, b, job, spec := generating(t, append([]rigOpt{shapes}, mods...)...)
			withAudit(r)
			out := r.deliver(spec, deliverOpts{lieSHA: map[string]bool{"audio.flac": true}})
			r.setStatus(r.refOf(job, 1), completed(out, 60000))
			r.advance(11 * time.Second)
			return r, b.Batch
		}},
		{"window close and fail remainder", func(t *testing.T, mods ...rigOpt) (*rig, string) {
			r := withAudit(newRig(t, append([]rigOpt{shapes}, mods...)...))
			b := r.seedBatch(batchSpec{jobs: 3, workersMax: 1})
			r.ghost(b.Jobs[0], "unknown", false)
			r.exec(`UPDATE jobs SET first_submitted_at = ?, queue_expiries = 7 WHERE id = ?`, "2026-10-12T00:00:00.000Z", b.Jobs[0])
			r.advance(4 * time.Hour)
			return r, b.Batch
		}},
		{"awaiting approval", func(t *testing.T, mods ...rigOpt) (*rig, string) {
			r := withAudit(newRig(t, append([]rigOpt{shapes}, mods...)...))
			b := r.seedBatch(batchSpec{jobs: 2, state: "awaiting_confirmation"})
			r.exec(`UPDATE approvals SET state='approved', via='web', decided_by='human:h1', decided_at=? WHERE batch_id=?`, "2026-10-14T12:00:00.000Z", b.Batch)
			r.exec(`UPDATE batches SET state='active' WHERE id=?`, b.Batch)
			r.cancelIntent(b.Jobs[1])
			return r, b.Batch
		}},
		{"running seen", func(t *testing.T, mods ...rigOpt) (*rig, string) {
			r := withAudit(newRig(t, append([]rigOpt{shapes}, mods...)...))
			b := r.seedBatch(batchSpec{})
			r.submitOne(b.Jobs[0])
			r.setStatus(r.refOf(b.Jobs[0], 1), running())
			r.advance(6 * time.Second)
			return r, b.Batch
		}},
		{"cancel confirmed", func(t *testing.T, mods ...rigOpt) (*rig, string) {
			r, b, job, _ := generating(t, append([]rigOpt{shapes}, mods...)...)
			withAudit(r)
			r.cancelIntent(job)
			r.step()
			r.setStatus(r.refOf(job, 1), provider.RemoteStatus{State: provider.Cancelled, Raw: "CANCELLED", ExecutionMS: ms(1000)})
			r.advance(3 * time.Second)
			return r, b.Batch
		}},
		{"deadline", func(t *testing.T, mods ...rigOpt) (*rig, string) {
			r, b, _, _ := generating(t, append([]rigOpt{shapes}, mods...)...)
			withAudit(r)
			r.advance(186*time.Second + 15*time.Second + 2*time.Minute + time.Second)
			return r, b.Batch
		}},
		{"batch finish", func(t *testing.T, mods ...rigOpt) (*rig, string) {
			r, b, job, spec := generating(t, append([]rigOpt{shapes, withOpts(func(o *RunnerOptions) { o.Meter = &fixedMeter{ready: true, seconds: 40} })}, mods...)...)
			withAudit(r)
			out := r.deliver(spec, deliverOpts{truncated: [2]bool{true, false}})
			r.setStatus(r.refOf(job, 1), completed(out, 60000))
			r.advance(11 * time.Second)
			r.step()
			r.advance(11 * time.Second)
			return r, b.Batch
		}},
		{"ingest stuck", func(t *testing.T, mods ...rigOpt) (*rig, string) {
			r, b, job, spec := generating(t, append([]rigOpt{shapes}, mods...)...)
			withAudit(r)
			r.ing.err = &IngestFailedError{Code: "IO"}
			out := r.deliver(spec, deliverOpts{})
			r.setStatus(r.refOf(job, 1), completed(out, 60000))
			r.advance(11 * time.Second)
			return r, b.Batch
		}},
		{"duplicate audio", func(t *testing.T, mods ...rigOpt) (*rig, string) {
			r, b, job, spec := generating(t, append([]rigOpt{shapes}, mods...)...)
			withAudit(r)
			r.ing.err = &DuplicateAudioError{TrackID: "t-1"}
			out := r.deliver(spec, deliverOpts{})
			r.setStatus(r.refOf(job, 1), completed(out, 60000))
			r.advance(11 * time.Second)
			return r, b.Batch
		}},
		{"unconfirmed cancel", func(t *testing.T, mods ...rigOpt) (*rig, string) {
			r, b, job, _ := generating(t, append([]rigOpt{shapes}, mods...)...)
			withAudit(r)
			r.cancelIntent(job)
			r.step()
			r.advance(6 * time.Minute)
			return r, b.Batch
		}},
		{"result while cancelling", func(t *testing.T, mods ...rigOpt) (*rig, string) {
			r, b, job, spec := generating(t, append([]rigOpt{shapes}, mods...)...)
			withAudit(r)
			r.cancelIntent(job)
			r.step()
			r.deliver(spec, deliverOpts{})
			r.advance(3 * time.Second)
			return r, b.Batch
		}},
		{"rejected batch", func(t *testing.T, mods ...rigOpt) (*rig, string) {
			r := withAudit(newRig(t, append([]rigOpt{shapes}, mods...)...))
			b := r.seedBatch(batchSpec{jobs: 2, state: "rejected"})
			return r, b.Batch
		}},
		{"ghost completes while queued", func(t *testing.T, mods ...rigOpt) (*rig, string) {
			r := withAudit(newRig(t, append([]rigOpt{shapes}, mods...)...))
			r.disarm()
			b := r.seedBatch(batchSpec{})
			ghost := r.ghost(b.Jobs[0], "unknown", true)
			r.exec(`UPDATE job_attempts SET ref = 'ghost-ref' WHERE id = ?`, ghost)
			r.setStatus("ghost-ref", provider.RemoteStatus{State: provider.Failed, Raw: "FAILED", ExecutionMS: ms(1000)})
			r.arm()
			return r, b.Batch
		}},
		{"refusal beside a ghost", func(t *testing.T, mods ...rigOpt) (*rig, string) {
			r := withAudit(newRig(t, append([]rigOpt{shapes}, mods...)...))
			b := r.seedBatch(batchSpec{})
			r.makeGhost(b.Jobs[0])
			r.arm()
			r.submitErr(&httpErr{Code: "UNAUTHORIZED", Status: 401})
			r.advance(2 * time.Minute)
			return r, b.Batch
		}},
		{"ingest failed with the winner running", func(t *testing.T, mods ...rigOpt) (*rig, string) {
			r, b, job, spec := generating(t, append([]rigOpt{shapes}, mods...)...)
			withAudit(r)
			r.ing.err = &IngestFailedError{Code: "IO"}
			r.deliver(spec, deliverOpts{})
			r.advance(11 * time.Second)
			r.step()
			r.setStatus(r.refOf(job, 1), completed(provider.Output{}, 60000))
			r.advance(11 * time.Second)
			return r, b.Batch
		}},
		{"ghost announces", func(t *testing.T, mods ...rigOpt) (*rig, string) {
			r := withAudit(newRig(t, append([]rigOpt{shapes}, mods...)...))
			b := r.seedBatch(batchSpec{})
			spec := r.makeGhost(b.Jobs[0])
			r.arm()
			r.start(spec, "late-ref")
			return r, b.Batch
		}},
	}
}

// pollHeavy names the scenarios that exercise the status-following paths of poll.go; the race-mode sweep fails
// every third call of them instead of every eighth.
var pollHeavy = map[string]bool{"lost with evidence": true, "requeue": true, "queue expiry": true, "provider failure": true, "running seen": true}

func (r *rig) onSubmitOnce() {
	inner := r.submitFn
	r.mu.Lock()
	defer r.mu.Unlock()
	first := true
	r.submitFn = func(spec provider.TakeSpec) (provider.Ref, error) {
		r.mu.Lock()
		f := first
		first = false
		r.mu.Unlock()
		if f {
			return inner(spec)
		}
		return provider.Ref{Backend: provider.BackendFake, ID: "ref-" + spec.AttemptID}, nil
	}
}

// completeAll drives the runner until the batch is finished, completing what the provider has not
// scripted, and checks the books.
func (r *rig) completeAll(batch string) {
	r.t.Helper()
	for i := 0; i < 300; i++ {
		r.completeSubmitted()
		_, err := r.run.Step(r.t.Context())
		if err != nil && i > 250 {
			r.t.Fatalf("step %d: %v", i, err)
		}
		_ = r.run.Drain(r.t.Context())
		switch r.str(`SELECT state FROM batches WHERE id = ?`, batch) {
		case "finished", "rejected", "expired":
			r.assertConsistent(batch)
			return
		}
		if i%3 == 2 {
			r.advance(40 * time.Minute) // lets ghosts and backoffs run out
		} else {
			r.advance(20 * time.Second)
		}
	}
	r.t.Fatalf("batch %s never finished: jobs %s", batch, r.str(`SELECT group_concat(state) FROM jobs WHERE batch_id = ?`, batch))
}

// TestRunner_EveryDatabaseCallMayFail fails each database call of one tick in turn (statements, begins and
// commits alike): the tick reports the error or carries on, nothing is left half done, and the next ticks
// bring the batch to its end with consistent books. This is what makes every `return err` of the runner
// reachable.
func TestRunner_EveryDatabaseCallMayFail(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("heavy: one rig per database call of every scenario")
	}
	for si, sc := range failureScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			// how many calls does the tick make?
			base, batch := sc.build(t)
			fz := base.freeze()
			ctl := &failCtl{rows: true, scans: true}
			r := fz.thaw(t, ctl)
			ctl.arm(0)
			_, _ = r.run.Step(t.Context())
			_ = r.run.Drain(t.Context())
			ctl.disarm()
			total := ctl.calls()
			if total < 3 {
				t.Fatalf("the tick made only %d database calls", total)
			}
			stride := int64(1)
			if raceEnabled {
				stride = 8 // (rows closing count as calls too) the race detector makes each rig several times slower; plain `go test` fails every call
				if pollHeavy[sc.name] {
					stride = 3 // the poll paths are where most storage-error branches live
				}
			}
			for k := 1 + int64(si)%stride; k <= total; k += stride { // each scenario starts at its own offset: scenarios share code, so the strides together cover more calls
				t.Run(fmt.Sprintf("call %d", k), func(t *testing.T) {
					t.Parallel()
					ctl := &failCtl{rows: true, scans: true}
					r := fz.thaw(t, ctl)
					ctl.arm(k)
					_, _ = r.run.Step(t.Context())
					_ = r.run.Drain(t.Context())
					ctl.disarm()
					r.assertSafe(batch)
					if k%5 == 0 || k == total {
						r.completeAll(batch) // and to the end of the batch
						return
					}
					for range 3 {
						r.completeSubmitted()
						_, _ = r.run.Step(t.Context())
						_ = r.run.Drain(t.Context())
						r.advance(20 * time.Second)
					}
					r.assertSafe(batch)
				})
			}
		})
	}
}

// assertSafe checks what must hold at every instant, whatever happened to the last tick: money is within
// what was reserved, and no terminal job has an attempt that can still spend.
func (r *rig) assertSafe(batch string) {
	r.t.Helper()
	if n := r.count(`SELECT COUNT(*) FROM job_attempts a JOIN jobs j ON j.id = a.job_id WHERE j.batch_id = ?
		AND a.state IN ('pending','submitted','unknown') AND j.state IN ('failed','cancelled','expired')`, batch); n != 0 {
		r.t.Errorf("%d live attempts belong to failed, cancelled or expired jobs: %s", n, r.str(`SELECT group_concat(j.id || ':' || j.state || '/' || a.n || ':' || a.state || ' ' || COALESCE(j.error_code,''), '; ') FROM job_attempts a JOIN jobs j ON j.id = a.job_id WHERE j.batch_id = ?`, batch))
	}
	reserved := r.ledger(batch, "reserve", "")
	used := r.sum(`SELECT SUM(micro_usd) FROM ledger WHERE batch_id = ? AND ((kind = 'settle' AND reason <> 'excess') OR kind = 'release')`, batch)
	if used > reserved {
		r.t.Errorf("settled+released %d exceeds reserved %d", used, reserved)
	}
	if r.ledger(batch, "settle", "excess") != 0 {
		r.t.Error("excess settle")
	}
}

// frozen is a scenario captured right before its tick: the database, the uploaded files and the
// provider's scripted answers, so each failing call can start from the same moment cheaply.
type frozen struct {
	db, data string
	now      time.Time
	ids      int64
	nBat     int
	status   map[string]provider.RemoteStatus
	submitFn func(provider.TakeSpec) (provider.Ref, error)
	audit    bool
	ingErr   error
	ingEnv   string
	ingN     int
	mods     []rigOpt
}

func (r *rig) freeze() *frozen {
	r.t.Helper()
	dir := r.t.TempDir()
	f := &frozen{db: filepath.Join(dir, "s.db"), data: filepath.Join(dir, "data"), now: r.clk.Now(), ids: r.ids.Load(), nBat: r.nBat,
		audit: r.run.o.Audit != nil, ingErr: r.ing.err, ingEnv: r.ing.env, ingN: r.ing.n, mods: r.mods}
	r.mu.Lock()
	f.status = maps.Clone(r.status)
	f.submitFn = r.submitFn
	r.mu.Unlock()
	// a consistent copy of the live database, WAL included
	if _, err := r.st.W.Exec(`VACUUM INTO '` + f.db + `'`); err != nil {
		r.t.Fatal(err)
	}
	if err := os.CopyFS(f.data, os.DirFS(r.dataDir())); err != nil {
		r.t.Fatal(err)
	}
	return f
}

func (r *rig) dataDir() string { return r.data.Name() }

// thaw builds a new rig at the frozen moment; ctl (if any) routes its store through failing connectors.
func (f *frozen) thaw(t *testing.T, ctl *failCtl) *rig {
	t.Helper()
	dir := t.TempDir()
	db, data := filepath.Join(dir, "s.db"), filepath.Join(dir, "data")
	if err := copyFile(f.db, db); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(data, os.DirFS(f.data)); err != nil {
		t.Fatal(err)
	}
	mods := append(slices.Clone(f.mods), withFailing(ctl))
	r := newRigAt(t, db, data, false, mods...)
	r.clk.Set(f.now)
	r.ids.Store(f.ids)
	r.nBat = f.nBat
	r.status = maps.Clone(f.status)
	r.submitFn = f.submitFn
	r.ing.err, r.ing.env, r.ing.n = f.ingErr, f.ingEnv, f.ingN
	if f.audit {
		r.run.o.Audit = audit.NewStore(r.st, r.newID)
	}
	return r
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o600)
}
