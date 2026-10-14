package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/engine/registry"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestSkipStale(t *testing.T) {
	t.Parallel()
	if skipStale(errStale) != nil || skipStale(nil) != nil {
		t.Error("stale and nil are not errors")
	}
	boom := errors.New("boom")
	if !errors.Is(skipStale(fmt.Errorf("wrapped: %w", boom)), boom) {
		t.Error("other errors pass through")
	}
}

func TestNewRunnerDefaults(t *testing.T) {
	t.Parallel()
	r := NewRunner(RunnerOptions{})
	if r.o.Clock == nil || r.o.Log == nil || r.o.Notify == nil || r.o.NewID == nil || r.o.IngestWorkers != 2 {
		t.Errorf("defaults %+v", r.o)
	}
	if id := r.o.NewID(); len(id) != 36 {
		t.Errorf("id %q", id)
	}
	r.Wake()
	r.Wake() // never blocks
	if r.Holding() {
		t.Error("holding without a lease")
	}
}

func TestRowsHelpers(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	if _, err := loadJob(t.Context(), r.st.R, "no-such-job"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("loadJob: %v", err)
	}
	if parseT(sql.NullString{}) != nil || parseT(sql.NullString{Valid: true, String: "not a time"}) != nil {
		t.Error("parseT")
	}
	if !mustT("garbage").IsZero() {
		t.Error("mustT")
	}
	if got := liveAttempts([]attRec{{State: "done"}, {State: "unknown"}, {State: "rejected"}, {State: "pending"}}); len(got) != 2 {
		t.Errorf("live %v", got)
	}
}

func TestHeartbeatLoop(t *testing.T) {
	t.Parallel()
	ctl := &failCtl{}
	r := newRig(t, withFailing(ctl))
	r.step() // the lease
	ctx, cancel := contextWithCancel(t)
	defer cancel()
	lost := make(chan struct{})
	done := make(chan struct{})
	go func() { r.run.heartbeatLoop(ctx, lost); close(done) }()
	waitFor(t, func() bool { return r.clk.Pending() >= 1 })
	// a database error is logged and the loop carries on
	ctl.arm(1)
	r.advance(30 * time.Second)
	waitFor(t, func() bool { return ctl.fired.Load() })
	ctl.disarm()
	waitFor(t, func() bool { return r.clk.Pending() >= 1 })
	before := r.str(`SELECT heartbeat_at FROM runner_lease WHERE id = 1`)
	r.advance(30 * time.Second)
	waitFor(t, func() bool { return r.str(`SELECT heartbeat_at FROM runner_lease WHERE id = 1`) != before })
	// losing the lease ends it
	r.exec(`UPDATE runner_lease SET owner = 'someone-else' WHERE id = 1`)
	waitFor(t, func() bool { return r.clk.Pending() >= 1 })
	r.advance(30 * time.Second)
	select {
	case <-lost:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop did not report the lost lease")
	}
	<-done
}

func TestWaitDatabaseFailures(t *testing.T) {
	t.Parallel()
	ctl := &failCtl{}
	r := newRig(t, withFailing(ctl))
	r.disarm()
	job := r.seedBatch(batchSpec{}).Jobs[0]
	s := r.service(context.Background())
	for k := int64(1); k <= 1; k++ {
		ctl.arm(k)
		_, err := s.Wait(t.Context(), auth.Principal{}, job, StateQueued, 0)
		ctl.disarm()
		if !ctl.fired.Load() || !errors.Is(err, errInjected) {
			t.Errorf("call %d: fired %v, err %v", k, ctl.fired.Load(), err)
		}
	}
	// the re-read after the timeout fails too
	ch := r.waitAsync(t.Context(), s, job, StateQueued, 10*time.Second)
	r.timersUp()
	ctl.arm(1)
	r.advance(11 * time.Second)
	if o := <-ch; !errors.Is(o.err, errInjected) {
		t.Errorf("%+v", o)
	}
	ctl.disarm()
	_ = store.FormatTime
}

func TestReadOutputFailures(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	if _, err := r.run.readOutput("01900000-0000-7000-8000-0000000000ff"); err == nil {
		t.Error("a missing upload file")
	}
	if _, err := r.run.requiredArtifacts(t.Context(), r.st.R, &jobRec{JobRow: JobRow{RecipeID: "no-such-recipe"}, Engine: "yue2"}); err == nil {
		t.Error("a missing recipe")
	}
	if _, err := r.run.requiredArtifacts(t.Context(), r.st.R, &jobRec{Engine: "nope"}); err == nil {
		t.Error("a missing engine")
	}
}

func TestRowsCarryTheParent(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	parent := r.freshTrack()
	r.disarm()
	b := r.seedBatch(batchSpec{source: parent})
	j := r.job(b.Jobs[0])
	if j.SourceTrackID == nil || *j.SourceTrackID != parent {
		t.Errorf("source %v", j.SourceTrackID)
	}
}

