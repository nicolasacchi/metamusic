package pods_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestPodPoll_Matrix: the checks of C395.1 in their documented order, each with its status and problem code.
func TestPodPoll_Matrix(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	r.pod(rigPod, "mm-0190aaaaaaaa", "ready")
	good := r.link(rigPod)
	u, _ := url.Parse(good)
	other := pods.NewSigner([]byte("another-key-another-key-another-key"))
	expired := rigPod2
	r.deadl = rigNow.Add(-time.Hour) // a pod whose link is long expired
	r.podTry(expired, "mm-0190bbbbbbbb", rigBatch, "ready", 2)
	expiredLink := r.link(expired)
	r.deadl = rigNow.Add(5 * time.Hour)
	badExp := strings.Replace(good, "exp="+u.Query().Get("exp"), "exp=1", 1)
	unknown := strings.TrimPrefix(r.sig.URL(rigURL, "01900000-0000-7000-8000-00000000dfff", pods.PollExpFor(r.deadl)), rigURL)
	wrongExp := strings.TrimPrefix(r.sig.URL(rigURL, rigPod, pods.PollExpFor(r.deadl)+1), rigURL) // valid MAC, not this pod's expiry
	body := idle()
	tests := []struct {
		name   string
		method string
		target string
		body   any
		status int
		code   string
	}{
		{"GET refused", http.MethodGet, good, body, 405, "METHOD_NOT_ALLOWED"},
		{"bad path", http.MethodPost, "/worker/v1/pods/x/poll" + good[strings.Index(good, "?"):], body, 404, "NOT_FOUND"},
		{"extra path", http.MethodPost, strings.Replace(good, "/poll", "/poll/x", 1), body, 404, "NOT_FOUND"},
		{"no query", http.MethodPost, u.Path, body, 403, "POD_LINK_INVALID"},
		{"bad mac", http.MethodPost, other.URL(rigURL, rigPod, pods.PollExpFor(r.deadl))[len(rigURL):], body, 403, "POD_LINK_INVALID"},
		{"tampered exp", http.MethodPost, badExp, body, 403, "POD_LINK_INVALID"},
		{"expired", http.MethodPost, expiredLink, body, 403, "POD_LINK_EXPIRED"},
		{"unknown pod", http.MethodPost, unknown, body, 403, "POD_LINK_INVALID"},
		{"mac for another expiry", http.MethodPost, wrongExp, body, 403, "POD_LINK_INVALID"},
		{"not json", http.MethodPost, good, "nope", 400, "BAD_REQUEST"},
		{"trailing data", http.MethodPost, good, `{"state":"idle","agent":{"version":"1","build":"b"},"boot":null,"attempt_id":null,"done":null} {}`, 400, "BAD_REQUEST"},
		{"unknown field", http.MethodPost, good, idle().with("extra", 1), 400, "BAD_REQUEST"},
		{"unknown nested field", http.MethodPost, good, idle().with("agent", map[string]any{"version": "1", "build": "b", "x": 1}), 400, "BAD_REQUEST"},
		{"missing key", http.MethodPost, good, `{"state":"idle","agent":{"version":"1","build":"b"}}`, 400, "BAD_REQUEST"},
		{"bad state", http.MethodPost, good, state("sleeping"), 400, "BAD_REQUEST"},
		{"bad attempt id", http.MethodPost, good, idle().with("attempt_id", "nope"), 400, "BAD_REQUEST"},
		{"bad done status", http.MethodPost, good, idle().with("done", done("01900000-0000-7000-8000-0000000c0001", "maybe", 1)), 400, "BAD_REQUEST"},
		{"negative execution", http.MethodPost, good, idle().with("done", done("01900000-0000-7000-8000-0000000c0001", "ok", -1)), 400, "BAD_REQUEST"},
		{"empty agent version", http.MethodPost, good, idle().with("agent", map[string]any{"version": "", "build": "b"}), 400, "BAD_REQUEST"},
		{"too big", http.MethodPost, good, `{"state":"idle","pad":"` + strings.Repeat("x", pods.MaxPollBody) + `"}`, 413, "BODY_TOO_LARGE"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var raw string
			switch b := tc.body.(type) {
			case string:
				raw = b
			default:
				raw = string(mustJSON(t, b))
			}
			w := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.target, strings.NewReader(raw))
			r.h.ServeHTTP(w, req)
			if w.Code != tc.status || problemCode(t, w) != tc.code {
				t.Fatalf("status %d code %q (%s), want %d %s", w.Code, problemCode(t, w), w.Body, tc.status, tc.code)
			}
		})
	}
	if n := r.count(`SELECT COUNT(*) FROM pods WHERE last_poll_at IS NOT NULL`); n != 0 {
		t.Fatalf("a refused poll recorded an observation (%d pods)", n)
	}
	// a chunked body over the cap (no Content-Length) is refused too
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, good, strings.NewReader(strings.Repeat("x", pods.MaxPollBody+10)))
	req.ContentLength = -1
	r.h.ServeHTTP(w, req)
	if w.Code != 413 {
		t.Fatalf("chunked oversize: %d", w.Code)
	}
}

