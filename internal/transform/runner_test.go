package transform_test

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/transform"
	"github.com/nicolasacchi/metamusic/internal/transform/transformtest"
)

// TestRunner_HappyPath: a queued row runs through the fakes to done, and the result is an ordinary Take of the right shape
// (origin job in storage, import_rule transform, no job, 24-bit stereo, transform_id = the take id) with its ledger row.
func TestRunner_HappyPath(t *testing.T) {
	r := newRig(t, false)
	id := r.queue(qspec{})
	if !r.runOne() {
		t.Fatal("nothing was claimed")
	}
	row := r.row(id)
	if row.State != "done" || row.PCM == "" || !row.CPU.Valid || row.Owner != "" {
		t.Fatalf("row = %+v", row)
	}
	var origin, rule, job, tid, parent string
	var bits, ch, gen int
	err := r.st.R.QueryRow(`SELECT origin, import_rule, COALESCE(job_id,''), COALESCE(transform_id,''), COALESCE(parent_track_id,''), bits_per_sample, channels, generation
		FROM tracks WHERE id = ?`, id).Scan(&origin, &rule, &job, &tid, &parent, &bits, &ch, &gen)
	if err != nil {
		t.Fatal(err)
	}
	if origin != "job" || rule != "transform" || job != "" || tid != id || bits != 24 || ch != 2 || parent != r.fx["A"] || gen < 1 {
		t.Errorf("take shape: origin=%s rule=%s job=%q transform_id=%s bits=%d ch=%d parent=%s gen=%d", origin, rule, job, tid, bits, ch, parent, gen)
	}
	if n := r.count(`SELECT count(*) FROM mutations WHERE child_track_id = ? AND operator = 'glacier' AND transform_id = ?`, id, id); n != 1 {
		t.Errorf("ledger rows = %d", n)
	}
	if n := r.count(`SELECT count(*) FROM audit_log WHERE op = 'transform_done'`); n != 1 {
		t.Errorf("transform_done audit rows = %d", n)
	}
	if r.exists("incoming/transform-" + id) {
		t.Error("the work dir survived")
	}
	if r.runOne() {
		t.Error("a second claim found work")
	}
}

// TestRunner_Concurrency1: ConcurrentRenders is one. A running row blocks every claim, in this process and in a second
// runner on the same database; once it is recovered, FIFO resumes.
func TestRunner_Concurrency1(t *testing.T) {
	r := newRig(t, false)
	a := r.queue(qspec{state: "running", created: fxNow})
	b := r.queue(qspec{created: fxNow.Add(time.Second)})
	if r.runOne() {
		t.Fatal("claimed while another transform is running")
	}
	if got := r.row(b).State; got != "queued" {
		t.Fatalf("b is %s", got)
	}
	r.clk.add(transform.LeaseTTL + time.Second)
	if r.runOne() {
		t.Fatal("an expired but unrecovered lease must still count as running (Recover owns that decision)")
	}
	if err := r.svc.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !r.runOne() || r.row(a).State != "done" || r.row(b).State != "queued" {
		t.Fatalf("after recover a=%s b=%s", r.row(a).State, r.row(b).State)
	}

	// two runners, one database: a slow render in one blocks the other
	r2 := newRig(t, false)
	r2.child.Duration = 300 * time.Millisecond
	r2.queue(qspec{tag: "x"})
	r2.queue(qspec{tag: "y"})
	svc2 := transform.New(transform.Options{Store: r2.st, Library: r2.lib, Now: r2.clk.now, Config: testConfig(), Pipeline: r2.ingest, Media: r2.media,
		Child: r2.child, Data: r2.data, DataDir: r2.dir, Owner: "runner-b"})
	var wg sync.WaitGroup
	got := make([]bool, 2)
	for i, s := range []*transform.Service{r2.svc, svc2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i == 1 {
				time.Sleep(100 * time.Millisecond) // the first runner is mid-render
			}
			got[i], _ = s.RunOne(context.Background())
		}()
	}
	wg.Wait()
	if !got[0] || got[1] {
		t.Fatalf("runners claimed %v, want only the first", got)
	}
}

// TestRunner_RunsWithGenerationDisabled: the transform runner shares nothing with the generation runner (D-T1-17): it runs on
// a database whose generation lease table has never been touched.
func TestRunner_RunsWithGenerationDisabled(t *testing.T) {
	r := newRig(t, false)
	before := r.count(`SELECT count(*) FROM runner_lease`)
	id := r.queue(qspec{})
	if !r.runOne() || r.row(id).State != "done" {
		t.Fatal("the transform did not run")
	}
	if after := r.count(`SELECT count(*) FROM runner_lease`); after != before {
		t.Errorf("runner_lease rows %d -> %d: the transform runner must not touch the generation lease", before, after)
	}
}