func TestWaitReturnsTheFinishedJob(t *testing.T) {
	t.Parallel()
	r, _, job, spec := generating(t)
	out := r.deliver(spec, deliverOpts{})
	r.setStatus(r.refOf(job, 1), completed(out, 60000))
	r.advance(11 * time.Second)
	r.step()
	res, err := r.service(context.Background()).Wait(t.Context(), auth.Principal{}, job, StateGenerating, 0)
	if err != nil || !res.Changed || res.Job.State != StateCompleted || res.Job.Outcome == nil || *res.Job.Outcome != "clean" || res.Job.TrackID == nil {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestEventTextAndSizeLimits(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.disarm()
	job := r.seedBatch(batchSpec{}).Jobs[0]
	r.step() // takes the lease
	write := func(ev Event) error {
		return r.run.tx(t.Context(), func(tx *sql.Tx) error { return insertEvent(t.Context(), tx, r.run.fence(), ev) })
	}
	// 3-byte runes: the 500-byte cut lands inside one and must back off to a rune boundary
	if err := write(Event{JobID: job, Name: EvStatus, Detail: map[string]any{"text": strings.Repeat("€", 400)}}); err != nil {
		t.Fatal(err)
	}
	var d string
	if err := r.st.R.QueryRow(`SELECT json_extract(detail_json, '$.text') FROM job_events WHERE job_id = ? ORDER BY id DESC LIMIT 1`, job).Scan(&d); err != nil || len(d) > 500 || !utf8.ValidString(d) {
		t.Errorf("%d bytes valid=%v %v", len(d), utf8.ValidString(d), err)
	}
	// a detail above 2 KiB is refused
	big := []string{strings.Repeat("a", 900), strings.Repeat("b", 900), strings.Repeat("c", 900)}
	if err := write(Event{JobID: job, Name: EvFailed, Detail: map[string]any{"missing": big}}); err == nil {
		t.Error("a 2.7 KiB detail was accepted")
	}
	// events need the lease
	r.exec(`UPDATE runner_lease SET owner = 'someone-else'`)
	if err := write(Event{JobID: job, Name: EvStatus}); !errors.Is(err, store.ErrLeaseLost) {
		t.Errorf("event without the lease: %v", err)
	}
}

type noisyNotifier struct{ n int }

func (n *noisyNotifier) Notify(context.Context, notify.Message) error {
	n.n++
	return errors.New("ntfy down")
}

func TestNotificationFailuresAreSwallowed(t *testing.T) {
	t.Parallel()
	nn := &noisyNotifier{}
	r := newRig(t, withOpts(func(o *RunnerOptions) { o.Notify = nn }))
	r.disarm()
	b := r.seedBatch(batchSpec{})
	r.cancelIntent(b.Jobs[0])
	r.steps(2) // the batch finishes and its summary cannot be delivered
	if nn.n != 1 || r.str(`SELECT state FROM batches WHERE id = ?`, b.Batch) != "finished" {
		t.Errorf("notifications %d", nn.n)
	}
}

func TestHeartbeatJobLeaseFailure(t *testing.T) {
	t.Parallel()
	ctl := &failCtl{}
	r := newRig(t, withFailing(ctl))
	r.step()
	ctl.arm(2) // the renewal of the job leases follows the lease statement
	err := r.run.heartbeat(t.Context())
	ctl.disarm()
	if !errors.Is(err, errInjected) {
		t.Errorf("%v", err)
	}
}

func TestRunRetriesAfterADatabaseError(t *testing.T) {
	t.Parallel()
	ctl := &failCtl{}
	r := newRig(t, withFailing(ctl))
	r.disarm()
	ctl.arm(1)
	ctx, cancel := contextWithCancel(t)
	done := make(chan error, 1)
	go func() { done <- r.run.Run(ctx) }()
	waitFor(t, func() bool { return ctl.fired.Load() && r.clk.Pending() > 0 })
	ctl.disarm()
	r.advance(HeartbeatEvery + time.Second) // the retry acquires the lease
	waitFor(t, func() bool { return r.run.Holding() })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestTransferWithoutAnyWinnerTimesOut(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.disarm()
	job := r.seedBatch(batchSpec{}).Jobs[0]
	r.force(job, StateTransferring)
	r.exec(`UPDATE jobs SET lease_owner = 'runner-1', lease_expires_at = ?, state_deadline_at = ? WHERE id = ?`,
		store.FormatTime(t0.Add(time.Hour)), store.FormatTime(t0.Add(-time.Minute)), job)
	r.step()
	r.wantState(job, StateFailed)
	r.wantCode(job, CodeArtifactMissing)
}

type badEngine struct{ engine.Engine }

func (badEngine) Artifacts(json.RawMessage) (required, optional []string) {
	return []string{"audio.flac", "latent.npy"}, nil
}

func TestEngineWithAnUnlistedArtifactIsAnError(t *testing.T) {
	t.Parallel()
	reg := registry.Default()
	reg["yue2"] = badEngine{Engine: reg["yue2"]}
	r := newRig(t, withOpts(func(o *RunnerOptions) { o.Engines = reg }))
	b := r.seedBatch(batchSpec{})
	if _, err := r.run.Step(t.Context()); err == nil {
		t.Fatal("an engine asking for latent.npy must be refused")
	}
	r.wantState(b.Jobs[0], StateQueued)
}

func TestPreflightDefaultFreeSpace(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	srv := httptest.NewServer(r.up.Handler())
	defer srv.Close()
	pf := NewPreflight(PreflightOptions{DataDir: t.TempDir(), PublicURL: srv.URL}) // statfs of a real directory, the real upload route
	if err := pf(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
}