func TestPodPoll_StopsTerminalPodsAndCancelledBatches(t *testing.T) {
	t.Parallel()
	for _, st := range []string{"terminating", "orphaned"} {
		t.Run(st, func(t *testing.T) {
			r := newSRig(t)
			r.pod(rigPod, "mm-0190aaaaaaaa", st)
			r.queueTake(rigBatch, 200)
			resp := decodeResp(t, r.poll(r.link(rigPod), idle()))
			if resp.Action != "stop" || !noTake(resp) || resp.Cancel == nil {
				t.Fatalf("%s: %+v", st, resp)
			}
			if r.q.Len(rigBatch) != 1 {
				t.Fatal("a stopped pod took from the queue")
			}
		})
	}
	t.Run("terminated", func(t *testing.T) {
		r := newSRig(t)
		r.pod(rigPod, "mm-0190aaaaaaaa", "terminating")
		r.setState(rigPod, "terminated")
		if resp := decodeResp(t, r.poll(r.link(rigPod), idle())); resp.Action != "stop" {
			t.Fatalf("%+v", resp)
		}
	})
	t.Run("batch cancelled", func(t *testing.T) {
		r := newSRig(t)
		r.pod(rigPod, "mm-0190aaaaaaaa", "ready")
		att := r.queueTake(rigBatch, 200)
		r.exec(`UPDATE jobs SET cancel_requested_at = ?, cancel_requested_by = 'human:alice' WHERE id = (SELECT job_id FROM job_attempts WHERE id = ?)`, "2026-11-21T12:00:01.000Z", att)
		if resp := decodeResp(t, r.poll(r.link(rigPod), idle())); resp.Action != "stop" {
			t.Fatalf("%+v", resp)
		}
	})
	t.Run("batch no longer active", func(t *testing.T) {
		r := newSRig(t)
		r.pod(rigPod, "mm-0190aaaaaaaa", "ready")
		r.queueTake(rigBatch, 200)
		r.exec(`UPDATE batches SET state = 'finished', finished_at = '2026-11-21T12:00:01.000Z' WHERE id = ?`, rigBatch)
		if resp := decodeResp(t, r.poll(r.link(rigPod), idle())); resp.Action != "stop" {
			t.Fatalf("%+v", resp)
		}
	})
}

// TestPodPoll_HandsOutOnce: one queued take, two pollers racing: exactly one gets it; the assignment records pod and time once.
func TestPodPoll_HandsOutOnce(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	r.pod(rigPod, "mm-0190aaaaaaaa", "ready")
	r.podTry(rigPod2, "mm-0190bbbbbbbb", rigBatch, "ready", 2)
	att := r.queueTake(rigBatch, 200)
	results := make(chan pods.PollResponse, 2)
	for _, id := range []string{rigPod, rigPod2} {
		go func() {
			w := r.poll(r.link(id), idle())
			var resp pods.PollResponse
			_ = json.Unmarshal(w.Body.Bytes(), &resp)
			results <- resp
		}()
	}
	var takes, waits int
	for range 2 {
		switch resp := <-results; resp.Action {
		case "take":
			takes++
			if noTake(resp) || resp.WaitS != 0 {
				t.Fatalf("take answer %+v", resp)
			}
		case "wait":
			waits++
		}
	}
	if takes != 1 || waits != 1 {
		t.Fatalf("takes %d waits %d", takes, waits)
	}
	a, err := pods.GetAssignment(context.Background(), r.st.R, att)
	if err != nil || a.PodID == "" || a.HandedAt == nil || a.DoneAt != nil {
		t.Fatalf("assignment %+v err %v", a, err)
	}
	if r.q.Len(rigBatch) != 0 {
		t.Fatal("the take is still queued")
	}
}

