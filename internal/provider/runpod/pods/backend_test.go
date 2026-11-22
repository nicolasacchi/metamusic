package pods_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods/podtest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// brig is an srig with a pod backend over the same database, queue and clock, and a pod API fake behind it.
type brig struct {
	*srig
	b       *pods.Backend
	srv     *podtest.Server
	key     bool
	billing bool
}

func newBRig(t *testing.T, mod ...func(*pods.BackendOptions)) *brig {
	t.Helper()
	r := newSRig(t)
	dir, key := t.TempDir(), canary("pod")
	writeKey(t, dir, key, 0o600)
	srv := podtest.New(t, podtest.Options{Key: key})
	x := &brig{srig: r, srv: srv, key: true}
	x.b = x.backend(dir, mod...)
	return x
}

func (x *brig) backend(dir string, mod ...func(*pods.BackendOptions)) *pods.Backend {
	o := pods.BackendOptions{
		API:   pods.NewClient(pods.Options{BaseURL: x.srv.URL, KeyDir: dir, UserAgent: pods.UserAgentFor("test")}),
		Store: x.st, Queue: x.q, Clock: x.clk, Image: testDigest, GPUTypes: []string{gpu4090, "NVIDIA RTX A5000"},
		MaxRuntime: 6 * time.Hour, KillSlackS: 15,
		KeyPresent:      func() bool { return x.key },
		BillingReadable: func(context.Context) bool { return x.billing },
	}
	for _, m := range mod {
		m(&o)
	}
	return pods.NewBackend(o)
}

// attempt creates a job and a pending attempt of a batch WITHOUT an assignment (Submit writes it) and returns the attempt id.
func (x *brig) attempt(batchID string) string {
	x.t.Helper()
	x.jobs++
	ts := store.FormatTime(rigNow)
	job := fmt.Sprintf("01900000-0000-7000-8000-0000000a%04d", x.jobs)
	att := fmt.Sprintf("01900000-0000-7000-8000-0000000c%04d", x.jobs)
	x.exec(`INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,operator,mutation_json,title,expected_audio_s,expected_s,
		timeout_s,expected_micro_usd,reserved_micro_usd,state,next_attempt_at,created_at,updated_at)
		VALUES(?,?,?,'generate','yue2','fake','01900000-0000-7000-8000-00000000f001','reroll_seed','{}','T',216,96,186,10,20,'queued',?,?,?)`, job, batchID, x.jobs, ts, ts, ts)
	x.exec(`INSERT INTO job_attempts(id,job_id,n,backend,state,ref,submitted_at,window_ends_at,price_pph_micro_usd,created_at)
		VALUES(?,?,1,'fake','pending',NULL,?,?,690000,?)`, att, job, ts, store.FormatTime(x.deadl), ts)
	return att
}

func (x *brig) submit(batchID string, timeoutS int) (string, provider.Ref, error) {
	x.t.Helper()
	att := x.attempt(batchID)
	ref, err := x.b.Submit(bg(), x.spec(att, timeoutS))
	return att, ref, err
}

func (x *brig) mustSubmit(batchID string) string {
	x.t.Helper()
	att, ref, err := x.submit(batchID, 186)
	if err != nil {
		x.t.Fatalf("submit: %v", err)
	}
	if ref.Backend != provider.BackendRunPodPod || ref.ID != att {
		x.t.Fatalf("ref = %+v", ref)
	}
	return att
}

func (x *brig) status(att string) provider.RemoteStatus {
	x.t.Helper()
	st, err := x.b.Poll(bg(), provider.Ref{Backend: x.b.Name(), ID: att})
	if err != nil {
		x.t.Fatal(err)
	}
	return st
}

func (x *brig) assignment(att string) pods.Assignment {
	x.t.Helper()
	a, err := pods.GetAssignment(bg(), x.st.R, att)
	if err != nil {
		x.t.Fatal(err)
	}
	return a
}

