package transform

import (
	"context"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// Wait limits (C212.1): timeout 0 to 55 s, default 25.
const (
	MaxWait     = 55 * time.Second
	DefaultWait = 25 * time.Second
	// waitRecheck is the safety net behind the hub: a writer that forgets to broadcast delays a waiter by at most this long.
	waitRecheck = 5 * time.Second
)

// WaitResult is wait_for_transform: the transform, whether its state differs from since_state, and whether the wait timed out.
type WaitResult struct {
	View     View
	Changed  bool
	TimedOut bool
}

// Wait returns as soon as the state differs from sinceState or the transform is terminal, else after timeout with TimedOut set
// (the semantics of wait_for_job). An empty sinceState means the state at call time. A timeout of 0 is a poll; above MaxWait it
// is clamped.
func (s *Service) Wait(ctx context.Context, _ auth.Principal, id, sinceState string, timeout time.Duration) (WaitResult, error) {
	if !s.Enabled() {
		return WaitResult{}, unavailable("transform")
	}
	timeout = min(max(timeout, 0), MaxWait)
	ch, cancel := s.hub.subscribe(id) // subscribe first: a change between the read and the wait must not be lost
	defer cancel()
	v, err := s.view(ctx, s.o.Store.R, id)
	if err != nil {
		return WaitResult{}, err
	}
	if sinceState == "" {
		sinceState = v.State
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		if v.State != sinceState || isTerminal(v.State) {
			return WaitResult{View: v, Changed: v.State != sinceState}, nil
		}
		select {
		case <-ctx.Done():
			return WaitResult{}, ctx.Err()
		case <-deadline.C:
			return WaitResult{View: v, TimedOut: true}, nil
		case <-ch:
		case <-time.After(waitRecheck):
		}
		if v, err = s.view(ctx, s.o.Store.R, id); err != nil {
			return WaitResult{}, err
		}
	}
}

func isTerminal(state string) bool {
	return state == StateDone || state == StateFailed || state == StateCancelled
}
