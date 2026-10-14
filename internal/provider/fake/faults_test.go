package fake_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
)

// asErr unwraps the classified error a faulty Submit returns.
func asErr(t *testing.T, err error) *fake.Error {
	t.Helper()
	var fe *fake.Error
	if !errors.As(err, &fe) {
		t.Fatalf("not a *fake.Error: %v", err)
	}
	return fe
}

func decodeOutput(t *testing.T, raw json.RawMessage) provider.Output {
	t.Helper()
	var wrapped struct {
		Error string          `json:"error"`
		MM    json.RawMessage `json:"mm"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && len(wrapped.MM) > 0 {
		raw = wrapped.MM
	}
	out, err := provider.DecodeOutput(raw)
	if err != nil {
		t.Fatalf("output: %v\n%s", err, raw)
	}
	return out
}

func TestFakeFaults_Table(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("slow", func(t *testing.T) {
		t.Parallel()
		b, clk := newFake(t, fake.Options{})
		ref := mustSubmit(t, b, spec("j", 1, "pop fake:slow=5", 186))
		clk.Advance(3200*time.Millisecond + 4900*time.Millisecond)
		if st := mustPoll(t, b, ref); st.State != provider.Running {
			t.Errorf("%+v", st)
		}
		clk.Advance(200 * time.Millisecond)
		if st := mustPoll(t, b, ref); st.State != provider.Completed || ms(st.ExecutionMS) != 5000 {
			t.Errorf("%+v", st)
		}
	})

	t.Run("cold", func(t *testing.T) {
		t.Parallel()
		b, clk := newFake(t, fake.Options{})
		ref := mustSubmit(t, b, spec("j", 1, "pop,fake:cold=10", 186))
		clk.Advance(10 * time.Second)
		if st := mustPoll(t, b, ref); st.State != provider.Queued {
			t.Errorf("still cold at 10 s: %+v", st)
		}
		clk.Advance(300 * time.Millisecond)
		if st := mustPoll(t, b, ref); st.State != provider.Running {
			t.Errorf("%+v", st)
		}
	})

	t.Run("fail=worker_lost", func(t *testing.T) {
		t.Parallel()
		b, clk := newFake(t, fake.Options{})
		ref := mustSubmit(t, b, spec("j", 1, "pop fake:fail=worker_lost", 186))
		clk.Advance(10 * time.Second)
		st := mustPoll(t, b, ref)
		if st.State != provider.Failed || st.ExecutionMS != nil || len(st.Output) != 0 || st.ErrorText == "" {
			t.Errorf("%+v", st)
		}
		if ws := workersOf(t, b); len(ws) != 0 {
			t.Errorf("the worker vanished: %+v", ws)
		}
	})

	t.Run("fail=worker_error", func(t *testing.T) {
		t.Parallel()
		up := &recUploader{}
		b, clk := newWithSim(t, fake.Options{}, up)
		ref := mustSubmit(t, b, spec("j", 1, "pop fake:fail=worker_error", 186))
		clk.Advance(10 * time.Second)
		st := mustPoll(t, b, ref)
		if st.State != provider.Failed || ms(st.ExecutionMS) != 1000 {
			t.Fatalf("%+v", st)
		}
		out := decodeOutput(t, st.Output)
		if out.Status != "error" || out.Error == nil || out.Error.Code != "PIPELINE_ERROR" {
			t.Errorf("%+v", out)
		}
		if n := up.names(); len(n) == 0 || n[len(n)-1] != provider.ResultArtifact {
			t.Errorf("best-effort mm-result.json: %v", n)
		}
	})

	for _, which := range []string{"abc", "semantic"} {
		t.Run("truncate="+which, func(t *testing.T) {
			t.Parallel()
			up := &recUploader{}
			b, clk := newWithSim(t, fake.Options{}, up)
			ref := mustSubmit(t, b, spec("j", 1, "pop fake:truncate="+which, 186))
			clk.Advance(10 * time.Second)
			st := mustPoll(t, b, ref)
			out := decodeOutput(t, st.Output)
			if st.State != provider.Completed || out.Status != "ok" {
				t.Fatalf("%+v %+v", st, out)
			}
			want := map[string]bool{"abc": which == "abc", "semantic": which == "semantic"}
			if len(out.Truncated) != 2 || out.Truncated["abc"] != want["abc"] || out.Truncated["semantic"] != want["semantic"] {
				t.Errorf("truncated %v", out.Truncated)
			}
			var res struct {
				Truncated map[string]bool `json:"truncated"`
			}
			if err := json.Unmarshal(up.body("result.json"), &res); err != nil || res.Truncated["abc"] != want["abc"] || res.Truncated["semantic"] != want["semantic"] {
				t.Errorf("result.json truncated %v (%v)", res.Truncated, err)
			}
		})
	}

	t.Run("sha_mismatch", func(t *testing.T) {
		t.Parallel()
		up := &recUploader{}
		b, clk := newWithSim(t, fake.Options{}, up)
		ref := mustSubmit(t, b, spec("j", 1, "pop fake:sha_mismatch", 186))
		clk.Advance(10 * time.Second)
		out := decodeOutput(t, mustPoll(t, b, ref).Output)
		reported := out.Artifacts["audio.flac"].SHA256
		sum := sha256.Sum256(up.body("audio.flac"))
		if reported == "" || reported == hex.EncodeToString(sum[:]) {
			t.Errorf("reported sha must differ from the uploaded bytes: %q", reported)
		}
		for _, name := range []string{"request.json", "config.json", "result.json"} {
			if len(up.body(name)) == 0 {
				t.Errorf("%s missing", name)
			}
		}
	})

	t.Run("queue_expire", func(t *testing.T) {
		t.Parallel()
		b, clk := newFake(t, fake.Options{})
		r1 := mustSubmit(t, b, spec("j", 1, "pop fake:queue_expire=2", 186))
		r2 := mustSubmit(t, b, spec("j", 2, "pop fake:queue_expire=2", 186))
		r3 := mustSubmit(t, b, spec("j", 3, "pop fake:queue_expire=2", 186))
		clk.Advance(time.Hour)
		for _, r := range []provider.Ref{r1, r2} {
			if st := mustPoll(t, b, r); st.State != provider.Queued {
				t.Errorf("still in the queue before the TTL: %+v", st)
			}
		}
		if st := mustPoll(t, b, r3); st.State != provider.Completed {
			t.Errorf("third attempt runs: %+v", st)
		}
		clk.Advance(time.Hour + time.Second)
		for _, r := range []provider.Ref{r1, r2} {
			st := mustPoll(t, b, r)
			if st.State != provider.TimedOut || st.ExecutionMS != nil || st.Raw != "TIMED_OUT" {
				t.Errorf("TTL expiry shape: %+v", st)
			}
		}
	})

	t.Run("submit", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			marker    string
			code      fake.Code
			status    int
			retryable bool
			ambiguous bool
			access    bool
		}{
			{"429", fake.RateLimited, 429, true, false, false},
			{"5xx", fake.Upstream, 503, true, true, false},
			{"paused", fake.EndpointPaused, 409, true, false, false},
			{"1010", fake.EdgeBlocked, 403, false, false, true},
			{"network", fake.Network, 0, true, true, false},
		}
		for _, c := range cases {
			b, clk := newFake(t, fake.Options{})
			_, err := b.Submit(ctx, spec("j", 1, "pop fake:submit="+c.marker, 186))
			fe := asErr(t, err)
			if fe.Code != c.code || fe.Status != c.status || fe.Retryable() != c.retryable || fe.Ambiguous() != c.ambiguous || fe.Access() != c.access {
				t.Errorf("%s: %+v", c.marker, fe)
			}
			if c.access != errors.Is(err, provider.ErrAccess) {
				t.Errorf("%s: ErrAccess mapping", c.marker)
			}
			if c.marker == "429" && fe.RetryAfter <= 0 {
				t.Error("429 carries Retry-After")
			}
			if b.Counters().Submit != 1 {
				t.Errorf("submit counter")
			}
			// no job was created for a refused submit; the second attempt is not faulty
			ref := mustSubmit(t, b, spec("j", 2, "pop fake:submit="+c.marker, 186))
			clk.Advance(10 * time.Second)
			if st := mustPoll(t, b, ref); st.State != provider.Completed {
				t.Errorf("%s: second attempt %+v", c.marker, st)
			}
		}
	})

	t.Run("submit=accepted_5xx", func(t *testing.T) {
		t.Parallel()
		up := &recUploader{}
		b, clk := newWithSim(t, fake.Options{}, up)
		ref, err := b.Submit(ctx, spec("j", 1, "pop fake:submit=accepted_5xx", 186))
		fe := asErr(t, err)
		if fe.Code != fake.Upstream || !fe.Ambiguous() || ref != (provider.Ref{}) {
			t.Fatalf("%+v %+v", fe, ref)
		}
		clk.Advance(10 * time.Second)
		_, _ = b.Sweep(ctx) // drives the simulation; the ghost runs and announces itself
		if up.startCount() == 0 {
			t.Fatal("the ghost must call the start URL")
		}
		ghost := provider.Ref{Backend: provider.BackendFake, ID: up.starts[0].Ref}
		if st := mustPoll(t, b, ghost); st.State != provider.Completed {
			t.Errorf("ghost by its announced id: %+v", st)
		}
	})

	t.Run("upload_closed", func(t *testing.T) {
		t.Parallel()
		up := &recUploader{}
		b, clk := newWithSim(t, fake.Options{}, up)
		ref := mustSubmit(t, b, spec("j", 1, "pop fake:upload_closed", 186))
		clk.Advance(10 * time.Second)
		st := mustPoll(t, b, ref)
		out := decodeOutput(t, st.Output)
		if st.State != provider.Failed || out.Error == nil || out.Error.Code != "UPLOAD_CLOSED" {
			t.Errorf("%+v %+v", st, out)
		}
		if len(up.names()) != 0 {
			t.Errorf("a closed upload channel receives nothing: %v", up.names())
		}
	})

	t.Run("no_start", func(t *testing.T) {
		t.Parallel()
		up := &recUploader{}
		b, clk := newWithSim(t, fake.Options{}, up)
		ref := mustSubmit(t, b, spec("j", 1, "pop fake:no_start", 186))
		clk.Advance(10 * time.Second)
		if st := mustPoll(t, b, ref); st.State != provider.Completed {
			t.Errorf("%+v", st)
		}
		if up.startCount() != 0 {
			t.Errorf("start called %d times", up.startCount())
		}
	})

	t.Run("same_audio", func(t *testing.T) {
		t.Parallel()
		up := &recUploader{}
		b, clk := newWithSim(t, fake.Options{}, up)
		mustSubmit(t, b, spec("j1", 1, "pop fake:same_audio", 186))
		mustSubmit(t, b, spec("j2", 1, "pop fake:same_audio", 186))
		clk.Advance(30 * time.Second)
		_, _ = b.Sweep(ctx)
		var bodies [][]byte
		up.mu.Lock()
		for _, p := range up.puts {
			if p.Name == "audio.flac" {
				bodies = append(bodies, p.Body)
			}
		}
		up.mu.Unlock()
		if len(bodies) != 2 || string(bodies[0]) != string(bodies[1]) {
			t.Errorf("same_audio must give identical audio: %d", len(bodies))
		}
	})

	t.Run("stuck_worker", func(t *testing.T) {
		t.Parallel()
		b, clk := newFake(t, fake.Options{})
		mustSubmit(t, b, spec("j", 1, "pop fake:stuck_worker", 186))
		clk.Advance(time.Hour)
		ws := workersOf(t, b)
		if len(ws) != 1 || ws[0].Status != "RUNNING" {
			t.Fatalf("a RUNNING worker with no job for the rest of the session: %+v", ws)
		}
		if err := b.ScaleToZero(ctx); err != nil {
			t.Fatal(err)
		}
		if ws := workersOf(t, b); len(ws) != 0 {
			t.Errorf("scale to zero removes it: %+v", ws)
		}
	})

	t.Run("cancel_ignored", func(t *testing.T) {
		t.Parallel()
		b, clk := newFake(t, fake.Options{})
		ref := mustSubmit(t, b, spec("j", 1, "pop fake:slow=600 fake:cancel_ignored=2", 186))
		clk.Advance(10 * time.Second)
		if err := b.Cancel(ctx, ref); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if st := mustPoll(t, b, ref); st.State != provider.Running {
				t.Fatalf("poll %d after cancel: %+v", i+1, st)
			}
		}
		if st := mustPoll(t, b, ref); st.State != provider.Cancelled {
			t.Errorf("third poll: %+v", st)
		}
	})

	t.Run("cancel_ack_keeps_running", func(t *testing.T) {
		t.Parallel()
		up := &recUploader{}
		b, clk := newWithSim(t, fake.Options{}, up)
		ref := mustSubmit(t, b, spec("j", 1, "pop fake:slow=600 fake:cancel_ack_keeps_running", 586))
		clk.Advance(10 * time.Second)
		if err := b.Cancel(ctx, ref); err != nil {
			t.Fatal(err)
		}
		if st := mustPoll(t, b, ref); st.State != provider.Cancelled {
			t.Fatalf("acknowledged: %+v", st)
		}
		if ws := workersOf(t, b); countStatus(ws, "RUNNING") != 1 {
			t.Fatalf("the handler keeps running: %+v", ws)
		}
		// the app closes the attempt: the next start poll (every 15 s from the start) answers 410
		up.mu.Lock()
		up.startCode = func(int) int { return 410 }
		up.mu.Unlock()
		clk.Advance(20 * time.Second)
		if ws := workersOf(t, b); countStatus(ws, "RUNNING") != 0 {
			t.Errorf("a 410 from the start URL stops the handler: %+v", ws)
		}
		if len(up.names()) != 0 {
			t.Errorf("a closed attempt uploads nothing: %v", up.names())
		}
		if st := mustPoll(t, b, ref); st.State != provider.Cancelled {
			t.Errorf("still cancelled: %+v", st)
		}
	})

	t.Run("requeue", func(t *testing.T) {
		t.Parallel()
		b, clk := newFake(t, fake.Options{Speed: fake.Speed{Slow: 20 * time.Second}})
		ref := mustSubmit(t, b, spec("j", 1, "pop fake:requeue", 186))
		clk.Advance(8 * time.Second) // running since 3.2 s
		if st := mustPoll(t, b, ref); st.State != provider.Running {
			t.Fatalf("%+v", st)
		}
		clk.Advance(6 * time.Second) // requeued halfway through the run (13.2 s)
		if st := mustPoll(t, b, ref); st.State != provider.Queued {
			t.Fatalf("requeued: %+v", st)
		}
		started := 0
		for i := 0; i < 120; i++ {
			clk.Advance(time.Second)
			started = max2(started, len(workersOf(t, b)))
		}
		if st := mustPoll(t, b, ref); st.State != provider.Completed {
			t.Errorf("finally: %+v", st)
		}
		if started < 1 {
			t.Error("a new worker takes over")
		}
		// two executions were billed: more than one run of cold start + 20 s + idle
		if got := b.AccruedCost(clk.Now()); got <= 0 {
			t.Error("cost")
		}
	})

	t.Run("run_between_polls", func(t *testing.T) {
		t.Parallel()
		b, clk := newFake(t, fake.Options{})
		ref := mustSubmit(t, b, spec("j", 1, "pop fake:run_between_polls", 186))
		clk.Advance(3600 * time.Millisecond) // mid-run
		if st := mustPoll(t, b, ref); st.State != provider.Queued {
			t.Errorf("a poll mid-run still sees the queue: %+v", st)
		}
		clk.Advance(2 * time.Second)
		if st := mustPoll(t, b, ref); st.State != provider.Completed || ms(st.ExecutionMS) != 1000 {
			t.Errorf("%+v", st)
		}
	})
}