func wantRefusal(t *testing.T, err error, what string) {
	t.Helper()
	var se *pods.SubmitError
	if !errors.As(err, &se) || se.Code != "BAD_REQUEST" {
		t.Fatalf("%s: err = %v, want a BAD_REQUEST SubmitError", what, err)
	}
	for _, leak := range []string{"https://", "sig=", "worker/v1"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("%s: the refusal leaks %q: %v", what, leak, err)
		}
	}
}

func TestPodBackend_Identity(t *testing.T) {
	t.Parallel()
	x := newBRig(t)
	if x.b.Name() != provider.BackendRunPodPod || x.b.Engine() != provider.EngineYuE2 || x.b.ColdStartS() != 0 || x.b.MaxRuntime() != 6*time.Hour {
		t.Errorf("identity: %s %s %d %v", x.b.Name(), x.b.Engine(), x.b.ColdStartS(), x.b.MaxRuntime())
	}
	if !x.b.CancelledWhileQueuedIsNeverRan() {
		t.Error("the pod backend pins cancelled-while-queued as never ran")
	}
	var _ provider.ShapePinner = x.b
	fake := x.backend(t.TempDir(), func(o *pods.BackendOptions) { o.Name = provider.BackendFakePod })
	if fake.Name() != provider.BackendFakePod {
		t.Errorf("fake name %q", fake.Name())
	}
	if x.b.Queue() != x.q {
		t.Error("the backend must share the poller's queue")
	}
}

func TestPodBackend_PollStates(t *testing.T) {
	t.Parallel()
	x := newBRig(t)
	x.pod(rigPod, podName, "ready")

	// queued in memory
	queued := x.mustSubmit(rigBatch)
	if st := x.status(queued); st.State != provider.Queued || st.ExecutionMS != nil {
		t.Fatalf("queued: %+v", st)
	}
	if a := x.assignment(queued); a.QueuedAt.IsZero() || a.PodID != "" || !x.q.Has(queued) {
		t.Fatalf("assignment %+v", a)
	}
	// handed, not done
	resp := decodeResp(t, x.poll(x.link(rigPod), idle()))
	if resp.Action != "take" {
		t.Fatalf("hand-out: %+v", resp)
	}
	if st := x.status(queued); st.State != provider.Running || st.ExecutionMS != nil {
		t.Fatalf("running: %+v", st)
	}
	// done ok: the server's own measure of the execution
	x.clk.Advance(61234 * time.Millisecond)
	decodeResp(t, x.poll(x.link(rigPod), idle().with("done", done(queued, "ok", 1))))
	st := x.status(queued)
	if st.State != provider.Completed || st.ExecutionMS == nil || *st.ExecutionMS != 61234 {
		t.Fatalf("completed: %+v", st)
	}
	if st.Output != nil {
		t.Error("a pod poll carries no output: the result comes through the upload route")
	}

	// done error
	failed := x.mustSubmit(rigBatch)
	decodeResp(t, x.poll(x.link(rigPod), idle()))
	x.clk.Advance(5 * time.Second)
	decodeResp(t, x.poll(x.link(rigPod), idle().with("done", done(failed, "error", 1))))
	st = x.status(failed)
	if st.State != provider.Failed || st.ExecutionMS == nil || *st.ExecutionMS != 5000 {
		t.Fatalf("failed: %+v", st)
	}

	// lost: a failure without an execution time (settled at its time bound, rule 3) naming the code
	lost := x.mustSubmit(rigBatch)
	decodeResp(t, x.poll(x.link(rigPod), idle()))
	if _, err := pods.MarkLost(bg(), x.st.W, rigPod, "WORKER_LOST", x.clk.Now()); err != nil {
		t.Fatal(err)
	}
	st = x.status(lost)
	if st.State != provider.Failed || st.ExecutionMS != nil || st.ErrorText != "WORKER_LOST" {
		t.Fatalf("lost: %+v", st)
	}

	// unknown to the database
	if st, err := x.b.Poll(bg(), provider.Ref{Backend: x.b.Name(), ID: "01900000-0000-7000-8000-0000000cffff"}); err != nil || st.State != provider.NotFound {
		t.Fatalf("unknown: %+v, %v", st, err)
	}
}