// TestRunner_LeaseRenewed: while the child runs the lease is renewed every LeaseTTL/6, so a render much longer than the TTL is
// never taken for dead.
func TestRunner_LeaseRenewed(t *testing.T) {
	image(t)
	synctest.Test(t, func(t *testing.T) {
		var r *rig
		var mu sync.Mutex
		var samples []struct{ at, lease time.Time }
		r = newRig(t, true, func(o *transform.Options) {
			o.Changed = func(id string) {
				var lease *string
				if err := r.st.R.QueryRow(`SELECT lease_expires_at FROM transforms WHERE id = ?`, id).Scan(&lease); err != nil || lease == nil {
					return
				}
				l, _ := store.ParseTime(*lease)
				mu.Lock()
				samples = append(samples, struct{ at, lease time.Time }{time.Now(), l})
				mu.Unlock()
			}
		})
		r.child.Duration = 4 * transform.LeaseTTL
		r.child.Progress = []int{200, 400, 600, 800}
		id := r.queue(qspec{})
		start := time.Now()
		if !r.runOne() || r.row(id).State != "done" {
			t.Fatal("the long render did not finish")
		}
		mu.Lock()
		defer mu.Unlock()
		var maxLease time.Time
		for _, s := range samples {
			if !s.lease.After(s.at) {
				t.Errorf("the lease (%v) was expired at %v", s.lease, s.at)
			}
			if s.lease.After(maxLease) {
				maxLease = s.lease
			}
		}
		if !maxLease.After(start.Add(2 * transform.LeaseTTL)) {
			t.Errorf("the lease was never pushed past 2 TTLs (max %v, start %v)", maxLease, start)
		}
	})
}

// TestRunner_RecoverExpiredLease: a running row whose lease has passed is queued again (the claim already counted the
// attempt), and runs to done on the next claim.
func TestRunner_RecoverExpiredLease(t *testing.T) {
	r := newRig(t, false)
	id := r.queue(qspec{state: "running"})
	if err := r.svc.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.row(id).State; got != "running" {
		t.Fatalf("an unexpired lease was recovered (%s)", got)
	}
	r.clk.add(transform.LeaseTTL + time.Second)
	if err := r.svc.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	row := r.row(id)
	if row.State != "queued" || row.Owner != "" || row.Attempts != 1 {
		t.Fatalf("recovered row = %+v", row)
	}
	if !r.runOne() || r.row(id).State != "done" {
		t.Fatal("the recovered row did not run")
	}
	if r.row(id).Attempts != 2 {
		t.Errorf("attempts = %d, want 2", r.row(id).Attempts)
	}
}

// TestRunner_InterruptedRequeuedOnce: a crashed attempt is re-queued once; the second expiry fails the row INTERRUPTED with
// cpu_ms 0 and an audit row (D-T1-17).
func TestRunner_InterruptedRequeuedOnce(t *testing.T) {
	crash := true
	r := newRig(t, false, func(o *transform.Options) {
		o.Fault = func(p string) error {
			if crash && p == transform.FaultAfterDecode {
				return context.DeadlineExceeded
			}
			return nil
		}
	})
	id := r.queue(qspec{})
	for attempt := 1; attempt <= 2; attempt++ {
		if !r.runOne() {
			t.Fatalf("attempt %d: nothing claimed", attempt)
		}
		if row := r.row(id); row.State != "running" || row.Attempts != attempt {
			t.Fatalf("attempt %d left %+v (a crash must leave the row running)", attempt, row)
		}
		r.clk.add(transform.LeaseTTL + time.Second)
		if err := r.svc.Recover(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	row := r.row(id)
	if row.State != "failed" || row.ErrCode != transform.ErrInterrupted || !row.CPU.Valid || row.CPU.Int64 != 0 {
		t.Fatalf("after two expiries: %+v", row)
	}
	if n := r.count(`SELECT count(*) FROM audit_log WHERE op = 'transform_failed' AND error_code = ?`, transform.ErrInterrupted); n != 1 {
		t.Errorf("transform_failed audit rows = %d", n)
	}
	if r.runOne() {
		t.Error("a failed row was claimed again")
	}
}

// TestRunner_OwnerFence: every write of the running row repeats owner = ?. A runner whose row was taken over writes nothing: no
// Take, no state change, and the freeze it made is discarded.
func TestRunner_OwnerFence(t *testing.T) {
	r := newRig(t, false)
	id := r.queue(qspec{})
	r.child.WriteOut = func(dir string, job transformtest.FakeJob) error {
		r.exec(`UPDATE transforms SET owner = 'runner-z' WHERE id = ?`, id) // another runner now holds the row
		return r.child.WriteDefault(dir, job)
	}
	if !r.runOne() {
		t.Fatal("nothing claimed")
	}
	row := r.row(id)
	if row.State != "running" || row.Owner != "runner-z" || row.ErrCode != "" {
		t.Fatalf("the stale runner changed the row: %+v", row)
	}
	if n := r.count(`SELECT count(*) FROM tracks WHERE id = ?`, id); n != 0 {
		t.Error("the stale runner wrote a Take")
	}
	if r.ingest.Discards() != 1 || r.exists("takes/"+id) {
		t.Errorf("the stale runner's freeze was not discarded (discards %d)", r.ingest.Discards())
	}
	if n := r.count(`SELECT count(*) FROM audit_log WHERE op LIKE 'transform_%'`); n != 0 {
		t.Errorf("audit rows written by a stale runner: %d", n)
	}
	if r.exists("incoming/transform-" + id) {
		t.Error("the stale runner left its work dir")
	}
}

// TestRunner_WakeOnCreate: Run claims as soon as it is nudged, not at the next 5 s tick.
func TestRunner_WakeOnCreate(t *testing.T) {
	image(t)
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, true)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { r.svc.Run(ctx); close(done) }()
		synctest.Wait() // the loop is idle
		id := r.queue(qspec{})
		start := time.Now()
		r.svc.Kick()
		synctest.Wait()
		if got := r.row(id).State; got != "done" {
			t.Fatalf("state %s after a nudge", got)
		}
		if d := time.Since(start); d >= 5*time.Second {
			t.Errorf("the nudge took %v of virtual time: the runner waited for its tick", d)
		}
		cancel()
		<-done
	})
}

