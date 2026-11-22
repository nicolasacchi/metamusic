package pods_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods"
)

// Run wakes on a queued take and on a nudge, ticks once at the start, and ends with its context.
func TestLifecycle_RunWakesOnTheQueueAndTheNudge(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = r.l.Run(ctx) }()
	defer func() { cancel(); wg.Wait() }()
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for i := 0; i < 500; i++ {
			if ok() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", what)
	}
	r.queue() // Submit's queue.Add wakes the loop at once
	waitFor("the create", func() bool { return r.api.nCreates() == 1 })
	r.api.setStatus(1, pods.StatusRunning)
	r.l.Nudge()
	waitFor("RUNNING", func() bool {
		rows, err := pods.AllLiveRows(context.Background(), r.st.R)
		return err == nil && len(rows) == 1 && rows[0].RunningSeenAt != nil
	})
	cancel()
	wg.Wait()
}

func TestLifecycle_RunDoesNothingWithoutTheLease(t *testing.T) {
	t.Parallel()
	r := newLRig(t, func(o *pods.LifecycleOptions) { o.Holding = func() bool { return false } })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = r.l.Run(ctx); close(done) }()
	r.queue()
	r.l.Nudge()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done
	if r.api.nCalls() != 0 || len(r.rows(r.batchID)) != 0 {
		t.Error("a loop without the lease acted")
	}
}

// With nothing queued, nothing live and the key absent, a pass makes no call at all (the production shape between windows).
func TestLifecycle_QuietPassMakesNoCall(t *testing.T) {
	t.Parallel()
	r := newLRig(t)
	r.key.Store(false)
	for range 5 {
		r.step(time.Minute)
	}
	if r.api.nCalls() != 0 || r.reserved(r.batchID) != 0 {
		t.Errorf("calls %d reserved %d", r.api.nCalls(), r.reserved(r.batchID))
	}
}

// No log line, event, audit row or trip detail carries a pod id of RunPod, a poll link or a signature.
func TestLifecycle_NeverLogsSecrets(t *testing.T) {
	t.Parallel()
	var buf syncBuf
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	var details []string
	var mu sync.Mutex
	r := newLRig(t, func(o *pods.LifecycleOptions) {
		o.Log = logger
		o.Trip = func(_ context.Context, reason string, d map[string]any) error {
			b, _ := json.Marshal(d)
			mu.Lock()
			details = append(details, reason+string(b))
			mu.Unlock()
			return nil
		}
	})
	r.queue()
	r.walkToReady()
	r.api.getErr = accessDenied{}
	r.step(10 * time.Second) // an access trip and a warning
	r.api.getErr = nil
	r.api.createQ = nil
	r.step(5 * time.Minute) // orphaned, deleted
	r.step(10 * time.Second)
	row := r.row(r.batchID, 1, 1)
	link := r.api.env(1)["MM_POLL_URL"]
	sig := link[strings.Index(link, "sig=")+4:]
	var all strings.Builder
	all.WriteString(buf.String())
	all.WriteString(strings.Join(details, "\n"))
	all.WriteString(strings.Join(r.audits, "\n"))
	all.WriteString(r.str(`SELECT COALESCE(group_concat(COALESCE(detail_json, '') || COALESCE(code, ''), ' '), '') FROM pod_events`) + "\n")
	all.WriteString(r.str(`SELECT COALESCE(group_concat(detail_json, ' '), '') FROM pod_safety_log`) + "\n")
	for _, secret := range []string{row.ProviderRef, link, sig, "sig=", "MM_POLL_URL", "rp0000"} {
		if secret != "" && strings.Contains(all.String(), secret) {
			t.Errorf("%q leaked into logs, events, audits or trips: %.300s", secret, all.String())
		}
	}
	if !bytes.Contains([]byte(all.String()), []byte("access")) {
		t.Logf("no access warning in the logs (fine, but the scenario did not exercise one): %.200s", buf.String())
	}
	_ = money.MicroUSD(0)
}