func TestPodBackend_PollNeverCallsRunPod(t *testing.T) {
	t.Parallel()
	x := newBRig(t)
	att := x.mustSubmit(rigBatch)
	x.status(att)
	if err := x.b.Cancel(bg(), provider.Ref{ID: att}); err != nil {
		t.Fatal(err)
	}
	x.status(att)
	for _, op := range []string{podtest.OpCreate, podtest.OpGet, podtest.OpList, podtest.OpDelete, podtest.OpCatalog, podtest.OpBilling} {
		if n := x.srv.Calls(op); n != 0 {
			t.Errorf("%s called %d times by Submit, Poll or Cancel", op, n)
		}
	}
}

func TestPodBackend_SubmitRefusals(t *testing.T) {
	t.Parallel()
	x := newBRig(t)
	refused := func(what string, batchID string, timeoutS int) {
		t.Helper()
		before, queuedBefore := x.count(`SELECT COUNT(*) FROM pod_assignments`), x.q.Len(batchID)
		_, _, err := x.submit(batchID, timeoutS)
		wantRefusal(t, err, what)
		if x.count(`SELECT COUNT(*) FROM pod_assignments`) != before || x.q.Len(batchID) != queuedBefore {
			t.Errorf("%s: a refused take left an assignment or a queued spec", what)
		}
	}

	// no pod yet: nothing bounds the take, it is accepted
	x.mustSubmit(rigBatch)

	// the deadline: now + timeout + K + 30 s must fit before the batch's deadline
	x.pod(rigPod, podName, "ready") // deadline = rigNow + 5 h
	x.clk.Set(x.deadl.Add(-(186 + 15 + 30) * time.Second))
	if _, _, err := x.submit(rigBatch, 186); err != nil {
		t.Fatalf("exactly at the margin must fit: %v", err)
	}
	x.clk.Set(x.deadl.Add(-(186 + 15 + 30) * time.Second).Add(time.Second))
	refused("deadline", rigBatch, 186)
	if _, _, err := x.submit(rigBatch, 60); err != nil {
		t.Fatalf("a shorter take still fits: %v", err)
	}
	x.clk.Set(rigNow)

	// the re-creation is used: a second pod identity that is gone
	x.batch(rigBatch2, "est2")
	x.podOf(rigPod2, "mm-0190abcd5678", rigBatch2, "ready")
	x.mustSubmit(rigBatch2)
	if err := pods.InsertIntent(bg(), x.st.W, pods.NewPod{ID: "01900000-0000-7000-8000-00000000d003", Name: "mm-0190abcd9999", Kind: pods.KindBatch, BatchID: rigBatch2, N: 2, GPUTry: 1,
		GPUType: gpu4090, ImageDigest: testDigest, PricePPH: 300000, DeadlineAt: x.deadl, PollExp: pods.PollExpFor(x.deadl), At: rigNow}); err != nil {
		t.Fatal(err)
	}
	x.mustSubmit(rigBatch2) // the second pod is only an intent: it may still serve
	if err := pods.Transition(bg(), x.st.W, "01900000-0000-7000-8000-00000000d003", "intent", "terminated", pods.Move{At: rigNow}); err != nil {
		t.Fatal(err)
	}
	refused("re-creation used", rigBatch2, 186)

	// the pod overhead was refused (C396.3): the intent was terminated BUDGET_EXCEEDED
	x.batch("01900000-0000-7000-8000-00000000b003", "est3")
	const b3 = "01900000-0000-7000-8000-00000000b003"
	x.podOf("01900000-0000-7000-8000-00000000d004", "mm-0190abcdaaaa", b3, "intent")
	x.mustSubmit(b3)
	if err := pods.Transition(bg(), x.st.W, "01900000-0000-7000-8000-00000000d004", "intent", "terminated", pods.Move{At: rigNow, Code: "BUDGET_EXCEEDED"}); err != nil {
		t.Fatal(err)
	}
	refused("overhead refused", b3, 186)

	// the other batches are unaffected, a take of an unknown attempt is a plain error
	x.mustSubmit(rigBatch)
	if _, err := x.b.Submit(bg(), x.spec("01900000-0000-7000-8000-0000000cdead", 186)); err == nil {
		t.Error("an attempt with no job must not be accepted")
	}
}

