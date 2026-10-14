package fake_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
)

// TestFake_Adversarial: each C86.3 (b) switch has its observable effect.
func TestFake_Adversarial(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("NoWarmReuse", func(t *testing.T) {
		t.Parallel()
		b, clk := newFake(t, fake.Options{WorkersMax: 2, Adversarial: fake.Adversarial{NoWarmReuse: true}})
		mustSubmit(t, b, spec("j1", 1, "pop", 186))
		clk.Advance(10 * time.Second) // done at 4.2 s; the worker idles but may not serve again
		r2 := mustSubmit(t, b, spec("j2", 1, "pop", 186))
		clk.Advance(300 * time.Millisecond)
		ws := workersOf(t, b)
		if len(ws) != 2 {
			t.Fatalf("a second worker cold-starts although one is idle: %+v", ws)
		}
		if st := mustPoll(t, b, r2); st.State != provider.Queued {
			t.Errorf("the job waits for the cold start: %+v", st)
		}
	})

	t.Run("ExtraWorkersPerJob", func(t *testing.T) {
		t.Parallel()
		b, clk := newFake(t, fake.Options{WorkersMax: 1, Adversarial: fake.Adversarial{ExtraWorkersPerJob: true}})
		mustSubmit(t, b, spec("j1", 1, "pop", 186))
		clk.Advance(time.Second)
		if ws := workersOf(t, b); len(ws) != 2 {
			t.Fatalf("one job, two workers: %+v", ws)
		}
		clk.Advance(time.Minute)
		if ws := workersOf(t, b); len(ws) != 0 {
			t.Errorf("both idle out: %+v", ws)
		}
		// the extra worker is billed: > 2x one worker's lifetime would be false, but > 1x is true
		if got := b.AccruedCost(clk.Now()); got <= money.ForSeconds(24, price) {
			t.Errorf("extra worker not billed: %d", got)
		}
	})

	t.Run("CancelAckButKeepRunning", func(t *testing.T) {
		t.Parallel()
		b, clk := newFake(t, fake.Options{Speed: fake.Speed{Slow: 100 * time.Second}, Adversarial: fake.Adversarial{CancelAckButKeepRunning: true}})
		ref := mustSubmit(t, b, spec("j", 1, "pop", 186))
		clk.Advance(10 * time.Second)
		_ = b.Cancel(ctx, ref)
		if st := mustPoll(t, b, ref); st.State != provider.Cancelled {
			t.Fatalf("%+v", st)
		}
		if ws := workersOf(t, b); countStatus(ws, "RUNNING") != 1 {
			t.Errorf("handler keeps running after the acknowledged cancel: %+v", ws)
		}
		clk.Advance(2 * time.Minute) // without a closed attempt it finishes by itself
		if ws := workersOf(t, b); countStatus(ws, "RUNNING") != 0 {
			t.Errorf("%+v", ws)
		}
		if st := mustPoll(t, b, ref); st.State != provider.Cancelled {
			t.Errorf("a cancelled job stays cancelled: %+v", st)
		}
	})

	t.Run("Requeue", func(t *testing.T) {
		t.Parallel()
		b, clk := newFake(t, fake.Options{Speed: fake.Speed{Slow: 20 * time.Second}, Adversarial: fake.Adversarial{Requeue: true}})
		ref := mustSubmit(t, b, spec("j", 1, "pop", 186))
		clk.Advance(8 * time.Second)
		if st := mustPoll(t, b, ref); st.State != provider.Running {
			t.Fatalf("%+v", st)
		}
		clk.Advance(6 * time.Second)
		if st := mustPoll(t, b, ref); st.State != provider.Queued {
			t.Errorf("requeued after having run: %+v", st)
		}
	})

	t.Run("PurgeResultsAfter30m", func(t *testing.T) {
		t.Parallel()
		b, clk := newFake(t, fake.Options{Adversarial: fake.Adversarial{PurgeResultsAfter30m: true}})
		ref := mustSubmit(t, b, spec("j", 1, "pop", 186))
		clk.Advance(10 * time.Second)
		if st := mustPoll(t, b, ref); st.State != provider.Completed {
			t.Fatal(st)
		}
		clk.Advance(29 * time.Minute)
		if st := mustPoll(t, b, ref); st.State != provider.Completed {
			t.Errorf("kept for 30 minutes: %+v", st)
		}
		clk.Advance(2 * time.Minute)
		if st := mustPoll(t, b, ref); st.State != provider.NotFound {
			t.Errorf("purged: %+v", st)
		}
	})

	t.Run("AcceptThenFailRun", func(t *testing.T) {
		t.Parallel()
		up := &recUploader{}
		b, clk := newWithSim(t, fake.Options{Adversarial: fake.Adversarial{AcceptThenFailRun: 1}}, up)
		ref, err := b.Submit(ctx, spec("j", 1, "pop", 186))
		fe := asErr(t, err)
		if !fe.Ambiguous() || ref != (provider.Ref{}) {
			t.Fatalf("%+v %+v", fe, ref)
		}
		clk.Advance(10 * time.Second)
		_, _ = b.Sweep(ctx)
		if up.startCount() == 0 {
			t.Error("the accepted job still runs (a ghost)")
		}
		zero, clk0 := newFake(t, fake.Options{Adversarial: fake.Adversarial{AcceptThenFailRun: 0}})
		if _, err := zero.Submit(ctx, spec("j", 1, "pop", 186)); err != nil {
			t.Errorf("fraction 0: %v", err)
		}
		_ = clk0
	})

	t.Run("RevisedBucketsAfter", func(t *testing.T) {
		t.Parallel()
		b, clk := newFake(t, fake.Options{Adversarial: fake.Adversarial{RevisedBucketsAfter: 3 * time.Hour}})
		mustSubmit(t, b, spec("j", 1, "pop", 186))
		clk.Advance(2 * time.Hour)
		first, err := b.Billing(ctx, t0, t0.Add(time.Hour))
		if err != nil || len(first) != 1 {
			t.Fatalf("%v %+v", err, first)
		}
		clk.Advance(5 * time.Hour)
		second, _ := b.Billing(ctx, t0, t0.Add(time.Hour))
		if len(second) != 1 || second[0].Total <= first[0].Total || second[0].Compute != second[0].Total {
			t.Errorf("late adjustment must raise the bucket: %+v -> %+v", first, second)
		}
	})

	t.Run("PriceRiseAt", func(t *testing.T) {
		t.Parallel()
		rise := t0.Add(time.Hour)
		b, clk := newFake(t, fake.Options{Adversarial: fake.Adversarial{PriceRiseAt: &rise}})
		q1, _ := b.Quote(ctx)
		clk.Advance(2 * time.Hour)
		q2, _ := b.Quote(ctx)
		if q1.ListPricePPH != price || q2.ListPricePPH <= q1.ListPricePPH {
			t.Errorf("list price %d -> %d", q1.ListPricePPH, q2.ListPricePPH)
		}
		mustSubmit(t, b, spec("j", 1, "pop", 186))
		clk.Advance(time.Minute)
		if got, plain := b.AccruedCost(clk.Now()), money.ForSeconds(24, price); got <= plain {
			t.Errorf("work after the rise bills the new price: %d vs %d", got, plain)
		}
	})

	t.Run("IdleDriftTo and MaxDriftTo", func(t *testing.T) {
		t.Parallel()
		b, clk := newFake(t, fake.Options{Adversarial: fake.Adversarial{IdleDriftTo: 300, MaxDriftTo: 5}})
		r0, _ := b.Sweep(ctx)
		if len(r0.Drift) != 0 {
			t.Fatalf("no drift before work: %+v", r0.Drift)
		}
		mustSubmit(t, b, spec("j", 1, "pop", 186))
		clk.Advance(time.Second)
		r, _ := b.Sweep(ctx)
		got := map[string]provider.Drift{}
		for _, d := range r.Drift {
			got[d.Field] = d
		}
		if d := got["workers.idleTimeout"]; d.Want != "20" || d.Got != "300" {
			t.Errorf("idle drift %+v", r.Drift)
		}
		if d := got["workers.max"]; d.Want != "2" || d.Got != "5" {
			t.Errorf("max drift %+v", r.Drift)
		}
		if r.Endpoint == nil || r.Endpoint.WorkersMax != 5 || r.Endpoint.IdleTimeoutS == nil || *r.Endpoint.IdleTimeoutS != 300 {
			t.Errorf("endpoint %+v", r.Endpoint)
		}
		clk.Advance(2 * time.Minute) // drifted idle: the worker is still alive
		if len(workersOf(t, b)) != 1 {
			t.Error("drifted idle timeout keeps the worker alive")
		}
		if err := b.RevertDrift(ctx, r.Drift); err != nil {
			t.Fatal(err)
		}
		r, _ = b.Sweep(ctx)
		if len(r.Drift) != 0 || r.Endpoint.WorkersMax != 2 || *r.Endpoint.IdleTimeoutS != 20 {
			t.Errorf("reverted: %+v %+v", r.Drift, r.Endpoint)
		}
	})
}

func TestFake_ErrorTypes(t *testing.T) {
	t.Parallel()
	e := &fake.Error{Op: "submit", Status: 429, Code: fake.RateLimited, RetryAfter: 5 * time.Second}
	if e.Error() != "fake: submit: RATE_LIMITED (429)" {
		t.Error(e.Error())
	}
	if !errors.Is(&fake.Error{Code: fake.Unauthorized, Status: 401}, provider.ErrAccess) || !errors.Is(&fake.Error{Code: fake.Forbidden, Status: 403}, provider.ErrAccess) {
		t.Error("401 and 403 are access errors")
	}
	if errors.Is(e, provider.ErrAccess) {
		t.Error("429 is not an access error")
	}
}
