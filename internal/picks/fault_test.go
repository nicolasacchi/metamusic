package picks_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/picks"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/store/faultdb"
)

// faultTemplate builds a library with Songs of every shape the Pick operations touch: a Song with three takes, stars,
// three counted verdicts (so the ladder rule applies), a pinned Pick, open proposals, plus a one-take Song, a retired
// Song, and fresh source rows that leave the settle queue non-empty. It returns the template and the ids.
type faultIDs struct {
	song, other, retired string
	takes, otherTakes    []string
	open, extra          string // two open proposals on song
	lad                  *fakeLadder
}

func faultTemplate(t *testing.T) (string, faultIDs) {
	t.Helper()
	w := newWorld(t)
	var ids faultIDs
	ids.lad = w.lad
	ids.song, ids.takes = w.song(3)
	ids.other, ids.otherTakes = w.song(1)
	ids.retired = w.retiredSong(ids.song)
	w.sc.Rate(ids.takes[0], 4)
	w.sc.Rate(ids.takes[1], 5)
	w.verdicts(ids.takes[0], ids.takes[1], 3)
	w.lad.set(ids.takes[0], 1.2, 0.4)
	w.lad.set(ids.takes[1], 0.1, 0.5)
	w.lad.set(ids.takes[2], -0.5, 0.5)
	w.settle()
	ctx := context.Background()
	p1, err := w.svc.Propose(ctx, agentA, ids.song, ids.takes[2], "third is warmer")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := w.svc.Propose(ctx, agentB, ids.song, ids.takes[1], "second")
	if err != nil {
		t.Fatal(err)
	}
	ids.open, ids.extra = p1.ID, p2.ID
	if _, err := w.svc.Pin(ctx, human, ids.other, &ids.otherTakes[0], nil); err != nil {
		t.Fatal(err)
	}
	// a second wave leaves keys in the queue and a Song whose verdicts moved
	w.sc.Rate(ids.takes[2], 5)
	w.sc.Draft("fault-new", librarytest.DraftOpts{})
	return faultdb.Snapshot(t, w.st, w.path), ids
}

// faultSvc builds the Pick service and a Reconciler over the fault-injecting store.
func faultSvc(st *store.Store, ids faultIDs) (*picks.Service, *provenance.Service) {
	var n atomic.Int64
	now := func() time.Time { return librarytest.FixedNow }
	newID := func() string { return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xf00000+n.Add(1)) }
	svc := picks.New(st, ids.lad, now, newID)
	return svc, provenance.New(provenance.Options{Store: st, Picks: svc, Ladder: ids.lad, Now: now, NewID: newID})
}

func sweepPicks(t *testing.T, tpl string, ids faultIDs, op func(ctx context.Context, s *picks.Service, p *provenance.Service) error) {
	t.Helper()
	n := faultdb.Sweep(t, tpl, func(ctx context.Context, st *store.Store) error {
		s, p := faultSvc(st, ids)
		return op(ctx, s, p)
	})
	if n == 0 {
		t.Error("no injected failure surfaced as an error")
	}
}