func TestPodBackend_SubmitRefusalsByShape(t *testing.T) {
	t.Parallel()
	x := newBRig(t)
	att := x.attempt(rigBatch)
	base := x.spec(att, 186)
	for name, mod := range map[string]func(*provider.TakeSpec){
		"no timeout":   func(s *provider.TakeSpec) { s.TimeoutS = 0 },
		"no attempt":   func(s *provider.TakeSpec) { s.AttemptID = "" },
		"other engine": func(s *provider.TakeSpec) { s.Engine = "acestep15" },
		"unbuildable":  func(s *provider.TakeSpec) { s.Ears = &provider.EarsSpec{BudgetS: 10} }, // /2 features on a /1 wire
		"fetched input": func(s *provider.TakeSpec) {
			s.Inputs = map[string]provider.InputRef{provider.InputSrcAudio: {URL: rigURL + "/media/x?sig=s", SHA256: strings.Repeat("a", 64), Bytes: 10}}
		},
	} {
		s := base
		mod(&s)
		_, err := x.b.Submit(bg(), s)
		wantRefusal(t, err, name)
	}
	if x.count(`SELECT COUNT(*) FROM pod_assignments`) != 0 || x.q.Len(rigBatch) != 0 {
		t.Error("a refused take must leave nothing behind")
	}
	if _, err := x.b.Submit(bg(), base); err != nil { // the unmodified take is fine
		t.Fatal(err)
	}
}

func TestPodBackend_RejectsInputs(t *testing.T) {
	t.Parallel()
	x := newBRig(t)
	att := x.attempt(rigBatch)
	s := x.spec(att, 186)
	s.Inputs = map[string]provider.InputRef{provider.InputReferenceAudio: {URL: rigURL + "/media/y?sig=s", SHA256: strings.Repeat("b", 64), Bytes: 1}}
	_, err := x.b.Submit(bg(), s)
	wantRefusal(t, err, "inputs")
	if x.q.Len(rigBatch) != 0 {
		t.Error("queued")
	}
}

// TestPodBackend_RestartIsNeverRanRequeue: after a restart the in-memory takes are gone; an unhanded assignment answers Cancelled with no
// execution (pinned as never ran for this backend), is closed not_handed, and a take submitted by the new process is queued as usual.
func TestPodBackend_RestartIsNeverRanRequeue(t *testing.T) {
	t.Parallel()
	x := newBRig(t)
	att := x.mustSubmit(rigBatch)
	x.clk.Advance(time.Minute)
	// the process restarts: a new backend and queue over the same database
	x.q = pods.NewQueue()
	x.h = x.handler(-1)
	b2 := x.backend(t.TempDir(), func(o *pods.BackendOptions) { o.Queue = x.q })
	st, err := b2.Poll(bg(), provider.Ref{ID: att})
	if err != nil || st.State != provider.Cancelled || st.ExecutionMS != nil {
		t.Fatalf("after restart: %+v, %v", st, err)
	}
	if a := x.assignment(att); a.Status != pods.AssignNotHanded || a.PodID != "" || a.DoneAt == nil {
		t.Fatalf("assignment %+v", a)
	}
	if st, _ := b2.Poll(bg(), provider.Ref{ID: att}); st.State != provider.Cancelled {
		t.Errorf("a second poll: %+v", st)
	}
	if !b2.CancelledWhileQueuedIsNeverRan() {
		t.Error("the shape is what makes the runner requeue instead of settling at a time bound")
	}
	// a take of the new process is queued and stays queued
	x.b = b2
	again := x.mustSubmit(rigBatch)
	if st := x.status(again); st.State != provider.Queued {
		t.Errorf("new take: %+v", st)
	}
	// a handed take survives a restart as Running (its pod, not memory, holds it)
	x.pod(rigPod, podName, "ready")
	decodeResp(t, x.poll(x.link(rigPod), idle()))
	x.q = pods.NewQueue()
	x.h = x.handler(-1)
	b3 := x.backend(t.TempDir(), func(o *pods.BackendOptions) { o.Queue = x.q })
	if st, _ := b3.Poll(bg(), provider.Ref{ID: again}); st.State != provider.Running {
		t.Errorf("handed take after restart: %+v", st)
	}
}