// TestPodPoll_TakeIsTheWorkerInput: the take is exactly provider.BuildWorkerInput of the queued spec.
func TestPodPoll_TakeIsTheWorkerInput(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	r.pod(rigPod, "mm-0190aaaaaaaa", "ready")
	att := r.take(rigBatch)
	spec := r.spec(att, 200)
	r.q.Add(rigBatch, spec)
	want, err := provider.BuildWorkerInput(spec)
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeResp(t, r.poll(r.link(rigPod), idle()))
	if resp.Action != "take" || !jsonEqual(t, resp.Take, want) {
		t.Fatalf("%s", resp.Take)
	}
	if resp.DeadlineUnix != r.deadl.Unix() {
		t.Fatalf("deadline_unix %d", resp.DeadlineUnix)
	}
	// a /2 spec is built as /2
	att2 := r.take(rigBatch)
	spec2 := r.spec(att2, 200)
	spec2.Wire = provider.ContractV2
	r.q.Add(rigBatch, spec2)
	want2, err := provider.BuildWorkerInput(spec2)
	if err != nil {
		t.Fatal(err)
	}
	r.exec(`UPDATE pod_assignments SET done_at = ?, status = 'ok', execution_ms = 1 WHERE attempt_id = ?`, store.FormatTime(rigNow), att)
	r.setState(rigPod, "busy")
	resp = decodeResp(t, r.poll(r.link(rigPod), idle().with("done", done(att, "ok", 5))))
	if resp.Action != "take" || !jsonEqual(t, resp.Take, want2) {
		t.Fatalf("v2: %+v", resp)
	}
}

func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// TestPodPoll_OnlyReadyPodsGetTakes: a booting pod, or a busy pod that reports no done, is told to wait and the take stays queued.
func TestPodPoll_OnlyReadyPodsGetTakes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, podState, report string }{
		{"booting pod idle report", "booting", "idle"},
		{"booting report", "ready", "booting"},
		{"busy report", "ready", "busy"},
		{"busy pod idle without done", "busy", "idle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSRig(t)
			r.pod(rigPod, "mm-0190aaaaaaaa", tc.podState)
			r.queueTake(rigBatch, 200)
			resp := decodeResp(t, r.poll(r.link(rigPod), state(tc.report)))
			if resp.Action != "wait" || !noTake(resp) || resp.WaitS != pods.HoldS {
				t.Fatalf("%+v", resp)
			}
			if r.q.Len(rigBatch) != 1 {
				t.Fatal("the take left the queue")
			}
		})
	}
	t.Run("canary pods get nothing", func(t *testing.T) {
		r := newSRig(t)
		r.exec(`PRAGMA foreign_keys = ON`)
		if err := pods.InsertIntent(context.Background(), r.st.W, pods.NewPod{ID: rigPod, Name: "mm-c0190aaaaaa", Kind: pods.KindCanary, BatchID: rigBatch, N: 1, GPUTry: 1,
			GPUType: "g", ImageDigest: "d", PricePPH: 1, DeadlineAt: r.deadl, PollExp: pods.PollExpFor(r.deadl), At: rigNow}); err != nil {
			t.Fatal(err)
		}
		for _, to := range []string{"creating", "booting", "ready"} {
			r.setState(rigPod, to)
		}
		r.queueTake(rigBatch, 200)
		if resp := decodeResp(t, r.poll(r.link(rigPod), idle())); resp.Action != "wait" || r.q.Len(rigBatch) != 1 {
			t.Fatalf("%+v", resp)
		}
	})
}

