package provenance_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/ladder"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/picks"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/sounds"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var errBoom = errors.New("boom")

// sweepPicks is a pick step with a Sweep entry (the optional pickSweeper) and a recompute that may fail.
type sweepPicks struct {
	sweepErr, recomputeErr error
	sweeps                 atomic.Int32
}

func (p *sweepPicks) RecomputeTx(context.Context, *sql.Tx, store.Env, []string, map[string]ladder.Strength) (picks.Changes, error) {
	return picks.Changes{}, p.recomputeErr
}

func (p *sweepPicks) Sweep(context.Context) (picks.Changes, error) {
	p.sweeps.Add(1)
	return picks.Changes{}, p.sweepErr
}

// scriptSounds returns the scripted results of SweepStaleTx in order (the last repeats).
type scriptSounds struct {
	results []int
	err     error
	calls   atomic.Int32
}

func (s *scriptSounds) ComputeTx(context.Context, *sql.Tx, store.Env, []string, []string) (sounds.Changes, error) {
	return sounds.Changes{}, nil
}

func (s *scriptSounds) SweepStaleTx(context.Context, *sql.Tx, store.Env, int) (int, error) {
	i := int(s.calls.Add(1)) - 1
	if s.err != nil {
		return 0, s.err
	}
	if i >= len(s.results) {
		i = len(s.results) - 1
	}
	return s.results[i], nil
}

func TestSweep_Branches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := []struct {
		name    string
		picks   provenance.PickStep
		sounds  provenance.SoundStep
		wantErr error
		loops   int32 // expected SweepStaleTx calls
	}{
		{"no steps", nil, nil, nil, 0},
		{"picks without a sweep entry, no sounds", activePicks{}, nil, nil, 0},
		{"picks sweep error, no sounds", &sweepPicks{sweepErr: errBoom}, nil, errBoom, 0},
		{"picks sweep unsupported, no sounds", &sweepPicks{sweepErr: errors.ErrUnsupported}, nil, nil, 0},
		{"picks sweep error with sounds", &sweepPicks{sweepErr: errBoom}, &scriptSounds{results: []int{0}}, errBoom, 0},
		{"picks sweep unsupported with sounds", &sweepPicks{sweepErr: errors.ErrUnsupported}, &scriptSounds{results: []int{0}}, nil, 1},
		{"sounds loop until nothing is stale", &sweepPicks{}, &scriptSounds{results: []int{5, 3, 0}}, nil, 3},
		{"sounds unsupported", &sweepPicks{}, &scriptSounds{err: errors.ErrUnsupported}, nil, 1},
		{"sounds error", &sweepPicks{}, &scriptSounds{err: errBoom}, errBoom, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t, func(o *provenance.Options) {
				o.Picks, o.Sounds = tc.picks, tc.sounds
			})
			err := w.prov.Sweep(ctx)
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("Sweep = %v, want %v", err, tc.wantErr)
			}
			if ss, ok := tc.sounds.(*scriptSounds); ok && ss.calls.Load() != tc.loops {
				t.Errorf("SweepStaleTx calls = %d, want %d", ss.calls.Load(), tc.loops)
			}
			if held, _ := w.prov.LeaseHeld(ctx); held {
				t.Errorf("Sweep left the lease held")
			}
		})
	}
}

func TestSweep_BusyWhenAnotherSettlerHoldsTheLease(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.exec(`UPDATE provenance_lease SET owner = 'someone', expires_at = '2999-01-01T00:00:00.000Z'`)
	if err := w.prov.Sweep(context.Background()); !errors.Is(err, provenance.ErrSettleBusy) {
		t.Fatalf("Sweep = %v, want ErrSettleBusy", err)
	}
}