func TestPodBackend_CancelUnhandedIsNeverRan(t *testing.T) {
	t.Parallel()
	x := newBRig(t)
	att := x.mustSubmit(rigBatch)
	if err := x.b.Cancel(bg(), provider.Ref{ID: att}); err != nil {
		t.Fatal(err)
	}
	if x.q.Has(att) || x.q.Len(rigBatch) != 0 {
		t.Error("the take must leave the queue")
	}
	if a := x.assignment(att); a.Status != pods.AssignCancelled || a.PodID != "" {
		t.Fatalf("assignment %+v", a)
	}
	if st := x.status(att); st.State != provider.Cancelled || st.ExecutionMS != nil {
		t.Fatalf("status %+v", st)
	}
	if err := x.b.Cancel(bg(), provider.Ref{ID: att}); err != nil {
		t.Errorf("a second cancel: %v", err)
	}
	if err := x.b.Cancel(bg(), provider.Ref{ID: "01900000-0000-7000-8000-0000000cdead"}); err != nil {
		t.Errorf("an unknown attempt is already gone: %v", err)
	}
}

func TestPodBackend_CancelHandedIsListedToThePod(t *testing.T) {
	t.Parallel()
	x := newBRig(t)
	x.pod(rigPod, podName, "ready")
	att := x.mustSubmit(rigBatch)
	decodeResp(t, x.poll(x.link(rigPod), idle()))
	if err := x.b.Cancel(bg(), provider.Ref{ID: att}); err != nil {
		t.Fatal(err)
	}
	resp := decodeResp(t, x.poll(x.link(rigPod), state("busy").with("attempt_id", att)))
	if len(resp.Cancel) != 1 || resp.Cancel[0] != att {
		t.Fatalf("cancel list = %v", resp.Cancel)
	}
	if a := x.assignment(att); a.Status != "" || a.PodID == "" {
		t.Errorf("a handed take is not closed by Cancel: %+v", a)
	}
	// a finished take: nothing to do
	x.clk.Advance(time.Second)
	decodeResp(t, x.poll(x.link(rigPod), idle().with("done", done(att, "ok", 1))))
	if err := x.b.Cancel(bg(), provider.Ref{ID: att}); err != nil {
		t.Fatal(err)
	}
	if x.assignment(att).Status != pods.AssignOK {
		t.Error("done stays done")
	}
}

func TestPodBackend_PurgeQueue(t *testing.T) {
	t.Parallel()
	x := newBRig(t)
	a, b := x.mustSubmit(rigBatch), x.mustSubmit(rigBatch)
	if err := x.b.PurgeQueue(bg()); err != nil {
		t.Fatal(err)
	}
	for _, att := range []string{a, b} {
		if x.assignment(att).Status != pods.AssignCancelled || x.status(att).State != provider.Cancelled {
			t.Errorf("%s not cancelled", att)
		}
	}
	if x.q.Len(rigBatch) != 0 {
		t.Error("queue not empty")
	}
}