// TestRunner_FIFO: rows run in created_at order whatever their insertion order.
func TestRunner_FIFO(t *testing.T) {
	r := newRig(t, false)
	late := r.queue(qspec{created: fxNow.Add(2 * time.Minute), tag: "late"})
	early := r.queue(qspec{created: fxNow, tag: "early"})
	mid := r.queue(qspec{created: fxNow.Add(time.Minute), tag: "mid"})
	var order []string
	for i := 0; i < 3; i++ {
		before := map[string]bool{}
		for _, id := range []string{late, early, mid} {
			before[id] = r.row(id).State == "done"
		}
		if !r.runOne() {
			t.Fatal("nothing claimed")
		}
		for _, id := range []string{late, early, mid} {
			if !before[id] && r.row(id).State == "done" {
				order = append(order, id)
			}
		}
		r.clk.add(time.Second)
	}
	if len(order) != 3 || order[0] != early || order[1] != mid || order[2] != late {
		t.Errorf("run order %v, want early, mid, late", order)
	}
}

// TestShutdown_RequeuesRunning: a context that ends mid-render kills the child and leaves the row running (no failure is
// recorded); once its lease expires Recover re-queues it and the next claim finishes it.
func TestShutdown_RequeuesRunning(t *testing.T) {
	r := newRig(t, false)
	id := r.queue(qspec{})
	r.child.Hang = true
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _, _ = r.svc.RunOne(ctx); close(done) }()
	deadline := time.Now().Add(10 * time.Second)
	for r.child.Runs() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the child never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	row := r.row(id)
	if row.State != "running" || row.ErrCode != "" || row.Owner == "" {
		t.Fatalf("shutdown changed the row: %+v", row)
	}
	if r.exists("incoming/transform-" + id) {
		t.Error("the work dir survived the shutdown")
	}
	r.clk.add(transform.LeaseTTL + time.Second)
	if err := r.svc.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.row(id).State; got != "queued" {
		t.Fatalf("after recover: %s", got)
	}
	r.child.Hang = false
	if !r.runOne() || r.row(id).State != "done" {
		t.Fatal("the re-queued row did not finish")
	}
}

// TestRunner_NotRunnableIdles: a service without the media pipeline claims nothing, Run only waits for its context, and Verify
// and Cancel say why (503) rather than panic.
func TestRunner_NotRunnableIdles(t *testing.T) {
	r := newRig(t, false)
	id := r.queue(qspec{})
	bare := transform.New(transform.Options{Store: r.st, Config: testConfig(), Now: r.clk.now})
	if bare.Runnable() || !r.svc.Runnable() {
		t.Fatalf("runnable: bare %v full %v", bare.Runnable(), r.svc.Runnable())
	}
	if ok, err := bare.RunOne(context.Background()); ok || err != nil {
		t.Fatalf("RunOne = %v, %v", ok, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { bare.Run(ctx); close(done) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop with its context")
	}
	if got := r.row(id).State; got != "queued" {
		t.Errorf("a runner without a pipeline claimed the row: %s", got)
	}
	if pe := problemOf(t, func() error { _, err := bare.Verify(ctx, id); return err }()); pe.Status != 503 {
		t.Errorf("Verify without a pipeline: %+v", pe)
	}
	var nilSvc *transform.Service
	nilSvc.Kick() // nil-safe
	if pe := problemOf(t, nilSvc.Cancel(ctx, human, id)); pe.Status != 503 {
		t.Errorf("Cancel on a nil service: %+v", pe)
	}
}