// TestPodPoll_NoHandOutPastDeadline: a take that cannot finish (timeout + K + 30 s) before the pod's deadline stays queued.
func TestPodPoll_NoHandOutPastDeadline(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	r.pod(rigPod, "mm-0190aaaaaaaa", "ready")
	att := r.queueTake(rigBatch, 200)
	// K = 15: the last moment is deadline - (200 + 15 + 30) s
	last := r.deadl.Add(-245 * time.Second)
	r.clk.Set(last.Add(time.Second))
	if resp := decodeResp(t, r.poll(r.link(rigPod), idle())); resp.Action != "wait" || r.q.Len(rigBatch) != 1 {
		t.Fatalf("handed out one second too late: %+v", resp)
	}
	r.clk.Set(last)
	if resp := decodeResp(t, r.poll(r.link(rigPod), idle())); resp.Action != "take" {
		t.Fatalf("not handed out at the boundary: %+v", resp)
	}
	if a, _ := pods.GetAssignment(context.Background(), r.st.R, att); a.HandedAt == nil {
		t.Fatal("not recorded")
	}
}

// TestPodPoll_HoldsThenWaits: an idle poll with nothing queued is held HoldS seconds of the injected clock, then answered wait.
func TestPodPoll_HoldsThenWaits(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	r.pod(rigPod, "mm-0190aaaaaaaa", "ready")
	r.take(rigBatch) // a live job: with none left to run the batch's pods are told to stop
	out := make(chan pods.PollResponse, 1)
	r.h = r.handler(0)
	go func() {
		w := r.poll(r.link(rigPod), idle())
		var resp pods.PollResponse
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		out <- resp
	}()
	for i := 0; i < pods.HoldS-1; i++ {
		r.clk.Advance(time.Second)
		time.Sleep(2 * time.Millisecond)
		select {
		case resp := <-out:
			t.Fatalf("answered after %d s: %+v", i+1, resp)
		default:
		}
	}
	// the timer was created when the handler started, possibly a few advances after the first one
	for i := 0; i < 20; i++ {
		r.clk.Advance(time.Second)
		select {
		case resp := <-out:
			if resp.Action != "wait" || resp.WaitS != pods.HoldS {
				t.Fatalf("%+v", resp)
			}
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
	t.Fatal("still held")
}

// TestPodPoll_WakesOnSubmit: a held poll is answered with the take the moment one is queued.
func TestPodPoll_WakesOnSubmit(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	r.pod(rigPod, "mm-0190aaaaaaaa", "ready")
	r.take(rigBatch) // a live job: with none left to run the batch's pods are told to stop
	out := make(chan pods.PollResponse, 1)
	r.h = r.handler(0)
	go func() {
		w := r.poll(r.link(rigPod), idle())
		var resp pods.PollResponse
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		out <- resp
	}()
	time.Sleep(20 * time.Millisecond)
	select {
	case resp := <-out:
		t.Fatalf("answered with nothing queued: %+v", resp)
	default:
	}
	r.queueTake(rigBatch, 200)
	select {
	case resp := <-out:
		if resp.Action != "take" {
			t.Fatalf("%+v", resp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("not woken")
	}
}

// TestPodPoll_HoldEndsWithTheRequest: a cancelled request context ends the hold.
func TestPodPoll_HoldEndsWithTheRequest(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	r.pod(rigPod, "mm-0190aaaaaaaa", "ready")
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, r.link(rigPod), bytes.NewReader(mustJSON(t, idle()))).WithContext(ctx)
	w := httptest.NewRecorder()
	finished := make(chan struct{})
	go func() { r.h.ServeHTTP(w, req); close(finished) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("hold ignored the request context")
	}
}

// TestPodPoll_DoneRecordedOnce: the done report sets status, agent_execution_ms and error_code once; a repeat or a different later
// report changes nothing.
func TestPodPoll_DoneRecordedOnce(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	r.pod(rigPod, "mm-0190aaaaaaaa", "ready")
	att := r.queueTake(rigBatch, 200)
	decodeResp(t, r.poll(r.link(rigPod), idle())) // hand-out
	r.setState(rigPod, "busy")
	r.clk.Advance(90 * time.Second)
	code := "OUT_OF_MEMORY"
	d := done(att, "error", 61234)
	d["error_code"] = code
	resp := decodeResp(t, r.poll(r.link(rigPod), state("idle").with("done", d)))
	if resp.Action != "wait" && resp.Action != "take" {
		t.Fatalf("%+v", resp)
	}
	a, _ := pods.GetAssignment(context.Background(), r.st.R, att)
	if a.Status != "error" || a.ErrorCode != code || a.AgentExecutionMS == nil || *a.AgentExecutionMS != 61234 || a.DoneAt == nil {
		t.Fatalf("%+v", a)
	}
	before := r.str(`SELECT done_at||status||execution_ms||agent_execution_ms||error_code FROM pod_assignments WHERE attempt_id = ?`, att)
	r.clk.Advance(time.Minute)
	decodeResp(t, r.poll(r.link(rigPod), state("idle").with("done", done(att, "ok", 1))))
	if after := r.str(`SELECT done_at||status||execution_ms||agent_execution_ms||error_code FROM pod_assignments WHERE attempt_id = ?`, att); after != before {
		t.Fatalf("a second done changed the row: %s -> %s", before, after)
	}
	// a done for an attempt that was never handed to this pod is ignored
	stray := r.take(rigBatch)
	decodeResp(t, r.poll(r.link(rigPod), state("busy").with("done", done(stray, "ok", 1))))
	if a, _ := pods.GetAssignment(context.Background(), r.st.R, stray); a.DoneAt != nil {
		t.Fatalf("a stray done was recorded: %+v", a)
	}
}

// TestPodPoll_ExecutionMeasuredByServer: execution_ms is done_at - handed_at on the server's clock, whatever the agent claims.
func TestPodPoll_ExecutionMeasuredByServer(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	r.pod(rigPod, "mm-0190aaaaaaaa", "ready")
	att := r.queueTake(rigBatch, 200)
	decodeResp(t, r.poll(r.link(rigPod), idle()))
	r.setState(rigPod, "busy")
	r.clk.Advance(75*time.Second + 250*time.Millisecond)
	decodeResp(t, r.poll(r.link(rigPod), state("idle").with("done", done(att, "ok", 1))))
	a, _ := pods.GetAssignment(context.Background(), r.st.R, att)
	if a.ExecutionMS == nil || *a.ExecutionMS != 75250 {
		t.Fatalf("execution_ms %v, want 75250", a.ExecutionMS)
	}
	if *a.AgentExecutionMS != 1 {
		t.Fatalf("agent_execution_ms %v", *a.AgentExecutionMS)
	}
}

// TestPodPoll_ObservationsRecorded: first_poll_at is set once, last_poll_at moves, last_reported and boot_json follow the report; the
// loop is nudged on the first poll.
func TestPodPoll_ObservationsRecorded(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	r.pod(rigPod, "mm-0190aaaaaaaa", "booting")
	boot := map[string]any{"gpu_name": "NVIDIA GeForce RTX 4090", "cuda": "12.8", "weights_ready": true, "load_s": 41.5}
	decodeResp(t, r.poll(r.link(rigPod), state("booting").with("boot", boot)))
	if r.nudges.Load() != 1 {
		t.Fatalf("nudges %d", r.nudges.Load())
	}
	first := r.str(`SELECT first_poll_at FROM pods WHERE id = ?`, rigPod)
	r.clk.Advance(30 * time.Second)
	decodeResp(t, r.poll(r.link(rigPod), state("idle")))
	row, err := pods.GetRow(context.Background(), r.st.R, rigPod)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.FormatTime(*row.FirstPollAt); got != first || !row.LastPollAt.Equal(rigNow.Add(30*time.Second)) || row.LastReported != "idle" {
		t.Fatalf("%+v", row)
	}
	if bj := r.str(`SELECT boot_json FROM pods WHERE id = ?`, rigPod); !strings.Contains(bj, `"weights_ready":true`) || !strings.Contains(bj, "12.8") {
		t.Fatalf("boot_json %s (kept across a poll without boot)", bj)
	}
	if r.nudges.Load() != 1 {
		t.Fatalf("a quiet poll nudged the loop (%d)", r.nudges.Load())
	}
	if n := r.count(`SELECT COUNT(*) FROM pods WHERE state <> 'booting'`); n != 0 {
		t.Fatal("the handler moved a pod state")
	}
}

// TestPodPoll_RestartLosesHandedTake: a booting report from a busy pod records the handed take lost WORKER_RESTARTED and hands nothing.
func TestPodPoll_RestartLosesHandedTake(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	r.pod(rigPod, "mm-0190aaaaaaaa", "ready")
	att := r.queueTake(rigBatch, 200)
	decodeResp(t, r.poll(r.link(rigPod), idle()))
	r.setState(rigPod, "busy")
	queued := r.queueTake(rigBatch, 200)
	n := r.nudges.Load()
	resp := decodeResp(t, r.poll(r.link(rigPod), state("booting")))
	if resp.Action != "wait" || !noTake(resp) {
		t.Fatalf("%+v", resp)
	}
	a, _ := pods.GetAssignment(context.Background(), r.st.R, att)
	if a.Status != "lost" || a.ErrorCode != "WORKER_RESTARTED" || a.DoneAt == nil || a.ExecutionMS != nil {
		t.Fatalf("%+v", a)
	}
	if q, _ := pods.GetAssignment(context.Background(), r.st.R, queued); q.PodID != "" || r.q.Len(rigBatch) != 1 {
		t.Fatalf("the queued take was touched: %+v", q)
	}
	if r.nudges.Load() != n+1 {
		t.Fatal("restart did not nudge")
	}
	if c := r.count(`SELECT COUNT(*) FROM pod_events WHERE pod_id = ? AND event = 'restart_reported' AND code = 'WORKER_RESTARTED'`, rigPod); c != 1 {
		t.Fatalf("restart events: %d", c)
	}
	if st := r.str(`SELECT state FROM pods WHERE id = ?`, rigPod); st != "busy" {
		t.Fatalf("handler moved the pod to %s", st)
	}
}

// TestPodPoll_CancelListed: a handed take the runner cancelled is listed in `cancel` until it is done; a take cancelled before
// hand-out is never listed (the backend drops it from memory).
func TestPodPoll_CancelListed(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	r.pod(rigPod, "mm-0190aaaaaaaa", "ready")
	att := r.queueTake(rigBatch, 200)
	other := r.take(rigBatch)
	decodeResp(t, r.poll(r.link(rigPod), idle()))
	r.setState(rigPod, "busy")
	if resp := decodeResp(t, r.poll(r.link(rigPod), state("busy"))); len(resp.Cancel) != 0 {
		t.Fatalf("%+v", resp)
	}
	r.q.RequestCancel(att)
	r.q.RequestCancel(other) // not handed to this pod
	resp := decodeResp(t, r.poll(r.link(rigPod), state("busy")))
	if resp.Action != "wait" || len(resp.Cancel) != 1 || resp.Cancel[0] != att {
		t.Fatalf("%+v", resp)
	}
	decodeResp(t, r.poll(r.link(rigPod), state("idle").with("done", done(att, "error", 3))))
	if resp := decodeResp(t, r.poll(r.link(rigPod), state("idle"))); len(resp.Cancel) != 0 {
		t.Fatalf("a done take is still listed: %+v", resp)
	}
}

// TestPodPoll_DroppedWhenNoLongerQueued: a take whose assignment was cancelled while queued (but still in memory) is skipped and the
// next one is handed.
func TestPodPoll_SkipsCancelledAndUnbuildable(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	r.pod(rigPod, "mm-0190aaaaaaaa", "ready")
	gone := r.queueTake(rigBatch, 200)
	bad := r.take(rigBatch)
	badSpec := r.spec(bad, 200)
	badSpec.Kind = provider.KindAnalyze // /2 features on a /1 wire: BuildWorkerInput refuses it
	r.q.Add(rigBatch, badSpec)
	good := r.queueTake(rigBatch, 200)
	if ok, err := pods.CloseAssignment(context.Background(), r.st.W, gone, "cancelled", "", rigNow); err != nil || !ok {
		t.Fatal(ok, err)
	}
	resp := decodeResp(t, r.poll(r.link(rigPod), idle()))
	var in struct {
		AttemptID string `json:"attempt_id"`
	}
	if err := json.Unmarshal(resp.Take, &in); err != nil || in.AttemptID != good {
		t.Fatalf("handed %q (%v), want %q: %+v", in.AttemptID, err, good, resp)
	}
	if a, _ := pods.GetAssignment(context.Background(), r.st.R, bad); a.Status != "not_handed" || a.ErrorCode != "BAD_REQUEST" || a.PodID != "" {
		t.Fatalf("%+v", a)
	}
}

// TestPodPoll_URLsNeverLogged: the access log has the method, the masked path, the status and the byte count, and never the pod id,
// the query, a signature or any part of a take.
func TestPodPoll_URLsNeverLogged(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	r.pod(rigPod, "mm-0190aaaaaaaa", "ready")
	r.queueTake(rigBatch, 200)
	link := r.link(rigPod)
	u, _ := url.Parse(link)
	decodeResp(t, r.poll(link, idle()))
	r.poll(link+"x", idle())        // a refused one
	r.poll(u.Path+"?sig=a", idle()) // and one with a junk query
	logs := r.logs.String()
	if !strings.Contains(logs, `"path":"/worker/v1/pods/<pod>/poll"`) || !strings.Contains(logs, `"status":200`) || !strings.Contains(logs, `"status":403`) {
		t.Fatalf("log: %s", logs)
	}
	for _, leak := range []string{u.Query().Get("sig"), "sig=", "exp=", rigPod, "worker/v1/uploads", "mm-0190aaaaaaaa", "syn-a"} {
		if strings.Contains(logs, leak) {
			t.Errorf("the log contains %q: %s", leak, logs)
		}
	}
}

// TestPodPoll_ResponsesAreNoStore.
func TestPodPoll_ResponsesAreNoStore(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	r.pod(rigPod, "mm-0190aaaaaaaa", "terminating")
	w := r.poll(r.link(rigPod), idle())
	if w.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("%v", w.Header())
	}
}

func TestPodPoll_Signer(t *testing.T) {
	t.Parallel()
	s := pods.NewSigner([]byte("k1k1k1k1k1k1k1k1k1k1k1k1k1k1k1k1"))
	u, err := url.Parse(s.URL("https://mm.example.com/", rigPod, 1234))
	if err != nil || u.Path != "/worker/v1/pods/"+rigPod+"/poll" || u.Host != "mm.example.com" {
		t.Fatalf("%v %v", u, err)
	}
	sig := u.Query().Get("sig")
	if !s.Verify(rigPod, 1234, sig) || s.Verify(rigPod, 1235, sig) || s.Verify(rigPod2, 1234, sig) || s.Verify(rigPod, 1234, sig+"a") {
		t.Fatal("verify")
	}
	if pods.NewSigner([]byte("other-other-other-other-other-ot")).Verify(rigPod, 1234, sig) {
		t.Fatal("another key verified")
	}
	if pods.PollExpFor(rigNow) != rigNow.Unix()+600 {
		t.Fatal("exp")
	}
}

// TestSigner_ShortKeyVerifiesNothing: the pod key is absent in production; a signer built from an empty or short key must not accept
// the MAC an attacker computes with that same key, and a poller without a signer refuses every link.
func TestSigner_ShortKeyVerifiesNothing(t *testing.T) {
	t.Parallel()
	for _, key := range [][]byte{nil, {}, []byte("short")} {
		s := pods.NewSigner(key)
		u, err := url.Parse(s.URL(rigURL, rigPod, 1234))
		if err != nil {
			t.Fatal(err)
		}
		if s.Verify(rigPod, 1234, u.Query().Get("sig")) {
			t.Fatalf("a signer with a %d-byte key verified its own MAC", len(key))
		}
	}
	var nilSigner *pods.Signer
	if nilSigner.Verify(rigPod, 1234, "x") {
		t.Fatal("nil signer verified")
	}
	r := newSRig(t)
	r.pod(rigPod, "mm-0190aaaaaaaa", "ready")
	link := r.link(rigPod)
	h := pods.NewPoller(pods.PollOptions{Store: r.st, Queue: r.q, Clock: r.clk, Hold: -1}).Handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, link, bytes.NewReader(mustJSON(t, idle()))))
	if w.Code != 403 || problemCode(t, w) != "POD_LINK_INVALID" {
		t.Fatalf("poller without signer: %d %s", w.Code, w.Body)
	}
}

