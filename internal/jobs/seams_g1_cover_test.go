package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/jobspec"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

// breakNeeds gives a job a needs_json that is a valid JSON array (so the column CHECK accepts it) but no list of capabilities.
// The frozen-columns trigger is dropped because nothing legitimate ever rewrites needs_json.
func breakNeeds(r *rig, job string) {
	r.exec(`DROP TRIGGER jobs_frozen`)
	r.exec(`UPDATE jobs SET needs_json = '[1]' WHERE id = ?`, job)
}

// TestTakeInfo_FailsClosedOnMalformedNeeds: a job whose needs_json cannot be parsed makes the take hook's input unknowable, so
// the take fails closed: takeInfo errors, and an ingest with a hook wired rolls the whole take back (no track, no file, no hook
// call), then succeeds once the row is repaired.
func TestTakeInfo_FailsClosedOnMalformedNeeds(t *testing.T) {
	t.Run("takeInfo reports the job", func(t *testing.T) {
		b := kindBridge(t, nil)
		job := b.seed(batchSpec{recipe: b.newRecipe("pop"), needs: `["ears"]`}).Jobs[0]
		breakNeeds(b.rig, job)
		_, err := takeInfo(t.Context(), b.st.R, JobRow{ID: job}, "trk", store.LeaseFence{})
		if err == nil || !strings.Contains(err.Error(), "needs_json") || !strings.Contains(err.Error(), job) {
			t.Fatalf("err = %v, want a needs_json error naming the job", err)
		}
	})
	t.Run("a database error is not read as an empty job", func(t *testing.T) {
		b := kindBridge(t, nil)
		job := b.seed(batchSpec{recipe: b.newRecipe("pop")}).Jobs[0]
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if info, err := takeInfo(ctx, b.st.R, JobRow{ID: job}, "trk", store.LeaseFence{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v (info %+v), want context.Canceled", err, info)
		}
	})
	t.Run("the take rolls back", func(t *testing.T) {
		h := &recHook{}
		b := newBridgeRig(t, true, func(o *BridgeOptions) { o.TakeHook = h })
		h.b = b
		job := b.seed(batchSpec{recipe: b.newRecipe("pop"), needs: `["ears","v2"]`}).Jobs[0]
		jr, ar, files, out := b.capture(job, b.flac(3))
		before := tableCounts(t, b)
		breakNeeds(b.rig, job)
		id, err := b.bridge.IngestTake(WithLeaseOwner(t.Context(), "runner-1"), jr, ar, files, out)
		if err == nil || id != "" {
			t.Fatalf("IngestTake = %q, %v, want a failure", id, err)
		}
		if len(h.tx) != 0 || len(h.commit) != 0 {
			t.Errorf("the hook ran: tx %d, commit %d", len(h.tx), len(h.commit))
		}
		if n := b.count(`SELECT COUNT(*) FROM tracks`); n != 0 || len(b.tracksOnDisk()) != 0 || b.mastersOnDisk() != 0 {
			t.Errorf("half-written take: %d tracks, %v takes, %d masters", n, b.tracksOnDisk(), b.mastersOnDisk())
		}
		after := tableCounts(t, b)
		for table, n := range before {
			// the failure may be recorded by the caller (events), never by the take transaction
			if after[table] != n && table != "job_events" && table != "uploads" {
				t.Errorf("table %s changed from %d to %d rows", table, n, after[table])
			}
		}
		// repaired: the same files ingest
		b.exec(`UPDATE jobs SET needs_json = '["ears","v2"]' WHERE id = ?`, job)
		id, err = b.bridge.IngestTake(WithLeaseOwner(t.Context(), "runner-1"), jr, ar, files, out)
		if err != nil || id == "" || b.count(`SELECT COUNT(*) FROM tracks`) != 1 || len(h.tx) != 1 {
			t.Fatalf("retry: %q %v (hook calls %d)", id, err, len(h.tx))
		}
	})
}

// TestScanJob_MalformedNeedsIsAnError: a job row whose needs_json is not a capability list is not served as a job with no needs.
func TestScanJob_MalformedNeedsIsAnError(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	job := r.seedBatch(batchSpec{needs: `["v2"]`}).Jobs[0]
	breakNeeds(r, job)
	if _, err := loadJob(t.Context(), r.st.R, job); err == nil || !strings.Contains(err.Error(), job) {
		t.Fatalf("loadJob err = %v, want one naming the job", err)
	}
}

// TestIngestKindJob_Refusals: every way a kind ingest can be refused before its transaction writes anything.
func TestIngestKindJob_Refusals(t *testing.T) {
	setup := func(t *testing.T, kinds map[string]KindIngester) (*bridgeRig, *planIngester, JobRow, AttemptRow, []upload.Received, provider.Output) {
		t.Helper()
		pi := &planIngester{}
		if kinds == nil {
			kinds = map[string]KindIngester{KindPlan: pi}
		}
		b := kindBridge(t, kinds)
		job := b.seed(batchSpec{recipe: b.newRecipe("pop"), kind: KindPlan, needs: `["plan","v2"]`}).Jobs[0]
		jr, ar, files, out := b.capture(job, []byte("x"))
		return b, pi, jr, ar, files, out
	}
	ctx := func() context.Context { return WithLeaseOwner(context.Background(), "runner-1") }
	nothingWritten := func(t *testing.T, b *bridgeRig, pi *planIngester, job string) {
		t.Helper()
		if n, _ := pi.calls(); n != 0 {
			t.Errorf("the ingester ran %d times", n)
		}
		if b.count(`SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event = 'kind_ingested'`, job) != 0 ||
			b.count(`SELECT COUNT(*) FROM uploads WHERE job_id = ? AND state = 'consumed'`, job) != 0 {
			t.Errorf("a refused ingest wrote: %v", b.events(job))
		}
	}
	violationCode := func(t *testing.T, err error, want string) {
		t.Helper()
		var cv *ContractViolation
		if !errors.As(err, &cv) || cv.Code != want {
			t.Fatalf("err = %v, want contract violation %s", err, want)
		}
	}
	t.Run("no runner lease", func(t *testing.T) {
		b, pi, jr, ar, files, out := setup(t, nil)
		if _, _, err := b.bridge.IngestKindJob(context.Background(), jr, ar, files, out); err == nil || !strings.Contains(err.Error(), "lease") {
			t.Fatalf("err = %v", err)
		}
		nothingWritten(t, b, pi, jr.ID)
	})
	t.Run("a nil ingester is no ingester", func(t *testing.T) {
		b, pi, jr, ar, files, out := setup(t, map[string]KindIngester{KindPlan: nil})
		_, _, err := b.bridge.IngestKindJob(ctx(), jr, ar, files, out)
		var ife *IngestFailedError
		if !errors.As(err, &ife) || ife.Code != CodeKindUnavailable {
			t.Fatalf("err = %v", err)
		}
		nothingWritten(t, b, pi, jr.ID)
	})
	t.Run("generate is never served here", func(t *testing.T) {
		b, pi, jr, ar, files, out := setup(t, map[string]KindIngester{KindGenerate: &planIngester{}})
		jr.Kind = KindGenerate
		_, _, err := b.bridge.IngestKindJob(ctx(), jr, ar, files, out)
		var ife *IngestFailedError
		if !errors.As(err, &ife) || ife.Code != CodeKindUnavailable {
			t.Fatalf("err = %v", err)
		}
		nothingWritten(t, b, pi, jr.ID)
	})
	t.Run("an unwired bridge", func(t *testing.T) {
		pi := &planIngester{}
		b := newBridgeRig(t, false, func(o *BridgeOptions) { o.Kinds = map[string]KindIngester{KindPlan: pi}; o.Uploads = nil })
		_, _, err := b.bridge.IngestKindJob(ctx(), JobRow{ID: "j", Kind: KindPlan}, AttemptRow{}, nil, provider.Output{})
		if err == nil || !strings.Contains(err.Error(), "not wired") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("output of another job", func(t *testing.T) {
		b, pi, jr, ar, files, out := setup(t, nil)
		out.TakeID = "someone-else"
		_, _, err := b.bridge.IngestKindJob(ctx(), jr, ar, files, out)
		violationCode(t, err, "WORKER_CONTRACT")
		nothingWritten(t, b, pi, jr.ID)
	})
	t.Run("output that is not ok", func(t *testing.T) {
		b, pi, jr, ar, files, out := setup(t, nil)
		out.Status = "error"
		_, _, err := b.bridge.IngestKindJob(ctx(), jr, ar, files, out)
		violationCode(t, err, "WORKER_CONTRACT")
		nothingWritten(t, b, pi, jr.ID)
	})
	t.Run("result.json not received", func(t *testing.T) {
		b, pi, jr, ar, files, out := setup(t, nil)
		var rest []upload.Received
		for _, f := range files {
			if f.Artifact != provider.ResultArtifact {
				rest = append(rest, f)
			}
		}
		_, _, err := b.bridge.IngestKindJob(ctx(), jr, ar, rest, out)
		violationCode(t, err, "ARTIFACT_MISSING")
		nothingWritten(t, b, pi, jr.ID)
	})
	t.Run("a reported artifact that was not received", func(t *testing.T) {
		b, pi, jr, ar, files, out := setup(t, nil)
		out.Artifacts = map[string]provider.ArtifactDigest{"plan-00.tar": {SHA256: strings.Repeat("a", 64), Bytes: 3}}
		var rest []upload.Received // the bundle is reported (it is in the plan's allowlist) but was never uploaded
		for _, f := range files {
			if f.Artifact != "plan-00.tar" {
				rest = append(rest, f)
			}
		}
		_, _, err := b.bridge.IngestKindJob(ctx(), jr, ar, rest, out)
		violationCode(t, err, "ARTIFACT_MISSING")
		nothingWritten(t, b, pi, jr.ID)
	})
	t.Run("a received artifact that differs from its report", func(t *testing.T) {
		b, pi, jr, ar, files, out := setup(t, nil)
		rep := map[string]provider.ArtifactDigest{}
		for name, a := range out.Artifacts {
			a.Bytes++
			rep[name] = a
		}
		if len(rep) == 0 {
			t.Fatal("the captured output reports no artifact")
		}
		out.Artifacts = rep
		_, _, err := b.bridge.IngestKindJob(ctx(), jr, ar, files, out)
		violationCode(t, err, "ARTIFACT_MISMATCH")
		nothingWritten(t, b, pi, jr.ID)
	})
}

// TestIngestKindJob_EventCarriesWhatTheIngesterReturned: the kind_ingested event records the kind, the outcome, the track and the
// audio length of the job, so that a runner adopting it later completes the job with the same facts.
func TestIngestKindJob_EventCarriesWhatTheIngesterReturned(t *testing.T) {
	pi := &planIngester{outcome: "truncated"}
	b := kindBridge(t, map[string]KindIngester{KindAnalyze: pi})
	src, _, _ := masterOf(t, b.rig)
	pi.track = &src
	job := b.seed(batchSpec{recipe: b.newRecipe("pop"), kind: KindAnalyze, source: src, needs: `["analyze","v2"]`}).Jobs[0]
	jr, ar, files, out := b.capture(job, []byte("x"))
	n := json.Number("12.5")
	out.AudioSeconds = &n
	track, outcome, err := b.bridge.IngestKindJob(WithLeaseOwner(t.Context(), "runner-1"), jr, ar, files, out)
	if err != nil || track != src || outcome != "truncated" {
		t.Fatalf("IngestKindJob = %q %q %v", track, outcome, err)
	}
	var detail string
	if err := b.st.R.QueryRow(`SELECT detail_json FROM job_events WHERE job_id = ? AND event = 'kind_ingested'`, job).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"kind":"analyze"`, `"outcome":"truncated"`, `"track_id":"` + src + `"`, `"audio_s":12.5`} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail %s lacks %s", detail, want)
		}
	}
	if got := b.str(`SELECT COALESCE(env_id, 'NULL') FROM jobs WHERE id = ?`, job); got == "NULL" {
		t.Error("the job did not receive the worker environment")
	}
}

// TestIngestKindJob_EveryDatabaseCallMayFail fails each database call of one kind ingest in turn: the result is either complete
// (event, environment and consumed uploads together) or absent (none of them), and the retry from the still-present uploads
// succeeds.
func TestIngestKindJob_EveryDatabaseCallMayFail(t *testing.T) {
	if testing.Short() {
		t.Skip("one ingest per database call")
	}
	kinds := func() map[string]KindIngester { return map[string]KindIngester{KindPlan: &planIngester{}} }
	src := kindBridge(t, kinds())
	job := src.seed(batchSpec{recipe: src.newRecipe("pop"), kind: KindPlan, needs: `["plan","v2"]`}).Jobs[0]
	jr, ar, files, out := src.capture(job, []byte("x"))
	fz := src.freeze()
	attempt := func(t *testing.T, ctl *failCtl, k int64) (*bridgeRig, error) {
		r := fz.thaw(t, ctl)
		b := attachBridge(t, r, false, func(o *BridgeOptions) { o.Kinds = kinds() })
		ctl.arm(k)
		_, _, err := b.bridge.IngestKindJob(WithLeaseOwner(t.Context(), "runner-1"), jr, ar, append([]upload.Received(nil), files...), out)
		ctl.disarm()
		return b, err
	}
	count := &failCtl{rows: true, scans: true}
	if _, err := attempt(t, count, 0); err != nil {
		t.Fatalf("the clean call: %v", err)
	}
	total := count.calls()
	if total < 5 {
		t.Fatalf("the call made %d database calls", total)
	}
	for k := int64(1); k <= total; k++ {
		t.Run(fmt.Sprintf("call %d", k), func(t *testing.T) {
			t.Parallel()
			b, err := attempt(t, &failCtl{rows: true, scans: true}, k)
			events := b.count(`SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event = 'kind_ingested'`, job)
			env := b.str(`SELECT COALESCE(env_id, 'NULL') FROM jobs WHERE id = ?`, job)
			open := b.count(`SELECT COUNT(*) FROM uploads WHERE job_id = ? AND state <> 'consumed'`, job)
			if err == nil {
				if events != 1 || env == "NULL" || open != 0 {
					t.Fatalf("success with %d events, env %s, %d unconsumed uploads", events, env, open)
				}
				return
			}
			// the one ambiguous point is a failure after the commit (removing the consumed files), which the bridge only logs
			if events != 0 || env != "NULL" || open == 0 {
				t.Fatalf("failure (%v) left %d events, env %s, %d unconsumed uploads", err, events, env, open)
			}
			if _, _, err = b.bridge.IngestKindJob(WithLeaseOwner(t.Context(), "runner-1"), jr, ar, files, out); err != nil {
				t.Fatalf("retry: %v", err)
			}
			if b.count(`SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event = 'kind_ingested'`, job) != 1 {
				t.Error("the retry left no event")
			}
		})
	}
}

// TestIngest_KindCommitted_EveryDatabaseCallMayFail: a runner adopting a plan job whose kind transaction already committed
// completes it from the event; failing each database call of that Step never completes it half-way (state, outcome and
// environment move together) and the next Step finishes it.
func TestIngest_KindCommitted_EveryDatabaseCallMayFail(t *testing.T) {
	if testing.Short() {
		t.Skip("one runner step per database call")
	}
	pi := &planIngester{outcome: "truncated"}
	src := kindBridge(t, map[string]KindIngester{KindPlan: pi})
	job := src.seed(batchSpec{recipe: src.newRecipe("pop"), kind: KindPlan, needs: `["plan","v2"]`}).Jobs[0]
	jr, ar, files, out := src.capture(job, []byte("x"))
	if _, _, err := src.bridge.IngestKindJob(WithLeaseOwner(t.Context(), "runner-1"), jr, ar, files, out); err != nil {
		t.Fatal(err)
	}
	src.advance(2 * LeaseLength)
	fz := src.freeze()
	attempt := func(t *testing.T, ctl *failCtl, k int64) *bridgeRig {
		r := fz.thaw(t, ctl)
		b := attachBridge(t, r, false, func(o *BridgeOptions) { o.Kinds = map[string]KindIngester{KindPlan: &planIngester{}} })
		b2 := b.second("runner-2", func(o *RunnerOptions) { o.Ingest = b.bridge })
		ctl.arm(k)
		for range 3 {
			_, _ = b2.Step(t.Context())
			_ = b2.Drain(t.Context())
		}
		ctl.disarm()
		for range 6 {
			_, _ = b2.Step(t.Context())
			_ = b2.Drain(t.Context())
			if IsTerminal(b.state(job)) {
				break
			}
			b.advance(3e9)
		}
		return b
	}
	count := &failCtl{rows: true, scans: true}
	attempt(t, count, 0)
	total := count.calls()
	if total < 3 {
		t.Fatalf("the steps made %d database calls", total)
	}
	step := max(total/40, 1) // the adoption is a handful of calls; the rest is the runner's idle tick
	for k := int64(1); k <= total; k += step {
		t.Run(fmt.Sprintf("call %d", k), func(t *testing.T) {
			t.Parallel()
			b := attempt(t, &failCtl{rows: true, scans: true}, k)
			b.wantState(job, StateCompleted)
			if got := b.str(`SELECT COALESCE(track_id, 'NULL') || '|' || outcome FROM jobs WHERE id = ?`, job); got != "NULL|truncated" {
				t.Errorf("job row = %s", got)
			}
			if b.str(`SELECT COALESCE(env_id, 'NULL') FROM jobs WHERE id = ?`, job) == "NULL" {
				t.Error("the completed plan job lost its environment")
			}
		})
	}
}

// TestCompleteKinded_NothingToAdopt: a plan job with no kind_ingested event is left for the ordinary ingest, and one already past
// ingesting is not completed again.
func TestCompleteKinded_NothingToAdopt(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	job := r.seedBatch(batchSpec{kind: KindPlan, needs: `["plan"]`}).Jobs[0]
	if done, err := r.run.completeKinded(t.Context(), job); err != nil || done {
		t.Fatalf("no event: done %v err %v", done, err)
	}
	if done, err := r.run.completeCommitted(t.Context(), job); err != nil || done {
		t.Fatalf("completeCommitted without an event: done %v err %v", done, err)
	}
	if st := r.state(job); st == StateCompleted {
		t.Fatalf("job completed without an ingest")
	}
}

// TestMintPins_ReadErrorsAndShortWindows: a database failure while reading the pinned master is not reported as a changed master,
// and a window that has already closed still yields a link that lives one bucket.
func TestMintPins_ReadErrorsAndShortWindows(t *testing.T) {
	r, sg, o := minterRig(t)
	id, sha, size := masterOf(t, r)
	m := NewInputMinter(o).(PinMinter)
	pin := jobspec.Inputs{SrcAudio: &jobspec.Pin{TrackID: id, MasterSHA256: sha, Bytes: size}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := m.MintPins(ctx, pin, r.clk.Now().Add(time.Hour))
	if err == nil || errors.Is(err, ErrInputChanged) || !strings.Contains(err.Error(), "read pinned master") {
		t.Fatalf("err = %v, want a read error that is not ErrInputChanged", err)
	}
	got, err := m.MintPins(t.Context(), pin, r.clk.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if exp := verifyLink(t, sg, got[provider.InputSrcAudio].URL, sha); !exp.After(r.clk.Now()) {
		t.Errorf("a link for a closed window is already expired: %v", exp)
	}
}

// TestIngest_KindIngesterFailuresKeepTheirCode: what a kind ingester refuses with is what the job records, and nothing it wrote
// stays: a contract violation fails the job with its own code, an IngestFailedError with its own ingest code (neither is
// rewritten to DB_INSERT), and a plain error is DB_INSERT.
func TestIngest_KindIngesterFailuresKeepTheirCode(t *testing.T) {
	for _, c := range []struct {
		name     string
		err      error
		wantCode string
		wantText string
	}{
		{"contract violation", violation("WORKER_CONTRACT", "plan is empty"), "WORKER_CONTRACT", ""},
		{"ingest failure", &IngestFailedError{Code: "PLAN_REJECTED", Err: errors.New("no seeds")}, CodeIngestFailed, `"ingest_code":"PLAN_REJECTED"`},
		{"plain error", errors.New("disk full"), CodeIngestFailed, `"ingest_code":"DB_INSERT"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			pi := &planIngester{err: c.err}
			b := kindBridge(t, map[string]KindIngester{KindPlan: pi})
			job := b.seed(batchSpec{recipe: b.newRecipe("pop"), kind: KindPlan, needs: `["plan","v2"]`}).Jobs[0]
			b.runKind(job)
			b.wantState(job, StateFailed)
			b.wantCode(job, c.wantCode)
			if c.wantText != "" {
				var detail string
				if err := b.st.R.QueryRow(`SELECT detail_json FROM job_events WHERE job_id = ? AND code = ? ORDER BY id DESC LIMIT 1`, job, c.wantCode).Scan(&detail); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(detail, c.wantText) {
					t.Errorf("detail = %s, want %s", detail, c.wantText)
				}
			}
			if b.count(`SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event = 'kind_ingested'`, job) != 0 ||
				b.str(`SELECT COALESCE(env_id, 'NULL') FROM jobs WHERE id = ?`, job) != "NULL" {
				t.Errorf("a refused kind ingest left its writes: %v", b.events(job))
			}
			if _, after := pi.calls(); after != 0 {
				t.Error("AfterKindCommit ran after a refusal")
			}
		})
	}
}

