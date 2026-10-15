package servertest_test

import (
	"context"
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

// TestRig_L1Reconcile: Options.Reconcile starts Run, which drains the queue by itself.
func TestRig_L1Reconcile(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, Reconcile: true})
	deadline := time.Now().Add(20 * time.Second)
	for {
		p, err := r.Provenance.Pending(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if p == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Run did not drain the queue: %d left", p)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if l1Count(t, r, `SELECT COUNT(*) FROM songs`) == 0 {
		t.Error("no songs after Run")
	}
}
