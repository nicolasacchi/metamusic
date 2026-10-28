package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/tape"
)

// TestStartL2_StartsAndStops: serve starts the Syncer and the tape runner on the run context and the tape sweep on the prune
// context; all three end with their contexts.
func TestStartL2_StartsAndStops(t *testing.T) {
	t.Parallel()
	svc := &app.Services{Syncer: records.NewSyncer(records.New(nil, nil, nil), nil), Tapes: tape.New(tape.Options{})}
	runCtx, stopRun := context.WithCancel(context.Background())
	pruneCtx, stopPrune := context.WithCancel(context.Background())
	var running, pruned sync.WaitGroup
	startL2(runCtx, pruneCtx, svc, &running, &pruned)
	stopRun()
	stopPrune()
	done := make(chan struct{})
	go func() { running.Wait(); pruned.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the L2 loops did not end with their contexts")
	}
}

// TestStartL2_NilServicesAreSkipped: a Services without the L2 services (mcp-stdio, a partial rig) starts nothing and does not panic.
func TestStartL2_NilServicesAreSkipped(t *testing.T) {
	t.Parallel()
	var running, pruned sync.WaitGroup
	startL2(context.Background(), context.Background(), &app.Services{}, &running, &pruned)
	running.Wait()
	pruned.Wait()
}

type countSweeper struct {
	mu   sync.Mutex
	ages []time.Duration
}

func (c *countSweeper) Sweep(_ context.Context, older time.Duration) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ages = append(c.ages, older)
	return 0, nil
}

// TestSweepTapes_UsesRetention: the sweep runs once at start and expires tapes older than tape.RetentionDays.
func TestSweepTapes_UsesRetention(t *testing.T) {
	t.Parallel()
	c := &countSweeper{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sweepTapes(ctx, c, time.Hour); close(done) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		c.mu.Lock()
		n := len(c.ages)
		c.mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if len(c.ages) == 0 || c.ages[0] != 14*24*time.Hour {
		t.Errorf("sweep ages = %v, want one of 14 days", c.ages)
	}
}

// TestCLI_LibraryL2Hooks: `library verify` runs the L2 invariants after L1's (the stub finds nothing) and accepts --files;
// `library reconcile --dry-run` reports the slots with pending events and a real reconcile runs the Syncer.
func TestCLI_LibraryL2Hooks(t *testing.T) {
	t.Parallel()
	env := libraryEnv(t)
	if code, out, errs := do(t, env, "", "library", "reconcile"); code != 0 {
		t.Fatalf("reconcile: %d %q %q", code, out, errs)
	}
	code, out, errs := do(t, env, "", "library", "verify", "--files")
	if code != 0 || !strings.Contains(out, "consistent") {
		t.Fatalf("verify --files: %d %q %q", code, out, errs)
	}
	code, out, errs = do(t, env, "", "library", "reconcile", "--dry-run")
	if code != 0 || !strings.Contains(out, "records: 0 slots with pending Pick events") || !strings.Contains(out, "nothing written") {
		t.Fatalf("dry-run: %d %q %q", code, out, errs)
	}
}