// TestPodPoll_PodStoppedDuringHold: the pod is moved to terminating while its poll is held; a take that arrives afterwards is not
// handed to it, stays queued and the answer is wait.
func TestPodPoll_PodStoppedDuringHold(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	r.pod(rigPod, "mm-0190aaaaaaaa", "ready")
	r.h = r.handler(0)
	got := make(chan pods.PollResponse, 1)
	go func() { got <- decodeResp(t, r.poll(r.link(rigPod), idle())) }()
	time.Sleep(50 * time.Millisecond)
	r.setState(rigPod, "terminating")
	att := r.queueTake(rigBatch, 200)
	select {
	case resp := <-got:
		if resp.Action == "take" || !noTake(resp) {
			t.Fatalf("take handed to a stopping pod: %+v", resp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hold did not end")
	}
	if a, _ := pods.GetAssignment(context.Background(), r.st.R, att); a.PodID != "" || a.HandedAt != nil {
		t.Fatalf("assignment recorded as handed: %+v", a)
	}
	if r.q.Len(rigBatch) != 1 {
		t.Fatalf("take not requeued: %d", r.q.Len(rigBatch))
	}
}

// TestPodPoll_OneTakeInFlightPerPod: a repeated idle poll (the take response was lost) while the pod row is still ready gets no second
// take while the first is handed and not done.
func TestPodPoll_OneTakeInFlightPerPod(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	r.pod(rigPod, "mm-0190aaaaaaaa", "ready")
	first := r.queueTake(rigBatch, 200)
	second := r.queueTake(rigBatch, 200)
	if resp := decodeResp(t, r.poll(r.link(rigPod), idle())); resp.Action != "take" {
		t.Fatalf("%+v", resp)
	}
	if resp := decodeResp(t, r.poll(r.link(rigPod), idle())); resp.Action == "take" || !noTake(resp) {
		t.Fatalf("second take handed: %+v", resp)
	}
	if a, _ := pods.GetAssignment(context.Background(), r.st.R, second); a.PodID != "" {
		t.Fatalf("second assignment handed: %+v", a)
	}
	if r.q.Len(rigBatch) != 1 {
		t.Fatalf("queue %d", r.q.Len(rigBatch))
	}
	// once the first is done, the pod can take the next
	r.setState(rigPod, "busy")
	if resp := decodeResp(t, r.poll(r.link(rigPod), state("idle").with("done", done(first, "ok", 5)))); resp.Action != "take" {
		t.Fatalf("after done: %+v", resp)
	}
}

// TestPodPoll_RestartReportedOnce: repeated booting reports before the loop acts append one event and nudge once.
func TestPodPoll_RestartReportedOnce(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	r.pod(rigPod, "mm-0190aaaaaaaa", "busy")
	r.poll(r.link(rigPod), state("booting"))
	n := r.nudges.Load()
	r.poll(r.link(rigPod), state("booting"))
	if c := r.count(`SELECT COUNT(*) FROM pod_events WHERE pod_id = ? AND event = 'restart_reported'`, rigPod); c != 1 {
		t.Fatalf("restart events: %d", c)
	}
	if r.nudges.Load() != n {
		t.Fatal("a repeated restart report nudged again")
	}
}

// TestQueue_ClearCancel: a cancel request is forgotten once cleared, and the poller clears it when the attempt is done.
func TestQueue_ClearCancel(t *testing.T) {
	t.Parallel()
	q := pods.NewQueue()
	q.RequestCancel("a1")
	q.ClearCancel("a1")
	if q.CancelRequested("a1") {
		t.Fatal("cancel survived ClearCancel")
	}
	r := newSRig(t)
	r.pod(rigPod, "mm-0190aaaaaaaa", "ready")
	att := r.queueTake(rigBatch, 200)
	decodeResp(t, r.poll(r.link(rigPod), idle()))
	r.q.RequestCancel(att)
	r.setState(rigPod, "busy")
	decodeResp(t, r.poll(r.link(rigPod), state("idle").with("done", done(att, "error", 1))))
	if r.q.CancelRequested(att) {
		t.Fatal("cancel entry kept after done")
	}
}