func TestPodBackend_SweepIsEmpty(t *testing.T) {
	t.Parallel()
	x := newBRig(t)
	rep, err := x.b.Sweep(bg())
	if err != nil || len(rep.Workers) != 0 || rep.Health != nil || rep.Endpoint != nil || len(rep.Drift) != 0 || rep.Skew != nil || !rep.ObservedAt.Equal(rigNow) {
		t.Fatalf("sweep = %+v, %v", rep, err)
	}
	if err := x.b.RevertDrift(bg(), []provider.Drift{{Field: "x"}}); !errors.Is(err, provider.ErrNotPermitted) {
		t.Errorf("RevertDrift: %v", err)
	}
	if n := x.srv.Calls(podtest.OpList) + x.srv.Calls(podtest.OpGet); n != 0 {
		t.Errorf("a sweep must not call RunPod: %d calls", n)
	}
}

func TestPodBackend_Capabilities(t *testing.T) {
	t.Parallel()
	x := newBRig(t)
	if c := x.b.Capabilities(); !c.ScaleToZero || c.RevertDrift || c.Billing != "none" {
		t.Errorf("key, billing not readable: %+v", c)
	}
	x.billing = true
	if c := x.b.Capabilities(); !c.ScaleToZero || c.Billing != "hour" {
		t.Errorf("key, billing readable: %+v", c)
	}
	x.key = false
	if c := x.b.Capabilities(); c.ScaleToZero || c.Billing != "none" {
		t.Errorf("no key: %+v", c)
	}
}

// TestPodBackend_BillingReadFromTheProbeRow: without an override the backend reads the latest probe row of pod_safety_log.
func TestPodBackend_BillingReadFromTheProbeRow(t *testing.T) {
	t.Parallel()
	x := newBRig(t, func(o *pods.BackendOptions) { o.BillingReadable = nil })
	if c := x.b.Capabilities(); c.Billing != "none" {
		t.Fatalf("no probe row: %+v", c)
	}
	probe := func(verdict string, readable bool) {
		t.Helper()
		if err := pods.AppendSafety(bg(), x.st.W, pods.SafetyEntry{Kind: "probe", Verdict: verdict, CreatedBy: "cli:alice", At: rigNow, Detail: map[string]any{"billing_readable": readable}}); err != nil {
			t.Fatal(err)
		}
	}
	probe("ok", true)
	if c := x.b.Capabilities(); c.Billing != "hour" {
		t.Errorf("probe ok, readable: %+v", c)
	}
	probe("ok", false)
	if c := x.b.Capabilities(); c.Billing != "none" {
		t.Errorf("the latest probe says unreadable: %+v", c)
	}
	probe("failed", true)
	if c := x.b.Capabilities(); c.Billing != "none" {
		t.Errorf("a failed probe proves nothing, the older row says unreadable: %+v", c)
	}
}

func TestPodBackend_BillingOwnPodsOnly(t *testing.T) {
	t.Parallel()
	x := newBRig(t)
	h := func(n int) time.Time { return rigNow.Add(time.Duration(n) * time.Hour) }
	x.srv.SetBilling([]podtest.BillRecord{
		{Start: h(0), End: h(1), PodID: "rp-mine", Total: "0.75", GPU: "0.66", Disk: "0.09"},
		{Start: h(0), End: h(1), PodID: "rp-other", Total: "9", GPU: "9"},
	})
	if _, err := x.b.Billing(bg(), h(0), h(2)); !errors.Is(err, provider.ErrNotPermitted) {
		t.Fatalf("billing not readable: %v", err)
	}
	x.billing = true
	// no pod of ours: no call, no buckets
	if got, err := x.b.Billing(bg(), h(0), h(2)); err != nil || len(got) != 0 || x.srv.Calls(podtest.OpBilling) != 0 {
		t.Fatalf("no own pods: %v, %v, %d calls", got, err, x.srv.Calls(podtest.OpBilling))
	}
	x.pod(rigPod, podName, "ready")
	x.exec(`UPDATE pods SET provider_ref = 'rp-mine' WHERE id = ?`, rigPod)
	got, err := x.b.Billing(bg(), h(0), h(2))
	if err != nil || len(got) != 1 || got[0].Total != money.MicroUSD(750000) || got[0].Compute != 660000 || got[0].Disk != 90000 {
		t.Fatalf("billing = %+v, %v", got, err)
	}
	if r, _ := x.srv.Last(podtest.OpBilling); !strings.Contains(r.Query, "podId=rp-mine") || strings.Contains(r.Query, "rp-other") {
		t.Errorf("query %q must name only own pods", r.Query)
	}
	// a pod that ended before the window is not asked for
	x.exec(`UPDATE pods SET provider_ref = 'rp-mine' WHERE id = ?`, rigPod)
	calls := x.srv.Calls(podtest.OpBilling)
	if got, err := x.b.Billing(bg(), h(-5), h(-4)); err != nil || len(got) != 0 {
		t.Fatalf("window before the pod: %v, %v", got, err)
	}
	if x.srv.Calls(podtest.OpBilling) != calls {
		t.Error("a pod created after the window must not be billed for it")
	}
	x.key = false
	if _, err := x.b.Billing(bg(), h(0), h(2)); !errors.Is(err, provider.ErrNotPermitted) {
		t.Errorf("no key: %v", err)
	}
}

