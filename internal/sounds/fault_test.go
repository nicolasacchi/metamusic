package sounds_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/sounds"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/store/faultdb"
)

func sp(s string) *string { return &s }

func faultSounds(st *store.Store) (*sounds.Service, func() string) {
	var n atomic.Int64
	newID := func() string { return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xd00000+n.Add(1)) }
	var tick atomic.Int64
	now := func() time.Time { return librarytest.FixedNow.Add(time.Duration(tick.Add(1)) * time.Second) }
	return sounds.New(st, now, newID), newID
}

// faultLibrary: takes and drafts with styles that hit mapped, unmapped, plan and measured evidence; the second wave
// of source rows is left unsettled.
func faultLibrary(t *testing.T) string {
	t.Helper()
	w := newWorld(t, true)
	r1 := w.recipe("english, reggae, dub, 80 bpm, warm male vocal, wobble 3", "[Verse 1]\nla la")
	t1 := w.track(r1, trackOpts{})
	w.tempo(t1, 80)
	r2 := w.recipe("italian, 90s, dance pop, 125 bpm, female vocal, zzquux", "[Verse 1]\nla")
	t2 := w.track(r2, trackOpts{Declared: ip(125), AgeMin: 5})
	w.score(t2, 120)
	w.tempo(w.track(r2, trackOpts{Parent: t2, AgeMin: 2}), 0)
	w.draft("english, jazz, mellow, 90 bpm")
	w.draft("spanish, rock, 140bpm, aggressive")
	w.settle()
	s1 := w.songOfTrack(t1)
	w.pick(s1, t1)
	// second wave
	r3 := w.recipe("english, synthwave, 100 bpm, dreamy", "[Chorus]\nla")
	t3 := w.track(r3, trackOpts{AgeMin: 1})
	w.tempo(t3, 101)
	w.draft("french, chanson, slow")
	return faultdb.Snapshot(t, w.st, w.path)
}

func TestFault_SoundOperations(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("fault sweep: make test runs it")
	}
	tpl := faultLibrary(t)
	agent := agentP
	ops := map[string]func(ctx context.Context, st *store.Store) error{
		"Settle computes": func(ctx context.Context, st *store.Store) error {
			sv, newID := faultSounds(st)
			p := provenance.New(provenance.Options{Store: st, Sounds: sv, Now: func() time.Time { return librarytest.FixedNow }, NewID: newID})
			_, err := p.Settle(ctx)
			return err
		},
		"Map add": func(ctx context.Context, st *store.Store) error {
			sv, _ := faultSounds(st)
			_, err := sv.Map(ctx, agent, sounds.MapRequest{Mappings: []sounds.MapEntry{{Term: "wobble 3", Facet: "mood", Value: "dark", Op: "add"}, {Term: "zzquux", Facet: "vocal", Value: "female", Op: "add"}}, Note: sp("because")})
			return err
		},
		"Map remove": func(ctx context.Context, st *store.Store) error {
			sv, _ := faultSounds(st)
			if _, err := sv.Map(ctx, agent, sounds.MapRequest{Mappings: []sounds.MapEntry{{Term: "wobble 3", Facet: "mood", Value: "dark", Op: "add"}}}); err != nil {
				return err
			}
			_, err := sv.Map(ctx, agent, sounds.MapRequest{Mappings: []sounds.MapEntry{{Term: "wobble 3", Facet: "mood", Value: "dark", Op: "remove"}}})
			return err
		},
		"Taxonomy": func(ctx context.Context, st *store.Store) error {
			sv, _ := faultSounds(st)
			for _, q := range []sounds.TaxonomyQuery{{}, {Facet: "genre_family", Limit: 3}, {Q: "reggae"}, {Unmapped: true, Limit: 2}} {
				if _, err := sv.Taxonomy(ctx, q); err != nil {
					return err
				}
			}
			return nil
		},
		"Facets": func(ctx context.Context, st *store.Store) error {
			sv, _ := faultSounds(st)
			_, err := sv.Facets(ctx, songs.ListFilter{Sounds: map[string][]string{"genre_family": {"reggae", "jazz"}}})
			return err
		},
		"EnsureSeed and Index": func(ctx context.Context, st *store.Store) error {
			sv, _ := faultSounds(st)
			if err := sv.EnsureSeed(ctx); err != nil {
				return err
			}
			_, err := sv.Index(ctx)
			return err
		},
		"SweepStaleTx": func(ctx context.Context, st *store.Store) error {
			sv, newID := faultSounds(st)
			tx, err := st.W.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			if _, err := sv.SweepStaleTx(ctx, tx, sounds.Env{Now: librarytest.FixedNow, NewID: newID, Actor: "system:provenance"}, 50); err != nil {
				return err
			}
			return tx.Commit()
		},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			faultdb.Sweep(t, tpl, op)
		})
	}
}
