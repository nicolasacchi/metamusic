package fake_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
)

const price = money.MicroUSD(690000)

func TestFake_LifecycleFast(t *testing.T) {
	t.Parallel()
	b, clk := newFake(t, fake.Options{})
	ref := mustSubmit(t, b, spec("j1", 1, "pop, 100 BPM", 186))
	if ref.Backend != provider.BackendFake || ref.ID == "" {
		t.Fatalf("ref %+v", ref)
	}
	if st := mustPoll(t, b, ref); st.State != provider.Queued || st.ExecutionMS != nil {
		t.Fatalf("t=0 %+v", st)
	}
	clk.Advance(3 * time.Second) // queue 0.2 s, then the worker is still cold (3 s)
	if st := mustPoll(t, b, ref); st.State != provider.Queued {
		t.Fatalf("cold: %+v", st)
	}
	ws := workersOf(t, b)
	if len(ws) != 1 || ws[0].Status != "INITIALIZING" || ws[0].UptimeS != nil {
		t.Fatalf("pulling worker must have null uptime: %+v", ws)
	}
	if ws[0].Image != "sha256:"+zeros64 || ws[0].StartedAt == nil {
		t.Errorf("worker %+v", ws[0])
	}
	clk.Advance(300 * time.Millisecond) // 3.3 s: execution started at 3.2 s
	st := mustPoll(t, b, ref)
	if st.State != provider.Running || st.Raw != "IN_PROGRESS" || ms(st.DelayMS) != 3200 || st.WorkerID == "" {
		t.Fatalf("running: %+v", st)
	}
	if ws = workersOf(t, b); len(ws) != 1 || ws[0].Status != "RUNNING" || ws[0].UptimeS == nil {
		t.Fatalf("running worker: %+v", ws)
	}
	clk.Advance(time.Second)
	st = mustPoll(t, b, ref)
	if st.State != provider.Completed || st.Raw != "COMPLETED" || ms(st.ExecutionMS) != 1000 {
		t.Fatalf("completed: %+v", st)
	}
	clk.Advance(30 * time.Second)
	if ws = workersOf(t, b); len(ws) != 0 {
		t.Errorf("idle worker must stop after IdleS: %+v", ws)
	}
	// cold 3 s + execution 1 s + idle 20 s, plus the 0.2 s queue wait before the worker was asked for = 24 s
	want := money.ForSeconds(24, price)
	if got := b.AccruedCost(clk.Now()); got != want {
		t.Errorf("accrued %d, want %d", got, want)
	}
	// stopped: no further accrual
	clk.Advance(time.Hour)
	if got := b.AccruedCost(clk.Now()); got != want {
		t.Errorf("accrued after stop %d, want %d", got, want)
	}
}

const zeros64 = "0000000000000000000000000000000000000000000000000000000000000000"

func TestFake_WorkersMaxRespected(t *testing.T) {
	t.Parallel()
	for _, max := range []int{1, 2} {
		b, clk := newFake(t, fake.Options{WorkersMax: max, Speed: fake.Speed{Slow: 10 * time.Second}})
		refs := []provider.Ref{
			mustSubmit(t, b, spec("j1", 1, "pop", 186)),
			mustSubmit(t, b, spec("j2", 1, "pop", 186)),
			mustSubmit(t, b, spec("j3", 1, "pop", 186)),
		}
		peak, running := 0, 0
		done := map[int]time.Time{}
		for i := 0; i < 160; i++ {
			clk.Advance(500 * time.Millisecond)
			ws := workersOf(t, b)
			peak = max2(peak, len(ws))
			running = max2(running, countStatus(ws, "RUNNING"))
			for k, r := range refs {
				if _, ok := done[k]; !ok && mustPoll(t, b, r).State == provider.Completed {
					done[k] = clk.Now()
				}
			}
		}
		if peak != max {
			t.Errorf("WorkersMax %d: peak workers %d", max, peak)
		}
		if running > max {
			t.Errorf("WorkersMax %d: %d running at once", max, running)
		}
		if len(done) != 3 {
			t.Fatalf("WorkersMax %d: completed %v", max, done)
		}
		if max == 1 && !(done[0].Before(done[1]) && done[1].Before(done[2])) {
			t.Errorf("one worker serves jobs in order: %v", done)
		}
		if max == 2 && !done[2].After(done[0]) {
			t.Errorf("third job waits for a free worker: %v", done)
		}
	}
}

