package pods_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods"
)

const canaryBatch = "01900000-0000-7000-8000-00000000b0c1"

// canaryRig is a rig whose batch is a one-take pod batch (the canary shape).
func newCanaryRig(t *testing.T) *lrig {
	t.Helper()
	r := newLRig(t)
	r.lbatch(canaryBatch, "est-canary", 1, 1_000_000)
	r.batchID = canaryBatch
	r.exec(`UPDATE batches SET state = 'finished', finished_at = ? WHERE id = ?`, "2026-11-21T00:00:00.000Z", rigBatch2)
	return r
}

func (r *lrig) verdicts() string {
	return r.str(`SELECT COALESCE(group_concat(kind || ':' || verdict || ':' || image_digest, ';'), '') FROM pod_safety_log`)
}

func TestPodCanary_VerifiedAgainstFake(t *testing.T) {
	t.Parallel()
	r := newCanaryRig(t)
	att := r.queue()
	r.api.logs = []string{"agent: poll refused", "terminator: step 1 failed:500", "terminator: step 2 ok"}
	r.tick()
	row := r.row(canaryBatch, 1, 1)
	if row.Kind != pods.KindCanary || !strings.HasPrefix(row.Name, "mm-c") || len(row.Name) != 15 || row.State != pods.StateBooting {
		t.Fatalf("row %+v", row)
	}
	env := r.api.env(1)
	if env["MM_DEADMAN_S"] != "120" {
		t.Errorf("dead-man %s", env["MM_DEADMAN_S"])
	}
	if got := row.DeadlineAt.Sub(*row.CreateSentAt); got != (1200+420)*time.Second {
		t.Errorf("deadline %s after the create, want BootS + 420 s", got)
	}
	r.api.setStatus(1, pods.StatusRunning)
	r.step(20 * time.Second)
	if row = r.row(canaryBatch, 1, 1); row.RunningSeenAt == nil {
		t.Fatal("RUNNING not recorded")
	}
	r.step(110 * time.Second) // the pod killed itself 130 s after RUNNING
	r.api.vanish(1)
	r.step(10 * time.Second)
	row = r.row(canaryBatch, 1, 1)
	if row.State != pods.StateTerminated || row.GoneSeenAt == nil {
		t.Fatalf("state %s", row.State)
	}
	if got := r.verdicts(); got != "canary:verified:"+lDigest {
		t.Errorf("verdicts %q", got)
	}
	if got := r.str(`SELECT json_extract(detail_json, '$.terminator_step') || '|' || json_extract(detail_json, '$.deleted_by_server') FROM pod_safety_log`); got != "2|0" {
		t.Errorf("verdict detail %q", got)
	}
	if r.api.nDeletes() != 0 {
		t.Errorf("deletes %d: a self-terminated pod needs none", r.api.nDeletes())
	}
	// the take is never handed out: closed never-ran, later claims refused
	if got := r.str(`SELECT status FROM pod_assignments WHERE attempt_id = ?`, att); got != "cancelled" {
		t.Errorf("take %q", got)
	}
	if r.q.Has(att) {
		t.Error("the canary's take is still queued")
	}
	b := pods.NewBackend(pods.BackendOptions{Name: lBackend, Store: r.st, Queue: r.q, Clock: r.clk, KillSlackS: 15})
	var se *pods.SubmitError
	if _, err := b.Submit(context.Background(), r.spec(r.take(canaryBatch), 186)); !errors.As(err, &se) || se.Code != "BAD_REQUEST" {
		t.Errorf("a take submitted after the canary's verdict: %v", err)
	}
	r.step(time.Hour)
	if r.api.nCreates() != 1 {
		t.Errorf("creates %d: a second canary pod", r.api.nCreates())
	}
	if !strings.Contains(strings.Join(r.audits, "\n"), pods.OpPodCanary) {
		t.Errorf("audits %v", r.audits)
	}
}

