package pods_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods"
)

func TestInsertIntent_StartsAsIntentAndFreezesIdentity(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	r.pod(rigPod, "mm-0190aaaaaaaa", "intent")
	row, err := pods.GetRow(context.Background(), r.st.R, rigPod)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != "intent" || row.PricePPH != 300000 || row.OverheadReserved != 163750 || row.PollExp != pods.PollExpFor(r.deadl) || !row.DeadlineAt.Equal(r.deadl) ||
		row.N != 1 || row.GPUTry != 1 || row.Restarts != 0 || row.Settled || row.ProviderRef != "" || row.OverheadReservedAt != nil {
		t.Fatalf("%+v", row)
	}
	// the same pod identity twice: UNIQUE (batch_id, n, gpu_try)
	err = pods.InsertIntent(context.Background(), r.st.W, pods.NewPod{ID: rigPod2, Name: "mm-0190bbbbbbbb", Kind: "batch", BatchID: rigBatch, N: 1, GPUTry: 1, GPUType: "g",
		ImageDigest: "d", PricePPH: 1, DeadlineAt: r.deadl, PollExp: 1, At: rigNow})
	if err == nil {
		t.Fatal("a second intent for the same (batch, n, gpu_try)")
	}
}

func TestLiveRows(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	r.pod(rigPod, "mm-0190aaaaaaaa", "busy")
	r.podTry(rigPod2, "mm-0190bbbbbbbb", rigBatch, "terminating", 2)
	r.setState(rigPod2, "terminated")
	r.batch(rigBatch2, "est2")
	r.podOf("01900000-0000-7000-8000-00000000d003", "mm-0190cccccccc", rigBatch2, "ready")
	rows, err := pods.LiveRows(context.Background(), r.st.R, rigBatch)
	if err != nil || len(rows) != 1 || rows[0].ID != rigPod || rows[0].State != "busy" {
		t.Fatalf("%+v %v", rows, err)
	}
	if rows, err = pods.LiveRows(context.Background(), r.st.R, "none"); err != nil || len(rows) != 0 {
		t.Fatalf("%+v %v", rows, err)
	}
}

// TestMarkOverheadReserved: set once; the second call reports false, so the lifecycle reserves exactly once per pod.
func TestMarkOverheadReserved(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	r.pod(rigPod, "mm-0190aaaaaaaa", "intent")
	ctx := context.Background()
	if ok, err := pods.MarkOverheadReserved(ctx, r.st.W, rigPod, rigNow); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := pods.MarkOverheadReserved(ctx, r.st.W, rigPod, rigNow.Add(time.Minute)); err != nil || ok {
		t.Fatal("second mark succeeded", ok, err)
	}
	row, _ := pods.GetRow(ctx, r.st.R, rigPod)
	if row.OverheadReservedAt == nil || !row.OverheadReservedAt.Equal(rigNow) {
		t.Fatalf("%+v", row.OverheadReservedAt)
	}
	if ok, err := pods.MarkOverheadReserved(ctx, r.st.W, "nope", rigNow); err != nil || ok {
		t.Fatal(ok, err)
	}
	// in the caller's transaction: a rolled-back mark leaves the pod unmarked
	r.podTry(rigPod2, "mm-0190bbbbbbbb", rigBatch, "intent", 2)
	tx, err := r.st.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := pods.MarkOverheadReserved(ctx, tx, rigPod2, rigNow); err != nil || !ok {
		t.Fatal(ok, err)
	}
	_ = tx.Rollback()
	if row, _ := pods.GetRow(ctx, r.st.R, rigPod2); row.OverheadReservedAt != nil {
		t.Fatal("the mark survived a rollback")
	}
}