func max2(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func TestFake_ExecutionTimeoutKills(t *testing.T) {
	t.Parallel()
	b, clk := newFake(t, fake.Options{KillLatencyMS: 800})
	ref := mustSubmit(t, b, spec("j1", 1, "pop fake:slow=600", 186))
	clk.Advance(3200*time.Millisecond + 185*time.Second) // 1 s before the kill
	if st := mustPoll(t, b, ref); st.State != provider.Running {
		t.Fatalf("before the kill: %+v", st)
	}
	clk.Advance(time.Second) // 186 s in: the kill was sent, the kill itself takes 0.8 s
	if st := mustPoll(t, b, ref); st.State != provider.Running {
		t.Fatalf("kill latency still running: %+v", st)
	}
	clk.Advance(time.Second) // 190.2 s: the kill completed at 190.0 s
	st := mustPoll(t, b, ref)
	if st.State != provider.TimedOut || st.Raw != "TIMED_OUT" || ms(st.ExecutionMS) != 186_800 {
		t.Fatalf("timed out: %+v (executionTime must be 186 s + kill latency)", st)
	}
	if len(st.Output) != 0 {
		t.Errorf("a killed handler returns no output: %s", st.Output)
	}
}

func TestFake_AccruedCostByBatch(t *testing.T) {
	t.Parallel()
	batch := map[string]string{"j1": "A", "j2": "B"}
	b, clk := newFake(t, fake.Options{WorkersMax: 1, BatchOf: func(job string) string { return batch[job] }})
	mustSubmit(t, b, spec("j1", 1, "pop", 186))
	mustSubmit(t, b, spec("j2", 1, "pop", 186))
	clk.Advance(time.Minute)
	by := b.AccruedCostByBatch(clk.Now())
	// the worker lives 0.2 s .. 25.2 s: the cold start (caused by j1) and j1's execution belong to A (4 s),
	// j2's execution and the idle tail after the last job served belong to B
	if by["A"] != money.ForSeconds(4, price) {
		t.Errorf("batch A %d, want %d", by["A"], money.ForSeconds(4, price))
	}
	total := b.AccruedCost(clk.Now())
	if by["A"]+by["B"] != total || total != money.ForSeconds(25, price) {
		t.Errorf("A %d + B %d != total %d (want %d)", by["A"], by["B"], total, money.ForSeconds(25, price))
	}
	if len(by) != 2 {
		t.Errorf("batches %v", by)
	}
}

func TestFake_BillingBuckets(t *testing.T) {
	t.Parallel()
	b, clk := newFake(t, fake.Options{})
	clk.Advance(59*time.Minute + 50*time.Second)
	mustSubmit(t, b, spec("j1", 1, "pop", 186))
	clk.Advance(10 * time.Minute)
	got, err := b.Billing(context.Background(), t0, t0.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// worker: 00:59:50.2 .. 01:00:14.2 -> 9.8 s in hour 0, 14.2 s in hour 1, each bucket rounded up
	want := []provider.BillBucket{
		{Start: t0, End: t0.Add(time.Hour), Total: money.ForMillis(9800, price), Compute: money.ForMillis(9800, price)},
		{Start: t0.Add(time.Hour), End: t0.Add(2 * time.Hour), Total: money.ForMillis(14200, price), Compute: money.ForMillis(14200, price)},
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("buckets\n got %+v\nwant %+v", got, want)
	}
	if got[0].Disk != 0 {
		t.Errorf("disk %d", got[0].Disk)
	}
	// a window that excludes hour 0 only returns hour 1
	got, _ = b.Billing(context.Background(), t0.Add(time.Hour), t0.Add(2*time.Hour))
	if len(got) != 1 || got[0].Start != t0.Add(time.Hour) {
		t.Errorf("window: %+v", got)
	}
}

func TestFake_PermissionModes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("patch forbidden", func(t *testing.T) {
		t.Parallel()
		b, _ := newFake(t, fake.Options{Permissions: fake.Permissions{PatchForbidden: true}})
		if err := b.ScaleToZero(ctx); !errors.Is(err, provider.ErrNotPermitted) {
			t.Errorf("ScaleToZero: %v", err)
		}
		if err := b.RevertDrift(ctx, []provider.Drift{{Field: "workers.max"}}); !errors.Is(err, provider.ErrNotPermitted) {
			t.Errorf("RevertDrift: %v", err)
		}
	})
	t.Run("billing forbidden", func(t *testing.T) {
		t.Parallel()
		b, _ := newFake(t, fake.Options{Permissions: fake.Permissions{BillingForbidden: true}})
		if _, err := b.Billing(ctx, t0, t0.Add(time.Hour)); !errors.Is(err, provider.ErrNotPermitted) {
			t.Errorf("Billing: %v", err)
		}
	})
	t.Run("patch fails", func(t *testing.T) {
		t.Parallel()
		b, _ := newFake(t, fake.Options{Permissions: fake.Permissions{PatchFails: true}})
		err := b.ScaleToZero(ctx)
		if err == nil || errors.Is(err, provider.ErrNotPermitted) {
			t.Errorf("PatchFails must fail with an ordinary error: %v", err)
		}
		if err := b.RevertDrift(ctx, nil); err == nil || errors.Is(err, provider.ErrNotPermitted) {
			t.Errorf("RevertDrift: %v", err)
		}
		if !b.Capabilities().ScaleToZero {
			t.Error("PatchFails keeps the capability claim (the embedded file may be wrong)")
		}
	})
	t.Run("access revoked", func(t *testing.T) {
		t.Parallel()
		b, _ := newFake(t, fake.Options{Permissions: fake.Permissions{AccessRevoked: true}})
		checks := map[string]error{}
		_, checks["quote"] = b.Quote(ctx)
		_, checks["submit"] = b.Submit(ctx, spec("j1", 1, "pop", 60))
		_, checks["poll"] = b.Poll(ctx, provider.Ref{Backend: provider.BackendFake, ID: "x"})
		checks["cancel"] = b.Cancel(ctx, provider.Ref{Backend: provider.BackendFake, ID: "x"})
		_, checks["sweep"] = b.Sweep(ctx)
		checks["purge"] = b.PurgeQueue(ctx)
		checks["scale"] = b.ScaleToZero(ctx)
		_, checks["billing"] = b.Billing(ctx, t0, t0.Add(time.Hour))
		for k, err := range checks {
			if !errors.Is(err, provider.ErrAccess) {
				t.Errorf("%s: %v, want ErrAccess", k, err)
			}
		}
	})
}

func TestFake_CancelStopsBilling(t *testing.T) {
	t.Parallel()
	b, clk := newFake(t, fake.Options{Speed: fake.Speed{Slow: 100 * time.Second}})
	ref := mustSubmit(t, b, spec("j1", 1, "pop", 186))
	clk.Advance(10 * time.Second)
	if st := mustPoll(t, b, ref); st.State != provider.Running {
		t.Fatalf("%+v", st)
	}
	if err := b.Cancel(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	st := mustPoll(t, b, ref)
	if st.State != provider.Cancelled || st.Raw != "CANCELLED" || ms(st.ExecutionMS) != 6800 {
		t.Fatalf("cancelled: %+v", st)
	}
	clk.Advance(time.Hour)
	c1 := b.AccruedCost(clk.Now())
	clk.Advance(time.Hour)
	if c2 := b.AccruedCost(clk.Now()); c2 != c1 {
		t.Errorf("billing continued after cancel: %d -> %d", c1, c2)
	}
	// life 0.2 s .. 30 s (killed at 10 s, 20 s idle)
	if want := money.ForSeconds(30, price); c1 != want {
		t.Errorf("accrued %d, want %d", c1, want)
	}
	if ws := workersOf(t, b); len(ws) != 0 {
		t.Errorf("workers %+v", ws)
	}
	// cancelling a finished or unknown job is a no-op
	if err := b.Cancel(context.Background(), ref); err != nil {
		t.Error(err)
	}
	if err := b.Cancel(context.Background(), provider.Ref{Backend: provider.BackendFake, ID: "nope"}); err != nil {
		t.Error(err)
	}
}

func TestFake_CancelQueuedJob(t *testing.T) {
	t.Parallel()
	b, clk := newFake(t, fake.Options{})
	ref := mustSubmit(t, b, spec("j1", 1, "pop", 186))
	clk.Advance(time.Second)
	if err := b.Cancel(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	st := mustPoll(t, b, ref)
	if st.State != provider.Cancelled || st.ExecutionMS != nil {
		t.Errorf("cancelled while queued: %+v", st)
	}
	// the worker that was starting for it is still billed until it idles out: 0.2 s .. 23.2 s (ready 3.2 s, idle 20 s)
	clk.Advance(time.Minute)
	if got, want := b.AccruedCost(clk.Now()), money.ForSeconds(23, price); got != want {
		t.Errorf("accrued %d, want %d", got, want)
	}
}

func TestFake_PurgeQueue(t *testing.T) {
	t.Parallel()
	b, clk := newFake(t, fake.Options{WorkersMax: 1, Speed: fake.Speed{Slow: 50 * time.Second}})
	r1 := mustSubmit(t, b, spec("j1", 1, "pop", 186))
	r2 := mustSubmit(t, b, spec("j2", 1, "pop", 186))
	clk.Advance(10 * time.Second)
	if err := b.PurgeQueue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := mustPoll(t, b, r1); st.State != provider.Running {
		t.Errorf("a running job is not in the queue: %+v", st)
	}
	if st := mustPoll(t, b, r2); st.State != provider.Cancelled {
		t.Errorf("queued job: %+v", st)
	}
}

func TestFake_ScaleToZero(t *testing.T) {
	t.Parallel()
	b, clk := newFake(t, fake.Options{})
	mustSubmit(t, b, spec("j1", 1, "pop", 186))
	clk.Advance(10 * time.Second) // done, worker idle
	if err := b.ScaleToZero(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ws := workersOf(t, b); len(ws) != 0 {
		t.Errorf("idle workers stop at once: %+v", ws)
	}
	r, _ := b.Sweep(context.Background())
	if r.Endpoint == nil || r.Endpoint.WorkersMax != 0 {
		t.Errorf("endpoint %+v", r.Endpoint)
	}
	ref := mustSubmit(t, b, spec("j2", 1, "pop", 186))
	clk.Advance(time.Minute)
	if st := mustPoll(t, b, ref); st.State != provider.Queued {
		t.Errorf("max 0: nothing runs: %+v", st)
	}
	if len(workersOf(t, b)) != 0 {
		t.Error("no worker may start with workers.max 0")
	}
}

func TestFake_Quote(t *testing.T) {
	t.Parallel()
	b, clk := newFake(t, fake.Options{})
	q, err := b.Quote(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if q.Backend != provider.BackendFake || q.ListPricePPH != price || !q.ObservedAt.Equal(clk.Now()) || q.Endpoint.WorkersMax != 2 || q.Endpoint.IdleTimeoutS == nil || *q.Endpoint.IdleTimeoutS != 20 {
		t.Errorf("quote %+v", q)
	}
	if q.Endpoint.Type != "QUEUE" || q.Endpoint.ScalerType != "QUEUE_DELAY" || q.Endpoint.GPUCount != 1 {
		t.Errorf("endpoint %+v", q.Endpoint)
	}
}

func TestFake_Counters(t *testing.T) {
	t.Parallel()
	b, clk := newFake(t, fake.Options{})
	ctx := context.Background()
	ref := mustSubmit(t, b, spec("j1", 1, "pop", 60))
	for i := 0; i < 3; i++ {
		mustPoll(t, b, ref)
	}
	_ = b.Cancel(ctx, ref)
	_, _ = b.Sweep(ctx)
	_, _ = b.Quote(ctx)
	_ = b.PurgeQueue(ctx)
	_ = b.ScaleToZero(ctx)
	_ = b.RevertDrift(ctx, nil)
	_, _ = b.Billing(ctx, t0, t0.Add(time.Hour))
	_ = b.AccruedCost(clk.Now())
	_ = b.AccruedCostByBatch(clk.Now())
	_ = b.Capabilities()
	want := fake.Counters{Quote: 1, Submit: 1, Poll: 3, Cancel: 1, Sweep: 1, PurgeQueue: 1, ScaleToZero: 1, RevertDrift: 1, Billing: 1}
	if got := b.Counters(); got != want {
		t.Errorf("counters %+v, want %+v (accessors must not count)", got, want)
	}
}

func TestFake_ForeignAndUnknownRefs(t *testing.T) {
	t.Parallel()
	b, _ := newFake(t, fake.Options{})
	for _, r := range []provider.Ref{{Backend: provider.BackendFake, ID: "nope"}, {Backend: provider.BackendRunPod, ID: "x"}, {}} {
		if st := mustPoll(t, b, r); st.State != provider.NotFound {
			t.Errorf("%+v: %+v", r, st)
		}
	}
}

func TestFake_DeterministicAcrossClocks(t *testing.T) {
	t.Parallel()
	run := func(step time.Duration) (provider.RemoteStatus, money.MicroUSD) {
		b, clk := newFake(t, fake.Options{})
		ref := mustSubmit(t, b, spec("j1", 1, "pop", 60))
		for i := 0; i < int(time.Minute/step); i++ {
			clk.Advance(step)
		}
		return mustPoll(t, b, ref), b.AccruedCost(clk.Now())
	}
	s1, c1 := run(100 * time.Millisecond)
	s2, c2 := run(7 * time.Second)
	if s1.State != s2.State || ms(s1.ExecutionMS) != ms(s2.ExecutionMS) || c1 != c2 {
		t.Errorf("poll granularity changed the outcome: %+v/%d vs %+v/%d", s1, c1, s2, c2)
	}
}

func TestFakeDefaults_MatchCanary(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("canary-measured.json")
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		ColdStartS    int   `json:"cold_start_s"`
		IdleS         int   `json:"idle_s"`
		KillLatencyMS int64 `json:"kill_latency_ms"`
		WorkersPerJob int   `json:"workers_per_job"`
		Measured      bool  `json:"measured"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	o := fake.New(fake.Options{}).Options()
	if o.ColdStartS != c.ColdStartS || o.IdleS != c.IdleS || o.KillLatencyMS != c.KillLatencyMS {
		t.Errorf("defaults %d/%d/%d != canary %+v", o.ColdStartS, o.IdleS, o.KillLatencyMS, c)
	}
	if c.WorkersPerJob != 1 || o.Adversarial.ExtraWorkersPerJob {
		t.Errorf("friendly fake runs one worker per job: %+v", c)
	}
	if got := fake.CanaryDefaults(); got.ColdStartS != c.ColdStartS || got.Measured != c.Measured {
		t.Errorf("CanaryDefaults %+v", got)
	}
	if o.PricePPH != price || o.WorkersMax != 2 {
		t.Errorf("price/max %d/%d", o.PricePPH, o.WorkersMax)
	}
}
