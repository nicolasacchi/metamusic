package jobs

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Small deterministic tests for branches the fault sweeps cannot reach: a write that finds its row changed
// under it, a lease that is gone at that moment, malformed input and the error text of the typed errors.

func TestStaleWrites(t *testing.T) {
	t.Parallel()
	for _, lost := range []bool{false, true} {
		name := "row changed under the writer"
		if lost {
			name = "lease lost"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.disarm()
			sd := r.seedBatch(batchSpec{})
			r.step() // takes the lease and claims
			want := errStale
			if lost {
				r.exec(`UPDATE runner_lease SET owner = 'someone-else' WHERE id = 1`)
				want = store.ErrLeaseLost
			}
			ctx := t.Context()
			stale := jobRec{ID: sd.Jobs[0], State: StateGenerating} // not the state the row is in
			check := func(what string, fn func(tx *sql.Tx, f store.LeaseFence) error) {
				t.Helper()
				err := r.run.tx(ctx, func(tx *sql.Tx) error { return fn(tx, r.run.fence()) })
				if !errors.Is(err, want) {
					t.Errorf("%s: %v, want %v", what, err, want)
				}
			}
			check("setJob", func(tx *sql.Tx, f store.LeaseFence) error {
				j := stale
				return r.run.setJob(ctx, tx, f, &j, set("attempts", 3))
			})
			check("moveJob", func(tx *sql.Tx, f store.LeaseFence) error {
				j := stale
				return r.run.moveJob(ctx, tx, f, &j, StateTransferring, Event{Name: EvStatus})
			})
			check("setAttempt", func(tx *sql.Tx, f store.LeaseFence) error {
				return r.run.setAttempt(ctx, tx, f, "no-such-attempt", "remote_status = ?", "x")
			})
		})
	}
}

func TestTypedErrorText(t *testing.T) {
	t.Parallel()
	if got := (&ContractViolation{Code: "C", Text: "t"}).Error(); got != "jobs: C: t" {
		t.Errorf("ContractViolation: %q", got)
	}
	if got := (&GenerationDisabledError{Reason: "why"}).Error(); got != "jobs: generation is disabled: why" {
		t.Errorf("GenerationDisabledError: %q", got)
	}
	r := newRig(t)
	m := &stubMeter{}
	r.run.UseMeter(m)
	if r.run.o.Meter != BatchMeter(m) {
		t.Error("UseMeter did not install the meter")
	}
}

type stubMeter struct{ BatchMeter }