// TestSettle_LeaseStolenBetweenRenewAndPhase: a trigger bumps the epoch right after the renew succeeded, so the phase
// transaction's own epoch check (not the renew) refuses and nothing is written.
func TestSettle_LeaseStolenBetweenRenewAndPhase(t *testing.T) {
	t.Parallel()
	w := newWorld(t, func(o *provenance.Options) {
		o.BeforeSettle = func(ctx context.Context) {
			if _, err := o.Store.W.ExecContext(ctx, `CREATE TRIGGER steal AFTER UPDATE OF expires_at ON provenance_lease
				BEGIN UPDATE provenance_lease SET epoch = epoch + 100 WHERE id = 1; END`); err != nil {
				panic(err)
			}
		}
	})
	w.sc.Draft("a_v1", librarytest.DraftOpts{})
	if _, err := w.prov.Settle(context.Background()); !errors.Is(err, provenance.ErrLeaseLost) {
		t.Fatalf("Settle = %v, want ErrLeaseLost", err)
	}
	if n := w.count(`SELECT COUNT(*) FROM songs`); n != 0 {
		t.Errorf("a settler with a stale epoch wrote %d Songs", n)
	}
}

// waitFor polls cond until it holds or the deadline passes (real time: Run drives real tickers and SQLite).
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestRun_LogsFailuresAndStopsWithTheContext: a failing sweep and a failing settle are logged (ids only, no payload) and
// the loop keeps running until its context ends.
func TestRun_LogsFailuresAndStopsWithTheContext(t *testing.T) {
	t.Parallel()
	p := &sweepPicks{sweepErr: errBoom, recomputeErr: errBoom}
	w := newWorld(t, func(o *provenance.Options) {
		o.Picks = p
		o.Poll, o.SweepEvery = 5*time.Millisecond, 5*time.Millisecond
	})
	w.sc.Draft("a_v1", librarytest.DraftOpts{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.prov.Run(ctx); close(done) }()
	waitFor(t, "the sweep and settle warnings", func() bool {
		s := w.log.String()
		return strings.Contains(s, "provenance sweep failed") && strings.Contains(s, "provenance settle failed") && p.sweeps.Load() >= 2
	})
	cancel()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not stop with its context")
	}
}

func TestRun_SettlesAndSweepsWhenHealthy(t *testing.T) {
	t.Parallel()
	p := &sweepPicks{}
	w := newWorld(t, func(o *provenance.Options) {
		o.Picks = p
		o.Poll, o.SweepEvery = 5*time.Millisecond, 5*time.Millisecond
	})
	w.sc.Draft("a_v1", librarytest.DraftOpts{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.prov.Run(ctx); close(done) }()
	waitFor(t, "the queue to drain and a sweep", func() bool {
		n, err := w.prov.Pending(ctx)
		return err == nil && n == 0 && p.sweeps.Load() >= 2
	})
	cancel()
	<-done
	if strings.Contains(w.log.String(), "failed") {
		t.Errorf("a healthy Run logged a failure: %s", w.log.String())
	}
}

func TestNew_DefaultsAndFormatQueue(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := provenance.New(provenance.Options{Store: w.st})
	if n, err := p.Pending(context.Background()); err != nil || n != 0 {
		t.Fatalf("Pending = %d, %v", n, err)
	}
	if got := provenance.FormatQueue(nil); got != "empty" {
		t.Errorf("FormatQueue(nil) = %q", got)
	}
	if got := provenance.FormatQueue(map[string]int{"draft": 2, "track": 1}); got != "track=1 draft=2" {
		t.Errorf("FormatQueue = %q", got)
	}
}

// TestSettle_StopsWhenTheContextEnds: a context cancelled after the lease was taken ends Settle with its error, the
// lease is released and the queue is intact.
func TestSettle_StopsWhenTheContextEnds(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	w := newWorld(t, func(o *provenance.Options) { o.BeforeSettle = func(context.Context) { cancel() } })
	w.sc.Draft("a_v1", librarytest.DraftOpts{})
	if _, err := w.prov.Settle(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Settle = %v, want context.Canceled", err)
	}
	if held, _ := w.prov.LeaseHeld(context.Background()); held {
		t.Errorf("the lease must be released")
	}
	if p, _ := w.prov.Pending(context.Background()); p != 1 {
		t.Errorf("queue = %d, want the intact 1", p)
	}
}