func TestPodCanary_FailedThenDeleted(t *testing.T) {
	t.Parallel()
	r := newCanaryRig(t)
	r.queue()
	r.api.logs = []string{"terminator: step 1 failed:500"}
	r.tick()
	r.api.setStatus(1, pods.StatusRunning)
	r.step(10 * time.Second)
	r.step(170 * time.Second) // 170 s after RUNNING: the dead-man (120 s) and the margin (60 s) are not over yet
	if r.api.nDeletes() != 0 {
		t.Fatalf("deleted %d times before 180 s", r.api.nDeletes())
	}
	r.step(15 * time.Second) // 185 s: still listed, the dead-man did not work
	if r.api.nDeletes() != 1 {
		t.Fatalf("deletes %d, want the loop's DELETE", r.api.nDeletes())
	}
	r.step(10 * time.Second)
	if got := r.row(canaryBatch, 1, 1).State; got != pods.StateTerminated {
		t.Fatalf("state %s", got)
	}
	if got := r.verdicts(); got != "canary:failed:"+lDigest {
		t.Errorf("verdicts %q", got)
	}
	if got := r.str(`SELECT json_extract(detail_json, '$.deleted_by_server') || '|' || json_extract(detail_json, '$.delete_failed') FROM pod_safety_log`); got != "1|0" {
		t.Errorf("verdict detail %q", got)
	}
	if r.api.logsCalls != 1 {
		t.Errorf("the log was read %d times, want once", r.api.logsCalls)
	}
}

func TestPodCanary_FailedAndTheDeleteFailsToo(t *testing.T) {
	t.Parallel()
	r := newCanaryRig(t)
	r.queue()
	r.tick()
	r.api.setStatus(1, pods.StatusRunning)
	r.step(10 * time.Second)
	r.api.keepAlive = true
	r.step(185 * time.Second)
	if r.api.nDeletes() != 1 {
		t.Fatalf("deletes %d", r.api.nDeletes())
	}
	r.api.keepAlive = false
	r.step(10 * time.Second) // the next DELETE works
	r.step(10 * time.Second)
	if got := r.verdicts(); got != "canary:failed:"+lDigest {
		t.Errorf("verdicts %q (state %s)", got, r.row(canaryBatch, 1, 1).State)
	}
}

func TestPodCanary_Inconclusive(t *testing.T) {
	t.Parallel()
	r := newCanaryRig(t)
	r.queue()
	r.tick()
	row := r.row(canaryBatch, 1, 1)
	// the pod never reaches RUNNING: nothing happens before the deadline plus a minute
	for r.clk.Now().Before(row.DeadlineAt.Add(30 * time.Second)) {
		r.step(30 * time.Second)
	}
	if r.api.nDeletes() != 0 || r.verdicts() != "" {
		t.Fatalf("acted before deadline + 60 s: deletes %d verdicts %q", r.api.nDeletes(), r.verdicts())
	}
	r.step(40 * time.Second)
	if r.api.nDeletes() != 1 {
		t.Fatalf("deletes %d", r.api.nDeletes())
	}
	r.step(10 * time.Second)
	if got := r.verdicts(); got != "canary:inconclusive:"+lDigest {
		t.Errorf("verdicts %q", got)
	}
}