// TestIngestKindJob_CancelledContext: a cancelled ingest stops before its
// transaction commits anything.
func TestIngestKindJob_CancelledContext(t *testing.T) {
	pi := &planIngester{}
	b := kindBridge(t, map[string]KindIngester{KindPlan: pi})
	job := b.seed(batchSpec{recipe: b.newRecipe("pop"), kind: KindPlan, needs: `["plan","v2"]`}).Jobs[0]
	jr, ar, files, out := b.capture(job, []byte("x"))
	ctx, cancel := context.WithCancel(WithLeaseOwner(t.Context(), "runner-1"))
	cancel()
	if _, _, err := b.bridge.IngestKindJob(ctx, jr, ar, files, out); err == nil {
		t.Fatal("a cancelled ingest succeeded")
	}
	if n, _ := pi.calls(); n != 0 || b.count(`SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event = 'kind_ingested'`, job) != 0 {
		t.Errorf("a cancelled ingest wrote (ingester calls %d)", n)
	}
}

// TestCompleteKinded_SettledJobIsLeftAlone: the kind_ingested event of a job that is no longer ingesting (cancelled or completed
// meanwhile) is reported as handled but changes nothing.
func TestCompleteKinded_SettledJobIsLeftAlone(t *testing.T) {
	pi := &planIngester{}
	b := kindBridge(t, map[string]KindIngester{KindPlan: pi})
	job := b.seed(batchSpec{recipe: b.newRecipe("pop"), kind: KindPlan, needs: `["plan","v2"]`}).Jobs[0]
	b.runKind(job)
	b.wantState(job, StateCompleted)
	before := b.str(`SELECT outcome || '|' || updated_at FROM jobs WHERE id = ?`, job)
	done, err := b.run.completeKinded(t.Context(), job)
	if err != nil || !done {
		t.Fatalf("completeKinded = %v, %v", done, err)
	}
	if after := b.str(`SELECT outcome || '|' || updated_at FROM jobs WHERE id = ?`, job); after != before {
		t.Errorf("a completed job changed: %s -> %s", before, after)
	}
	if n := b.count(`SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event = 'completed'`, job); n > 1 {
		t.Errorf("%d completed events", n)
	}
}

// TestCompleteJobAs_UnknownTrackFails: completing a job with a track that does not exist is an error, not a job completed without
// its environment.
func TestCompleteJobAs_UnknownTrackFails(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	job := r.seedBatch(batchSpec{}).Jobs[0]
	err := r.run.tx(t.Context(), func(tx *sql.Tx) error {
		cur, err := loadJob(t.Context(), tx, job)
		if err != nil {
			return err
		}
		return r.run.completeJobAs(t.Context(), tx, r.run.fence(), &cur, "no-such-track", "", provider.Output{})
	})
	if err == nil || !strings.Contains(err.Error(), "read ingested track") {
		t.Fatalf("err = %v", err)
	}
	if st := r.state(job); st == StateCompleted {
		t.Error("the job completed")
	}
}
