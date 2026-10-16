package provenance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestSettle_TwoSettlers: two Reconcilers on one database file with different owners. While one holds the lease (its
// BeforeSettle is blocked) the other gets ErrSettleBusy; a lease past its expires_at is taken over; a settler whose epoch
// went stale writes nothing (ErrLeaseLost).
func TestSettle_TwoSettlers(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	w := newWorld(t, func(o *provenance.Options) {
		o.Owner = "settler-a"
		o.BeforeSettle = func(context.Context) {
			select {
			case entered <- struct{}{}:
				<-release
			default:
			}
		}
	})
	w.sc.Draft("a_v1", librarytest.DraftOpts{})
	st2, err := store.Open(context.Background(), w.path) // a second handle on the same file, as a second process has
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	b := w.service(st2, func(o *provenance.Options) { o.Owner = "settler-b" })

	done := make(chan error, 1)
	go func() { _, err := w.prov.Settle(context.Background()); done <- err }()
	<-entered
	if _, err := b.Settle(context.Background()); !errors.Is(err, provenance.ErrSettleBusy) {
		t.Errorf("the second settler got %v, want ErrSettleBusy", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("the first settler: %v", err)
	}
	if p, _ := w.prov.Pending(context.Background()); p != 0 {
		t.Errorf("the first settler left %d keys", p)
	}
	if held, _ := b.LeaseHeld(context.Background()); held {
		t.Errorf("the lease must be released after Settle")
	}

	// takeover after expiry: a dead settler's lease past expires_at is taken over
	w.sc.Draft("b_v1", librarytest.DraftOpts{})
	w.exec(`UPDATE provenance_lease SET owner = 'dead', expires_at = '2000-01-01T00:00:00.000Z'`)
	if _, err := b.Settle(context.Background()); err != nil {
		t.Errorf("an expired lease must be taken over: %v", err)
	}
	if p, _ := b.Pending(context.Background()); p != 0 {
		t.Errorf("queue = %d after the takeover", p)
	}
	// a live lease of another owner blocks
	w.exec(`UPDATE provenance_lease SET owner = 'someone', expires_at = '2999-01-01T00:00:00.000Z'`)
	if _, err := b.Settle(context.Background()); !errors.Is(err, provenance.ErrSettleBusy) {
		t.Errorf("a live foreign lease must block: %v", err)
	}
	if held, _ := b.LeaseHeld(context.Background()); !held {
		t.Errorf("LeaseHeld must report the foreign lease")
	}
}

func TestSettle_StaleEpochWritesNothing(t *testing.T) {
	t.Parallel()
	taken := make(chan struct{})
	w := newWorld(t, func(o *provenance.Options) {
		o.Owner = "settler-a"
		o.BeforeSettle = func(context.Context) {
			// another process takes the lease over while this settler is between acquiring it and its first pass
			if _, err := o.Store.W.Exec(`UPDATE provenance_lease SET owner = 'settler-b', epoch = epoch + 1`); err != nil {
				panic(err)
			}
			close(taken)
		}
	})
	w.sc.Draft("a_v1", librarytest.DraftOpts{})
	_, err := w.prov.Settle(context.Background())
	<-taken
	if !errors.Is(err, provenance.ErrLeaseLost) {
		t.Fatalf("Settle with a stale epoch returned %v, want ErrLeaseLost", err)
	}
	if n := w.count(`SELECT COUNT(*) FROM songs`); n != 0 {
		t.Errorf("a stale settler wrote %d Songs", n)
	}
	if p, _ := w.prov.Pending(context.Background()); p != 1 {
		t.Errorf("the queue must be intact: %d", p)
	}
}

func TestLease_EpochBumpsAndSameOwnerReacquires(t *testing.T) {
	t.Parallel()
	w := newWorld(t, func(o *provenance.Options) { o.Owner = "o1" })
	for want := 2; want <= 4; want++ { // the seeded row is epoch 1; every acquisition bumps it
		w.settle()
		if got := w.str(`SELECT epoch FROM provenance_lease`); got != string(rune('0'+want)) {
			t.Errorf("epoch = %q after acquisition %d", got, want-1)
		}
		if owner := w.str(`SELECT owner FROM provenance_lease`); owner != "" {
			t.Errorf("owner %q: the lease is released after Settle", owner)
		}
	}
}
