package servertest_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/servertest"
)

func l1Count(t *testing.T, r *servertest.Rig, q string) int {
	t.Helper()
	var n int
	if err := r.Store.R.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// TestRig_L1Options: L1 adds the mirror rows; the default Reconcile (false) builds the Reconciler but leaves the queue
// alone; Rig.Settle drains it and the Songs appear.
func TestRig_L1Options(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true})
	if r.Provenance == nil || r.Services.Provenance == nil {
		t.Fatal("the Reconciler must be built")
	}
	if n := l1Count(t, r, `SELECT COUNT(*) FROM songs`); n != 0 {
		t.Errorf("songs before Settle = %d, want 0", n)
	}
	pending, err := r.Provenance.Pending(context.Background())
	if err != nil || pending == 0 {
		t.Fatalf("queue = %d (%v), want non-empty", pending, err)
	}
	time.Sleep(1200 * time.Millisecond) // an idle Reconciler never touches the queue, even past the poll interval
	if p, _ := r.Provenance.Pending(context.Background()); p != pending {
		t.Errorf("the idle Reconciler changed the queue: %d -> %d", pending, p)
	}
	rep := r.Settle(t)
	if p, _ := r.Provenance.Pending(context.Background()); p != 0 {
		t.Errorf("queue after Settle = %d, want 0", p)
	}
	if rep.SongsCreated == 0 || l1Count(t, r, `SELECT COUNT(*) FROM songs`) != rep.SongsCreated {
		t.Errorf("report %+v does not match the songs table", rep)
	}
	if an, err := r.Provenance.Verify(context.Background()); err != nil || len(an) != 0 {
		t.Errorf("Verify after Settle: %v %v", an, err)
	}
}

// drainProgress polls pending until it reaches 0. It tolerates any total duration so long as the count keeps falling:
// it fails (returns an error) only when the count has not decreased for stall. A wall-clock budget on work that scales
// with CPU load (-race, parallel packages) is what made this test flaky; progress is load-independent.
func drainProgress(pending func() (int, error), now func() time.Time, wait func(), stall time.Duration) error {
	last, err := pending()
	if err != nil {
		return err
	}
	moved := now()
	for last > 0 {
		wait()
		p, err := pending()
		if err != nil {
			return err
		}
		if p < last {
			last, moved = p, now()
		} else if now().Sub(moved) > stall {
			return fmt.Errorf("no progress for %s: %d left", stall, p)
		}
		if p == 0 {
			return nil
		}
	}
	return nil
}

func TestDrainProgress(t *testing.T) {
	t.Parallel()
	type clock struct{ t time.Time }
	run := func(counts []int) error {
		c := &clock{t: time.Unix(0, 0)}
		i := 0
		return drainProgress(
			func() (int, error) {
				if i >= len(counts) {
					return counts[len(counts)-1], nil
				}
				return counts[i], nil
			},
			func() time.Time { return c.t },
			func() { i++; c.t = c.t.Add(time.Second) },
			10*time.Second,
		)
	}
	// 100 polls (100 fake seconds, far past any fixed deadline) with steady progress: fine.
	slow := make([]int, 0, 101)
	for n := 100; n >= 0; n-- {
		slow = append(slow, n)
	}
	if err := run(slow); err != nil {
		t.Errorf("steady progress must pass: %v", err)
	}
	// Progress that then stops fails once the stall window passes.
	if err := run([]int{60, 50, 40}); err == nil {
		t.Error("a stalled drain must fail")
	}
}

// TestRig_L1Reconcile: Options.Reconcile starts Run, which drains the queue by itself.
func TestRig_L1Reconcile(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, Reconcile: true})
	err := drainProgress(
		func() (int, error) { return r.Provenance.Pending(context.Background()) },
		time.Now,
		func() { time.Sleep(50 * time.Millisecond) },
		30*time.Second,
	)
	if err != nil {
		t.Fatalf("Run did not drain the queue: %v", err)
	}
	if l1Count(t, r, `SELECT COUNT(*) FROM songs`) == 0 {
		t.Error("no songs after Run")
	}
}
