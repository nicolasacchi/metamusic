package jobs

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type waitOut struct {
	res WaitResult
	err error
}

func (r *rig) service(context.Context) *Service {
	return New(Options{Store: r.st, Clock: r.clk, Hub: r.hub})
}

func (r *rig) waitAsync(ctx context.Context, s *Service, job, since string, timeout time.Duration) <-chan waitOut {
	ch := make(chan waitOut, 1)
	go func() {
		res, err := s.Wait(ctx, auth.Principal{}, job, since, timeout)
		ch <- waitOut{res, err}
	}()
	return ch
}

// armed waits until Wait has created its timers (deadline and re-check).
func (r *rig) timersUp() {
	r.t.Helper()
	waitFor(r.t, func() bool { return r.clk.Pending() >= 2 })
}

func TestWait_ReturnsOnChange(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.disarm()
	job := r.seedBatch(batchSpec{}).Jobs[0]
	s := r.service(context.Background())
	ch := r.waitAsync(t.Context(), s, job, StateQueued, 30*time.Second)
	r.timersUp()
	select {
	case o := <-ch:
		t.Fatalf("returned early: %+v", o)
	default:
	}
	// the runner moves the job and nudges the hub: Wait returns without any clock movement
	r.cancelIntent(job)
	r.step()
	select {
	case o := <-ch:
		if o.err != nil || !o.res.Changed || o.res.TimedOut || o.res.Job.State != StateCancelled {
			t.Fatalf("%+v", o)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return on the hub nudge")
	}
}

func TestWait_TerminalImmediately(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.disarm()
	job := r.seedBatch(batchSpec{}).Jobs[0]
	r.cancelIntent(job)
	r.step()
	s := r.service(context.Background())
	// since == the terminal state itself: still returns at once
	res, err := s.Wait(t.Context(), auth.Principal{}, job, StateCancelled, 30*time.Second)
	if err != nil || !res.Changed || res.TimedOut || res.Job.State != StateCancelled || res.Job.ErrorCode == nil || *res.Job.ErrorCode != CodeCancelledByUser {
		t.Fatalf("%+v %v", res, err)
	}
	if r.clk.Pending() != 0 {
		t.Errorf("timers left behind: %d", r.clk.Pending())
	}
}

func TestWait_TimeoutZero(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.disarm()
	job := r.seedBatch(batchSpec{}).Jobs[0]
	s := r.service(context.Background())
	res, err := s.Wait(t.Context(), auth.Principal{}, job, StateQueued, 0)
	if err != nil || res.Changed || !res.TimedOut || res.Job.State != StateQueued {
		t.Fatalf("%+v %v", res, err)
	}
	res, err = s.Wait(t.Context(), auth.Principal{}, job, StateAwaiting, 0)
	if err != nil || !res.Changed || res.TimedOut {
		t.Fatalf("a different since returns changed: %+v %v", res, err)
	}
	if _, err := s.Wait(t.Context(), auth.Principal{}, "no-such-job", "", 0); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("unknown job: %v", err)
	}
}

func TestWait_TimeoutElapses(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.disarm()
	job := r.seedBatch(batchSpec{}).Jobs[0]
	s := r.service(context.Background())
	ch := r.waitAsync(t.Context(), s, job, StateQueued, 10*time.Second)
	r.timersUp()
	for i := 0; i < 12; i++ {
		r.advance(time.Second)
		time.Sleep(time.Millisecond)
	}
	select {
	case o := <-ch:
		if o.err != nil || o.res.Changed || !o.res.TimedOut {
			t.Fatalf("%+v", o)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no timeout")
	}
	// a timeout above 55 s is clamped
	ch = r.waitAsync(t.Context(), s, job, StateQueued, time.Hour)
	r.timersUp()
	r.advance(56 * time.Second)
	select {
	case o := <-ch:
		if !o.res.TimedOut {
			t.Fatalf("%+v", o)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the 55 s ceiling did not apply")
	}
}

func TestWait_ContextCancel(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.disarm()
	job := r.seedBatch(batchSpec{}).Jobs[0]
	s := r.service(context.Background())
	ctx, cancel := context.WithCancel(t.Context())
	ch := r.waitAsync(ctx, s, job, StateQueued, 30*time.Second)
	r.timersUp()
	cancel()
	select {
	case o := <-ch:
		if !errors.Is(o.err, context.Canceled) {
			t.Fatalf("%+v", o)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait ignored the request context")
	}
}

func TestWait_ReturnsOnShutdown(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.disarm()
	job := r.seedBatch(batchSpec{}).Jobs[0]
	shutdown, begin := context.WithCancel(t.Context())
	s := r.service(shutdown)
	// the request context derives from the server's BaseContext, which is cancelled at shutdown too, and
	// carries its Done channel (reqinfo.WithShutdown)
	reqCtx, cancelReq := context.WithCancel(reqinfo.WithShutdown(shutdown, shutdown.Done()))
	defer cancelReq()
	ch := r.waitAsync(reqCtx, s, job, StateQueued, 30*time.Second)
	r.timersUp()
	begin()
	select {
	case o := <-ch:
		if o.err != nil || !o.res.TimedOut || o.res.Job.State != StateQueued {
			t.Fatalf("%+v", o)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait held the shutdown drain")
	}
}

func TestWait_DBRecheckWithoutHub(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.disarm()
	job := r.seedBatch(batchSpec{}).Jobs[0]
	// another process owns the runner: the state changes behind this process's back (no hub nudge)
	s := New(Options{Store: r.st, Clock: r.clk}) // a private hub nobody nudges
	ch := r.waitAsync(t.Context(), s, job, StateQueued, 30*time.Second)
	r.timersUp()
	r.exec(`UPDATE jobs SET state = 'cancelled', error_code = 'CANCELLED_BY_USER', finished_at = ? WHERE id = ?`, store.FormatTime(t0), job)
	select {
	case o := <-ch:
		t.Fatalf("returned before the re-check: %+v", o)
	case <-time.After(20 * time.Millisecond):
	}
	r.advance(time.Second) // the 1 s re-check fires
	select {
	case o := <-ch:
		if o.err != nil || !o.res.Changed || o.res.Job.State != StateCancelled {
			t.Fatalf("%+v", o)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the database re-check never fired")
	}
}