func TestPodBackend_Quote(t *testing.T) {
	t.Parallel()
	x := newBRig(t)
	x.srv.SetPrice(gpu4090, "0.34")
	x.srv.SetPrice("NVIDIA RTX A5000", "0.27")
	q, err := x.b.Quote(bg())
	if err != nil {
		t.Fatal(err)
	}
	if q.Backend != provider.BackendRunPodPod || q.ListPricePPH != 340000 || len(q.GPUClasses) != 2 || q.GPUClasses[0] != gpu4090 || !q.ObservedAt.Equal(rigNow) {
		t.Errorf("quote = %+v", q)
	}
	e := q.Endpoint
	if e.Type != provider.EndpointTypePod || e.WorkersMax != 1 || e.GPUCount != 1 || e.ImageDigest != "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		t.Errorf("endpoint = %+v", e)
	}
	if r := provider.QuoteCheck(q, rigNow, provider.GateConfig{Mode: provider.ModeRunPod}); r != "" {
		t.Errorf("the backend's own quote must pass its gate: %q", r)
	}
	// the dearest of the list prices every pod, whatever its position
	x.srv.SetPrice("NVIDIA RTX A5000", "0.52")
	if q, err := x.b.Quote(bg()); err != nil || q.ListPricePPH != 520000 {
		t.Errorf("max price: %+v, %v", q, err)
	}
	// a missing or unusable price makes the quote unusable
	x.srv.SetPrice("NVIDIA RTX A5000", "0")
	var pe *pods.PriceError
	if _, err := x.b.Quote(bg()); !errors.As(err, &pe) {
		t.Errorf("zero price: %v", err)
	}
	// the key is gone: the catalog is not read; the error is "not permitted" (keys_absent at the gate)
	nokey := x.backend(t.TempDir())
	if _, err := nokey.Quote(bg()); !errors.Is(err, provider.ErrNotPermitted) {
		t.Errorf("no key: %v", err)
	}
	if _, err := x.backend(t.TempDir(), func(o *pods.BackendOptions) { o.GPUTypes = nil }).Quote(bg()); !errors.Is(err, pods.ErrNoGPUTypes) {
		t.Errorf("no types: %v", err)
	}
	if _, err := x.backend(t.TempDir(), func(o *pods.BackendOptions) { o.API = nil }).Quote(bg()); !errors.Is(err, provider.ErrNotPermitted) {
		t.Errorf("no api: %v", err)
	}
}