func TestAssignments(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	ctx := context.Background()
	a := r.take(rigBatch)
	got, err := pods.GetAssignment(ctx, r.st.R, a)
	if err != nil || got.BatchID != rigBatch || got.Status != "" || got.PodID != "" || got.HandedAt != nil || got.DoneAt != nil || !got.QueuedAt.Equal(rigNow) {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := pods.GetAssignment(ctx, r.st.R, "nope"); !errors.Is(err, pods.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := pods.CloseAssignment(ctx, r.st.W, a, "ok", "", rigNow); err == nil {
		t.Fatal("CloseAssignment accepted status ok")
	}
	ok, err := pods.CloseAssignment(ctx, r.st.W, a, pods.AssignNotHanded, "SUBMIT_REJECTED", rigNow)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, _ := pods.CloseAssignment(ctx, r.st.W, a, pods.AssignCancelled, "", rigNow); ok {
		t.Fatal("closed twice")
	}
	got, _ = pods.GetAssignment(ctx, r.st.R, a)
	if got.Status != "not_handed" || got.ErrorCode != "SUBMIT_REJECTED" || got.DoneAt == nil {
		t.Fatalf("%+v", got)
	}
	if err := pods.QueueAssignment(ctx, r.st.W, "01900000-0000-7000-8000-0000000c9999", rigBatch, rigNow); err == nil {
		t.Fatal("an assignment for an unknown attempt")
	}
}

func TestMarkLost(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	ctx := context.Background()
	r.pod(rigPod, "mm-0190aaaaaaaa", "ready")
	a, b := r.queueTake(rigBatch, 200), r.queueTake(rigBatch, 200)
	decodeResp(t, r.poll(r.link(rigPod), idle())) // hands a out
	ids, err := pods.MarkLost(ctx, r.st.W, rigPod, "WORKER_LOST", rigNow)
	if err != nil || len(ids) != 1 || ids[0] != a {
		t.Fatal(ids, err)
	}
	if ids, _ := pods.MarkLost(ctx, r.st.W, rigPod, "WORKER_LOST", rigNow); len(ids) != 0 {
		t.Fatal("lost twice", ids)
	}
	if g, _ := pods.GetAssignment(ctx, r.st.R, b); g.Status != "" {
		t.Fatalf("an unhanded take was marked: %+v", g)
	}
}

func TestSafetyLogAndEvents(t *testing.T) {
	t.Parallel()
	r := newSRig(t)
	ctx := context.Background()
	if err := pods.AppendSafety(ctx, r.st.W, pods.SafetyEntry{Kind: "probe", Verdict: "ok", CreatedBy: "cli:x", At: rigNow}); err != nil {
		t.Fatal(err)
	}
	if err := pods.AppendSafety(ctx, r.st.W, pods.SafetyEntry{Kind: "canary", ImageDigest: "ghcr.io/e/x@sha256:ab", Verdict: "verified", Detail: map[string]any{"a": 1}, CreatedBy: "cli:x", At: rigNow}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []pods.SafetyEntry{
		{Kind: "probe", Verdict: "verified", CreatedBy: "c", At: rigNow},
		{Kind: "canary", Verdict: "verified", CreatedBy: "c", At: rigNow}, // no digest
		{Kind: "probe", ImageDigest: "d", Verdict: "ok", CreatedBy: "c", At: rigNow},
		{Kind: "canary", ImageDigest: "d", Verdict: "ok", CreatedBy: "c", At: rigNow},
	} {
		if err := pods.AppendSafety(ctx, r.st.W, bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if n := r.count(`SELECT COUNT(*) FROM pod_safety_log`); n != 2 {
		t.Fatalf("%d rows", n)
	}
	r.pod(rigPod, "mm-0190aaaaaaaa", "intent")
	if err := pods.AppendEvent(ctx, r.st.W, rigPod, pods.Event{At: rigNow, Event: "note"}); err != nil {
		t.Fatal(err)
	}
	if err := pods.AppendEvent(ctx, r.st.W, rigPod, pods.Event{At: rigNow, Event: "bad", Detail: make(chan int)}); err == nil {
		t.Fatal("an unencodable detail was accepted")
	}
}