// Every poll of a canary answers 403 (the real handler): its link is signed with a key nobody has, so the agent never sees a 2xx.
func TestPodCanary_EveryPoll403(t *testing.T) {
	t.Parallel()
	r := newCanaryRig(t)
	r.queue()
	r.tick()
	row := r.row(canaryBatch, 1, 1)
	link, err := url.Parse(r.api.env(1)["MM_POLL_URL"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link.Path, "/worker/v1/pods/"+row.ID+"/poll") || link.Query().Get("exp") == "" {
		t.Fatalf("link %s", link.Path)
	}
	for _, body := range []pollIn{state("booting"), idle(), idle().with("boot", bootDoc), state("busy")} {
		if w := r.poll(link.RequestURI(), body); w.Code != 403 || problemCode(t, w) != "POD_LINK_INVALID" {
			t.Fatalf("poll answered %d %s", w.Code, w.Body)
		}
	}
	// the pod's row records no poll, and the same link signed with the app's own key would have been accepted (so the 403 is the key)
	if row = r.row(canaryBatch, 1, 1); row.FirstPollAt != nil || row.LastPollAt != nil {
		t.Error("a poll was recorded")
	}
	own := r.sig.URL(rigURL, row.ID, row.PollExp)
	u, _ := url.Parse(own)
	if w := r.poll(u.RequestURI(), state("booting")); w.Code != 200 {
		t.Errorf("the app's own link answered %d", w.Code)
	}
	// the throwaway key is random per pod
	r.api.createQ = nil
	if env2 := r.api.env(1)["MM_POLL_URL"]; env2 != link.String() {
		t.Error("env changed")
	}
	// and it is never handed out
	if got := r.str(`SELECT COUNT(*) FROM pod_assignments WHERE pod_id IS NOT NULL`); got != "0" {
		t.Errorf("a canary take was handed out")
	}
}

func TestPodCanary_ThrowawayKeyIsPerPodRandom(t *testing.T) {
	t.Parallel()
	var keys [][]byte
	r := newCanaryRig(t)
	_ = keys
	r.queue()
	r.tick()
	a := r.api.env(1)["MM_POLL_URL"]
	r2 := newCanaryRig(t)
	r2.queue()
	r2.tick()
	b := r2.api.env(1)["MM_POLL_URL"]
	sigOf := func(s string) string { u, _ := url.Parse(s); return u.Query().Get("sig") }
	if sigOf(a) == "" || sigOf(a) == sigOf(b) {
		t.Errorf("signatures %q %q", sigOf(a), sigOf(b))
	}
}

// An ordinary pod batch needs the latest canary row for the image digest to be `verified` (D-BULK-12).
func TestReconcilePods_CanaryRuleGatesOrdinaryBatches(t *testing.T) {
	t.Parallel()
	r := newLRig(t, func(o *pods.LifecycleOptions) { o.RequireCanary = true })
	r.queue()
	other := "sha256:" + strings.Repeat("1", 64)
	for _, tc := range []struct {
		name    string
		verdict string
		digest  string
		want    int
	}{
		{"no canary row", "", "", 0},
		{"a verified canary of another image", "verified", other, 0},
		{"a failed canary of this image", "failed", lDigest, 0},
		{"an inconclusive canary of this image", "inconclusive", lDigest, 0},
		{"a verified canary of this image", "verified", lDigest, 1},
	} {
		if tc.verdict != "" {
			r.exec(`INSERT INTO pod_safety_log(kind, image_digest, verdict, detail_json, created_by, created_at) VALUES('canary', ?, ?, '{}', 'system:bulk', ?)`,
				tc.digest, tc.verdict, "2026-11-21T12:00:00.000Z")
		}
		r.tick()
		if got := r.api.nCreates(); got != tc.want {
			t.Fatalf("%s: creates %d, want %d", tc.name, got, tc.want)
		}
	}
	// the latest row decides: a later failure closes the gate again for new batches
	r.exec(`INSERT INTO pod_safety_log(kind, image_digest, verdict, detail_json, created_by, created_at) VALUES('canary', ?, 'failed', '{}', 'system:bulk', ?)`, lDigest, "2026-11-21T13:00:00.000Z")
	ok, err := pods.NewLifecycle(r.opts).CanaryVerifiedForTest(context.Background())
	if err != nil || ok {
		t.Errorf("verified %v %v after a later failure", ok, err)
	}
}

// The canary batch itself is the only pod batch allowed before a canary is verified.
func TestReconcilePods_CanaryBatchNeedsNoCanary(t *testing.T) {
	t.Parallel()
	r := newCanaryRig(t)
	r.opts.RequireCanary = true
	r.l = pods.NewLifecycle(r.opts)
	r.queue()
	r.tick()
	if r.api.nCreates() != 1 || r.row(canaryBatch, 1, 1).Kind != pods.KindCanary {
		t.Errorf("creates %d", r.api.nCreates())
	}
}