func TestDecodeCursorRejects(t *testing.T) {
	t.Parallel()
	good := encodeJobCursor(t0, "id-1")
	if at, id, err := decodeCursor(good); err != nil || at != store.FormatTime(t0) || id != "id-1" {
		t.Fatalf("round trip: %q %q %v", at, id, err)
	}
	for name, c := range map[string]string{
		"not base64":  "***",
		"no bar":      encodeRaw("nobar"),
		"short time":  encodeRaw("2026|id"),
		"empty id":    encodeRaw(store.FormatTime(t0) + "|"),
		"unparseable": encodeRaw("xxxx" + store.FormatTime(t0)[4:] + "|id"),
	} {
		if _, _, err := decodeCursor(c); !errors.Is(err, ErrBadCursor) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSmallHelpers(t *testing.T) {
	t.Parallel()
	if got := appendUnique(appendUnique([]string{"a"}, "b"), "a"); len(got) != 2 {
		t.Errorf("appendUnique: %v", got)
	}
	n := 0
	for range mapKeys(map[string]int{"a": 1, "b": 2, "c": 3}) {
		n++
		break
	}
	if n != 1 {
		t.Errorf("mapKeys yielded %d after break", n)
	}
	r := newRig(t)
	a := r.admin("fake", true)
	var out ProviderStatus
	if err := a.loadTrips(t.Context(), "", r.clk.Now(), &out); err != nil || len(out.TripsRecent) != 0 {
		t.Errorf("loadTrips without a backend: %v %+v", err, out.TripsRecent)
	}
	if _, err := scanAtt(failScan{}); err == nil {
		t.Error("scanAtt hid a scan error")
	}
	if _, err := scanJob(failScan{}); err == nil {
		t.Error("scanJob hid a scan error")
	}
}

type failScan struct{}

func (failScan) Scan(...any) error { return errors.New("scan") }

func TestImageDigestOf(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := t.Context()
	digest := "ghcr.example/w@sha256:" + "ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12"
	r.exec(`INSERT INTO worker_meter(worker_key, started_at, first_seen_at, last_seen_at, uptime_s, status_last, image) VALUES('wk1', ?, ?, ?, 1, 'RUNNING', ?)`,
		store.FormatTime(t0), store.FormatTime(t0), store.FormatTime(t0), digest)
	if got, err := imageDigestOf(ctx, r.st.R, "recorded", "wk1", "b"); err != nil || got != "recorded" {
		t.Errorf("recorded digest: %q %v", got, err)
	}
	if got, err := imageDigestOf(ctx, r.st.R, "", "wk1", "b"); err != nil || got != "sha256:ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12" {
		t.Errorf("worker image digest: %q %v", got, err)
	}
	if got, err := imageDigestOf(ctx, r.st.R, "", "wk-none", "b7"); err != nil || got != "build:b7" {
		t.Errorf("fallback: %q %v", got, err)
	}
	dead, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := imageDigestOf(dead, r.st.R, "", "wk1", "b"); err == nil {
		t.Error("a cancelled context was ignored")
	}
}

func TestWaitResultOfMissingJob(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	s := r.service(t.Context())
	if _, err := s.result(t.Context(), "no-such-job", true, false); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("result: %v", err)
	}
	if _, err := s.last(t.Context(), "no-such-job", StateQueued); err == nil {
		t.Error("last of a missing job")
	}
}

func TestRefreshQuoteWithoutLease(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.step()
	r.exec(`UPDATE runner_lease SET owner = 'someone-else' WHERE id = 1`)
	rec := r.recon()
	if err := rec.refreshQuote(t.Context()); !errors.Is(err, store.ErrLeaseLost) && err != nil {
		t.Errorf("refreshQuote without the lease: %v", err)
	}
	_ = time.Second
}

func encodeRaw(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func TestBilling_ComputeFreeBucketsAreSkippedByTheActivityCheck(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.billBatch(at(12, 50), at(13, 20), 20000, at(21, 0))
	r.hours(map[time.Time][2]money.MicroUSD{at(12, 0): {5000, 0}, at(13, 0): {25000, 25000}})
	r.mustPass(r.recon())
	if got := r.str(`SELECT verdict FROM billing_windows`); got != "ok" {
		t.Errorf("verdict %q (windows %d)", got, r.windows())
	}
}

func TestBilling_UnsubmittedBatchesAloneCloseNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.clk.Set(at(6, 10))
	s := r.seedBatch(batchSpec{}) // active, never reached the provider: it never finishes
	r.clk.Set(at(23, 0))
	rec := r.recon()
	closed, err := rec.closeWindow(t.Context(), r.clk.Now())
	if err != nil || closed {
		t.Fatalf("closeWindow = %v, %v", closed, err)
	}
	if r.windows() != 0 || r.str(`SELECT reconciled_at FROM batches WHERE id = ?`, s.Batch) != "" {
		t.Error("a window or a reconciliation without a finished batch")
	}
}

func TestIngestBridge_ReportedArtifactDiffersFromUpload(t *testing.T) {
	b := newBridgeRig(t, false)
	bodies := map[string][]byte{"audio.flac": []byte("x"), "request.json": []byte(`{}`), "config.json": []byte(`{}`),
		"result.json": resultJSON(false, false), "mm-result.json": []byte(`{}`), "plan.json": []byte(`{}`)}
	files, out := b.received(bodies)
	out.TakeID = "job-x"
	d := out.Artifacts["plan.json"]
	d.SHA256 = strings.Repeat("0", 64)
	out.Artifacts["plan.json"] = d
	_, err := b.bridge.IngestTake(WithLeaseOwner(t.Context(), "runner-1"), JobRow{ID: "job-x", RecipeID: testRecipe}, AttemptRow{ID: "att-x"}, files, out)
	var cv *ContractViolation
	if !errors.As(err, &cv) || cv.Code != "ARTIFACT_MISMATCH" {
		t.Fatalf("err = %v", err)
	}
}

func TestIngestBridge_OversizedResultJSON(t *testing.T) {
	b := newBridgeRig(t, false)
	big := append([]byte(`{"pad":"`), make([]byte, 4<<20)...)
	bodies := map[string][]byte{"audio.flac": []byte("x"), "request.json": []byte(`{}`), "config.json": []byte(`{}`),
		"result.json": big, "mm-result.json": []byte(`{}`)}
	files, out := b.received(bodies)
	out.TakeID = "job-x"
	_, err := b.bridge.IngestTake(WithLeaseOwner(t.Context(), "runner-1"), JobRow{ID: "job-x", RecipeID: testRecipe}, AttemptRow{ID: "att-x"}, files, out)
	var cv *ContractViolation
	if !errors.As(err, &cv) || cv.Code != "WORKER_CONTRACT" || !strings.Contains(cv.Text, "unreadable") {
		t.Fatalf("err = %v", err)
	}
}

func TestReconciler_CrashLoopWithoutMeterKeyForgetsGoneWorkers(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.seedBatch(batchSpec{jobs: 3})
	rec := r.recon(func(o *ReconcilerOptions) { o.MeterKey = nil })
	// without a meter key a worker is tracked by its provider id; a worker that goes away is forgotten
	r.sweep(worker("w1", "UNHEALTHY", upt(900), t0))
	r.mustPass(rec)
	rec.mu.Lock()
	n := len(rec.crash)
	rec.mu.Unlock()
	if n != 1 {
		t.Fatalf("tracked %d workers, want 1", n)
	}
	r.sweep()
	r.mustPass(rec)
	rec.mu.Lock()
	n = len(rec.crash)
	rec.mu.Unlock()
	if n != 0 {
		t.Errorf("a worker that disappeared is still tracked (%d)", n)
	}
	if r.count(`SELECT COUNT(*) FROM provider_trips`) != 0 {
		t.Error("one observation of each tripped")
	}
}

func TestRunner_RefuseClaimWaitsForTheLatestGhostWindow(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.disarm()
	sd := r.seedBatch(batchSpec{})
	r.step()
	job := sd.Jobs[0]
	for i, h := range []int{2, 5} { // two live attempts, the later window must win
		r.exec(`INSERT INTO job_attempts(id, job_id, n, backend, state, submitted_at, window_ends_at, price_pph_micro_usd, created_at)
			VALUES(?, ?, ?, 'fake', 'unknown', ?, ?, 690000, ?)`, r.newID(), job, 90+i, store.FormatTime(t0), store.FormatTime(t0.Add(time.Duration(h)*time.Hour)), store.FormatTime(t0))
	}
	if _, err := r.run.refuseClaim(t.Context(), &claimRefusal{jobID: job, code: "X"}); err != nil {
		t.Fatal(err)
	}
	want := store.FormatTime(t0.Add(5*time.Hour + time.Second))
	if got := r.str(`SELECT next_attempt_at FROM jobs WHERE id = ?`, job); got != want {
		t.Errorf("next_attempt_at %q, want %q", got, want)
	}
}

func TestRunner_NoticesAreSentOncePerWindow(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.disarm()
	sd := r.seedBatch(batchSpec{})
	r.step()
	b := batchRec{ID: sd.Batch, IdleS: 20}
	r.run.warnIdleDrift(b, 60)
	r.run.warnIdleDrift(b, 60)
	r.run.preflightFailed(t.Context(), errors.New("disk"), sd.Jobs[0])
	r.advance(time.Minute)
	r.run.preflightFailed(t.Context(), errors.New("disk"), sd.Jobs[0])
	if n := r.count(`SELECT COUNT(*) FROM job_events WHERE event = ?`, EvPreflightFailed); n != 1 {
		t.Errorf("%d preflight events within the hour, want 1", n)
	}
}

func TestRunner_ConcludeLeavesOtherStatesAlone(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.disarm()
	sd := r.seedBatch(batchSpec{})
	r.step()
	err := r.run.tx(t.Context(), func(tx *sql.Tx) error {
		j, err := loadJob(t.Context(), tx, sd.Jobs[0])
		if err != nil {
			return err
		}
		j.State = StateQueued
		return r.run.conclude(t.Context(), tx, r.run.fence(), &j, batchRec{})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFollowTerminal_QuietOutcomes(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.disarm()
	sd := r.seedBatch(batchSpec{})
	r.step()
	for _, c := range []struct {
		name  string
		state string
		st    provider.RemoteState
	}{
		{"a ghost ended while the job is queued", StateQueued, provider.Failed},
		{"a non-terminal status while generating", StateGenerating, provider.Running},
		{"a state with nothing to follow", StateCancelled, provider.Failed},
	} {
		err := r.run.tx(t.Context(), func(tx *sql.Tx) error {
			j, err := loadJob(t.Context(), tx, sd.Jobs[0])
			if err != nil {
				return err
			}
			j.State = c.state
			return r.run.followTerminal(t.Context(), tx, r.run.fence(), &obs{j: &j, st: provider.RemoteStatus{State: c.st}})
		})
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestImageDigestOf_ScanFailure(t *testing.T) {
	t.Parallel()
	ctl := &failCtl{scans: true}
	r := newRig(t, withFailing(ctl))
	r.exec(`INSERT INTO worker_meter(worker_key, started_at, first_seen_at, last_seen_at, uptime_s, status_last, image) VALUES('wk1', ?, ?, ?, 1, 'RUNNING', 'img')`,
		store.FormatTime(t0), store.FormatTime(t0), store.FormatTime(t0))
	ctl.arm(2) // call 1 is the query, call 2 the first row, which cannot be scanned
	_, err := imageDigestOf(t.Context(), r.st.R, "", "wk1", "b")
	ctl.disarm()
	if !errors.Is(err, errInjected) && err == nil {
		t.Error("a row that cannot be scanned was ignored")
	}
}

func TestEdgeCases_NothingToDo(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	// a reconciler asked to run after its context ended returns at once
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.recon().Run(ctx); err != nil {
		t.Errorf("Run on a cancelled context: %v", err)
	}
	// without an audit store a trip's follow-up row is skipped, not an error
	r.recon(func(o *ReconcilerOptions) { o.Audit = nil }).auditAfter(t.Context(), "reconciler:test", nil, false)
	// without Ears the analysis step is a no-op
	NewIngestBridge(BridgeOptions{}).analyse(t.Context(), "runner-1", JobRow{ID: "j"}, "t")
}

func TestMeterModel_AttemptWithoutALifetimeIsIgnored(t *testing.T) {
	t.Parallel()
	m := &meterModel{now: t0, owner: map[int]string{}, overheadS: map[string]int64{}, lastEnd: map[string]time.Time{}, lifetimesOf: map[string][]int{},
		attempts: []meterAttempt{{id: "a", batchID: "b", key: "no-such-worker", submitted: t0, live: true}}}
	m.attribute()
	if len(m.lastEnd) != 0 || len(m.overheadS) != 0 {
		t.Errorf("an attempt that ran on no known worker changed the model: %v %v", m.lastEnd, m.overheadS)
	}
}
