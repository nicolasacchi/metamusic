package muses_test

import (
	"context"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/store/faultdb"
)

type faultIDs struct {
	ref, brief, hiddenRef string
	revRef, revBrief      int
	revHidden             int
	songs                 []string
}

// faultTemplate builds a library with a reference Muse, a brief, a hidden reference and settled Songs, one of them
// already assigned.
func faultTemplate(t *testing.T) (string, faultIDs) {
	t.Helper()
	w := newWorld(t)
	var ids faultIDs
	ref := w.create(muses.KindReference, "Synthetic Muse 500", ptr("Synthetic Artist 500"), nil)
	brief := w.create(muses.KindBrief, "Synthetic Brief 500", nil, ptr("a warm synthetic idea"))
	w.create(muses.KindReference, "Synthetic Muse 503", ptr("Synthetic Artist 500"), nil)
	hr := w.create(muses.KindReference, "Synthetic Muse 501", nil, nil)
	hidden := true
	hr, err := w.svc.Update(context.Background(), human, hr.ID, muses.Patch{Revision: hr.Revision, Hidden: &hidden})
	if err != nil {
		t.Fatal(err)
	}
	ids.ref, ids.brief, ids.hiddenRef = ref.ID, brief.ID, hr.ID
	ids.revRef, ids.revBrief, ids.revHidden = ref.Revision, brief.Revision, hr.Revision
	ids.songs = w.songs(3)
	if _, err := w.svc.AssignSongs(context.Background(), human, ref.ID, ids.songs[:1]); err != nil {
		t.Fatal(err)
	}
	return faultdb.Snapshot(t, w.st, w.path), ids
}

func TestFault_MuseOperations(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("fault sweep: make test runs it")
	}
	tpl, ids := faultTemplate(t)
	svc := func(st *store.Store) *muses.Service {
		n := 0
		return muses.New(st, func() time.Time { return librarytest.FixedNow }, func() string { n++; return idOf(800000 + n) })
	}
	f := func(s bool) *bool { return &s }
	ops := map[string]func(ctx context.Context, st *store.Store) error{
		"Create a reference": func(ctx context.Context, st *store.Store) error {
			_, err := svc(st).Create(ctx, human, muses.NewMuse{Kind: muses.KindReference, Title: "Synthetic Muse 600", Artist: ptr("Synthetic Artist 600")})
			return err
		},
		"Create a brief": func(ctx context.Context, st *store.Store) error {
			_, err := svc(st).Create(ctx, agent, muses.NewMuse{Kind: muses.KindBrief, Title: "Synthetic Brief 600", Brief: ptr("x")})
			return err
		},
		"Create a duplicate": func(ctx context.Context, st *store.Store) error {
			_, err := svc(st).Create(ctx, human, muses.NewMuse{Kind: muses.KindReference, Title: "Synthetic Muse 500", Artist: ptr("Synthetic Artist 500")})
			return expectExists(err)
		},
		"Update a reference": func(ctx context.Context, st *store.Store) error {
			_, err := svc(st).Update(ctx, human, ids.ref, muses.Patch{Revision: ids.revRef, Title: ptr("Synthetic Muse 502"), Artist: ptr("Synthetic Artist 502")})
			return err
		},
		"Update into an existing key": func(ctx context.Context, st *store.Store) error {
			_, err := svc(st).Update(ctx, human, ids.ref, muses.Patch{Revision: ids.revRef, Title: ptr("Synthetic Muse 503")})
			return expectExists(err)
		},
		"Update a brief and hide it": func(ctx context.Context, st *store.Store) error {
			_, err := svc(st).Update(ctx, human, ids.brief, muses.Patch{Revision: ids.revBrief, Brief: ptr("another idea"), Hidden: f(true)})
			return err
		},
		"Unhide": func(ctx context.Context, st *store.Store) error {
			_, err := svc(st).Update(ctx, human, ids.hiddenRef, muses.Patch{Revision: ids.revHidden, Hidden: f(false)})
			return err
		},
		"Assign Songs": func(ctx context.Context, st *store.Store) error {
			_, err := svc(st).AssignSongs(ctx, human, ids.ref, ids.songs)
			return err
		},
		"Get": func(ctx context.Context, st *store.Store) error {
			_, err := svc(st).Get(ctx, ids.ref)
			return err
		},
		"List": func(ctx context.Context, st *store.Store) error {
			if _, err := svc(st).List(ctx, muses.ListFilter{IncludeHidden: true, Limit: 2}); err != nil {
				return err
			}
			_, err := svc(st).List(ctx, muses.ListFilter{Q: "Synthetic", Kind: muses.KindReference})
			return err
		},
		"Ref and Refs": func(ctx context.Context, st *store.Store) error {
			if _, _, err := svc(st).Ref(ctx, ids.ref); err != nil {
				return err
			}
			_, err := svc(st).Refs(ctx, []string{ids.ref, ids.brief, "missing"})
			return err
		},
		"VerifyMuses": func(ctx context.Context, st *store.Store) error {
			_, err := muses.VerifyMuses(ctx, st.R)
			return err
		},
		"EnsureReferenceTx": func(ctx context.Context, st *store.Store) error {
			tx, err := st.W.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			now := func() string { return idOf(810000) }
			if _, _, err := muses.EnsureReferenceTx(ctx, tx, librarytest.FixedNow, now, "Synthetic Muse 700", nil, "system:test"); err != nil {
				return err
			}
			if _, _, err := muses.EnsureReferenceTx(ctx, tx, librarytest.FixedNow, now, "Synthetic Muse 500", ptr("Synthetic Artist 500"), "system:test"); err != nil {
				return err
			}
			return tx.Commit()
		},
		"CheckAssignableTx": func(ctx context.Context, st *store.Store) error {
			tx, err := st.W.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			return muses.CheckAssignableTx(ctx, tx, ids.ref)
		},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			faultdb.Sweep(t, tpl, op)
		})
	}
}

// expectExists turns the refusal the operation is meant to hit (MUSE_EXISTS) into success, so the unharmed run passes
// and only an injected database failure is an error.
func expectExists(err error) error {
	if c, _ := code(err); c == "MUSE_EXISTS" {
		return nil
	}
	return err
}