func TestFault_PickOperations(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("fault sweep: make test runs it")
	}
	tpl, ids := faultTemplate(t)
	ops := map[string]func(ctx context.Context, s *picks.Service, p *provenance.Service) error{
		"Pin a take": func(ctx context.Context, s *picks.Service, _ *provenance.Service) error {
			_, err := s.Pin(ctx, human, ids.song, &ids.takes[2], nil)
			return err
		},
		"Pin accepting a proposal": func(ctx context.Context, s *picks.Service, _ *provenance.Service) error {
			_, err := s.Pin(ctx, human, ids.song, &ids.takes[2], &ids.open)
			return err
		},
		"Unpin": func(ctx context.Context, s *picks.Service, _ *provenance.Service) error {
			_, err := s.Pin(ctx, human, ids.other, nil, nil)
			return err
		},
		"Propose": func(ctx context.Context, s *picks.Service, _ *provenance.Service) error {
			_, err := s.Propose(ctx, agentA, ids.song, ids.takes[1], "again")
			return err
		},
		"Propose over the open ones": func(ctx context.Context, s *picks.Service, _ *provenance.Service) error {
			if _, err := s.Propose(ctx, agentA, ids.song, ids.takes[1], "replace mine"); err != nil {
				return err
			}
			_, err := s.Propose(ctx, agentB, ids.song, ids.takes[2], "replace theirs")
			return err
		},
		"Dismiss": func(ctx context.Context, s *picks.Service, _ *provenance.Service) error {
			_, err := s.Dismiss(ctx, human, ids.open)
			return err
		},
		"Events": func(ctx context.Context, s *picks.Service, _ *provenance.Service) error {
			if _, err := s.Events(ctx, picks.EventFilter{SongID: ids.song}); err != nil {
				return err
			}
			_, err := s.Events(ctx, picks.EventFilter{Limit: 2})
			return err
		},
		"Get": func(ctx context.Context, s *picks.Service, _ *provenance.Service) error {
			if _, err := s.Get(ctx, ids.song); err != nil {
				return err
			}
			_, err := s.Get(ctx, ids.other)
			return err
		},
		"Sweep": func(ctx context.Context, s *picks.Service, _ *provenance.Service) error {
			_, err := s.Sweep(ctx)
			return err
		},
		"Settle recomputes": func(ctx context.Context, _ *picks.Service, p *provenance.Service) error {
			_, err := p.Settle(ctx)
			return err
		},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sweepPicks(t, tpl, ids, op)
		})
	}
}

// TestPicks_RefusalsOnTheTemplate checks the 4xx refusals a sweep never reaches: a retired Song and a take outside it.
func TestPicks_RefusalsOnTheTemplate(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := context.Background()
	song, tk := w.song(2)
	retired := w.retiredSong(song)
	_, err := w.svc.Pin(ctx, human, retired, &tk[0], nil)
	wantProblem(t, err, 409, "SONG_RETIRED")
	_, err = w.svc.Propose(ctx, agentA, retired, tk[0], "x")
	wantProblem(t, err, 409, "SONG_RETIRED")
	other, otk := w.song(1)
	_ = other
	_, err = w.svc.Pin(ctx, human, song, &otk[0], nil)
	wantProblem(t, err, 422, "TAKE_NOT_IN_SONG")
	if _, err := w.svc.Get(ctx, "no-such-song"); err == nil {
		t.Error("Get of an unknown Song succeeded")
	}
}

// TestPicks_UnconfiguredServiceIsUnsupported: a Service built without a store refuses every operation with
// errors.ErrUnsupported (the shape the structural-core tests of provenance use).
func TestPicks_UnconfiguredServiceIsUnsupported(t *testing.T) {
	t.Parallel()
	s := picks.New(nil, nil, time.Now, func() string { return "id" })
	ctx := context.Background()
	take := "t"
	checks := map[string]error{}
	_, checks["Events"] = s.Events(ctx, picks.EventFilter{})
	_, checks["Pin"] = s.Pin(ctx, human, "s", &take, nil)
	_, checks["Propose"] = s.Propose(ctx, agentA, "s", "t", "r")
	_, checks["Dismiss"] = s.Dismiss(ctx, human, "p")
	_, checks["Sweep"] = s.Sweep(ctx)
	_, checks["Get"] = s.Get(ctx, "s")
	for name, err := range checks {
		if !errors.Is(err, errors.ErrUnsupported) {
			t.Errorf("%s = %v, want ErrUnsupported", name, err)
		}
	}
}

// retiredSong adds a draft Song with no takes and retires it into into (a Song with takes cannot retire).
func (w *world) retiredSong(into string) string {
	w.t.Helper()
	d := w.sc.Draft(fmt.Sprintf("retire-%d", w.n.Add(1)), librarytest.DraftOpts{})
	w.settle()
	id := w.str(`SELECT id FROM songs WHERE draft_id = ?`, d)
	w.exec(`UPDATE songs SET retired_at = ?, retired_into = ? WHERE id = ?`, "2026-03-01T10:00:00.000Z", into, id)
	return id
}