func TestImageDigest(t *testing.T) {
	t.Parallel()
	d := "sha256:" + strings.Repeat("c", 64)
	for in, want := range map[string]string{"ghcr.io/example/mm-worker@" + d: d, "ghcr.io/example/mm-worker:latest": "", "": "", "x@md5:abc": "", "x@sha256:": "", "x@sha256:xyz": "", "x@sha256:" + strings.Repeat("A", 64): "", "x@sha256:" + strings.Repeat("c", 63): "", "x@sha256:" + strings.Repeat("c", 65): ""} {
		if got := pods.ImageDigest(in); got != want {
			t.Errorf("ImageDigest(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPodBackend_ScaleToZero(t *testing.T) {
	t.Parallel()
	x := newBRig(t)
	x.srv.AddPod(podtest.Pod{ID: "p-mine", Name: "mm-0190abcd1234", Status: "RUNNING"})
	x.srv.AddPod(podtest.Pod{ID: "p-canary", Name: "mm-c0190abcd123", Status: "RUNNING"})
	x.srv.AddPod(podtest.Pod{ID: "p-foreign", Name: "jupyter-bob", Status: "RUNNING"})
	x.srv.AddPod(podtest.Pod{ID: "p-done", Name: "mm-0190abcd0000", Status: "TERMINATED"})
	if err := x.b.ScaleToZero(bg()); err != nil {
		t.Fatal(err)
	}
	if !x.srv.Deleted("p-mine") || !x.srv.Deleted("p-canary") {
		t.Error("every live mm- pod must be deleted")
	}
	if x.srv.Deleted("p-foreign") {
		t.Error("a pod that is not ours must never be touched")
	}
	if x.srv.Deleted("p-done") {
		t.Error("a terminated pod needs no delete")
	}
	// the key file is gone: not permitted, and nothing is called
	x.key = false
	calls := x.srv.Calls(podtest.OpList)
	if err := x.b.ScaleToZero(bg()); !errors.Is(err, provider.ErrNotPermitted) || x.srv.Calls(podtest.OpList) != calls {
		t.Errorf("no key: %v", err)
	}
	// one failed delete does not stop the others; the error is joined and carries no id
	x.key = true
	x.srv.AddPod(podtest.Pod{ID: "p-a", Name: "mm-0190abcd1111", Status: "RUNNING"})
	x.srv.AddPod(podtest.Pod{ID: "p-b", Name: "mm-0190abcd2222", Status: "RUNNING"})
	x.srv.Fault(podtest.OpDelete, podtest.Fault{Status: 500})
	err := x.b.ScaleToZero(bg())
	if err == nil {
		t.Fatal("a failed delete must be reported")
	}
	if x.srv.Deleted("p-a") == x.srv.Deleted("p-b") {
		t.Error("exactly one delete failed: the other pod must still be deleted")
	}
	for _, leak := range []string{"p-a", "p-b", x.srv.URL} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the error leaks %q: %v", leak, err)
		}
	}
	x.srv.Fault(podtest.OpList, podtest.Fault{Status: 403})
	if err := x.b.ScaleToZero(bg()); !errors.Is(err, provider.ErrAccess) {
		t.Errorf("a refused key: %v", err)
	}
}

func TestPodBackend_FakeQuoteHasADigest(t *testing.T) {
	t.Parallel()
	x := newBRig(t, func(o *pods.BackendOptions) { o.Name = provider.BackendFakePod; o.Image = "" })
	x.srv.SetPrice(gpu4090, "0.34")
	x.srv.SetPrice("NVIDIA RTX A5000", "0.27")
	q, err := x.b.Quote(bg())
	if err != nil {
		t.Fatal(err)
	}
	if r := provider.QuoteCheck(q, rigNow, provider.GateConfig{Mode: provider.ModeRunPod}); r != "" {
		t.Errorf("an unpinned fake pod quote must pass its gate: %q (%+v)", r, q.Endpoint)
	}
	// the real backend with an unpinned image fails the gate
	y := newBRig(t, func(o *pods.BackendOptions) { o.Image = "ghcr.io/example/mm-worker:latest" })
	y.srv.SetPrice(gpu4090, "0.34")
	y.srv.SetPrice("NVIDIA RTX A5000", "0.27")
	q, err = y.b.Quote(bg())
	if err != nil {
		t.Fatal(err)
	}
	if r := provider.QuoteCheck(q, rigNow, provider.GateConfig{Mode: provider.ModeRunPod}); r != provider.ReasonEndpointConfig {
		t.Errorf("unpinned real image: %q", r)
	}
}
