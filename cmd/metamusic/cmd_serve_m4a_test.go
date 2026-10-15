package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type fakeSweeper struct {
	mu    sync.Mutex
	ages  []time.Duration
	calls chan struct{}
}

func (f *fakeSweeper) Sweep(_ context.Context, older time.Duration) (int, error) {
	f.mu.Lock()
	f.ages = append(f.ages, older)
	f.mu.Unlock()
	select {
	case f.calls <- struct{}{}:
	default:
	}
	return 0, nil
}

// TestSweepM4aCaches_RunsAtStartAndStops: covers are swept at 30 days, downloads at 7, once at start and then on the ticker.
func TestSweepM4aCaches_RunsAtStartAndStops(t *testing.T) {
	t.Parallel()
	covers := &fakeSweeper{calls: make(chan struct{}, 8)}
	dls := &fakeSweeper{calls: make(chan struct{}, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sweepM4aCaches(ctx, covers, dls, 20*time.Millisecond); close(done) }()
	for _, f := range []*fakeSweeper{covers, dls} {
		for range 2 { // at start and at least one tick
			select {
			case <-f.calls:
			case <-time.After(5 * time.Second):
				t.Fatal("sweeper not called")
			}
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sweep loop did not stop with its context")
	}
	covers.mu.Lock()
	dls.mu.Lock()
	defer covers.mu.Unlock()
	defer dls.mu.Unlock()
	if covers.ages[0] != 30*24*time.Hour || dls.ages[0] != 7*24*time.Hour {
		t.Errorf("ages: covers %v downloads %v, want 30 d and 7 d", covers.ages[0], dls.ages[0])
	}
}

func TestSweepM4aCaches_NilServicesAreSkipped(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sweepM4aCaches(ctx, nil, nil, time.Hour); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("did not stop")
	}
}

// TestPruneSubsonicSalts_RunsAtStartAndStops: salts older than the media window plus an hour are removed at start.
func TestPruneSubsonicSalts_RunsAtStartAndStops(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	old := store.FormatTime(time.Now().Add(-48 * time.Hour))
	fresh := store.FormatTime(time.Now())
	for _, r := range [][2]string{{"c1", old}, {"c2", fresh}} {
		if _, err := st.W.Exec(`INSERT INTO subsonic_salts(client_id, salt_sha256, first_seen_at) VALUES(?,?,?)`, r[0], "aa", r[1]); err != nil {
			t.Fatal(err)
		}
	}
	creds := auth.NewSubsonicStore(st, []byte("0123456789abcdef0123456789abcdef"), time.Now, nil, nil)
	pctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { pruneSubsonicSalts(pctx, creds, time.Hour); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var n int
		_ = st.R.QueryRow(`SELECT COUNT(*) FROM subsonic_salts`).Scan(&n)
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("old salt not pruned at start (%d rows)", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("prune loop did not stop with its context")
	}
	var client string
	if err := st.R.QueryRow(`SELECT client_id FROM subsonic_salts`).Scan(&client); err != nil || client != "c2" {
		t.Errorf("remaining salt %q (%v), want the fresh one", client, err)
	}
}
