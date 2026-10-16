package sessions_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/sessions"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/store/faultdb"
)

func TestFault_SessionOperations(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("fault sweep: make test runs it")
	}
	w, run := scene(t)
	sc := w.Sc
	d := sc.Draft("syn_job_v1", librarytest.DraftOpts{})
	batch := sc.Batch(`[{"item_index":0}]`, 1, "finished")
	sc.Job(batch, 0, d, "", sc.Recipe("syn_job_r", "pop"))
	run2 := sc.Run("running", 30) // a second wave: not synced yet
	w.Settle()
	id := w.Str(`SELECT id FROM sessions WHERE source_key = ?`, "import:"+run)
	rev := w.Count(`SELECT revision FROM sessions WHERE id = ?`, id)
	tpl := faultdb.Snapshot(t, w.St, w.Path)

	env := func() store.Env {
		n := 0
		return store.Env{Now: librarytest.FixedNow, Actor: "system:provenance", NewID: func() string {
			n++
			return idOfFault(n)
		}}
	}
	inTx := func(fn func(ctx context.Context, tx *sql.Tx) error) func(ctx context.Context, st *store.Store) error {
		return func(ctx context.Context, st *store.Store) error {
			tx, err := st.W.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			if err := fn(ctx, tx); err != nil {
				return err
			}
			return tx.Commit()
		}
	}
	svc := func(st *store.Store) *sessions.Service {
		return sessions.New(st, func() time.Time { return librarytest.FixedNow })
	}
	ops := map[string]func(ctx context.Context, st *store.Store) error{
		"SyncRunsTx": inTx(func(ctx context.Context, tx *sql.Tx) error {
			_, err := sessions.SyncRunsTx(ctx, tx, env(), []string{run, run2, "no-such-run"})
			return err
		}),
		"SyncCollectionsTx": inTx(func(ctx context.Context, tx *sql.Tx) error {
			_, err := sessions.SyncCollectionsTx(ctx, tx, env(), []string{"Synthetic Collection", "no-such-collection"})
			return err
		}),
		"SyncBatchesTx": inTx(func(ctx context.Context, tx *sql.Tx) error {
			_, err := sessions.SyncBatchesTx(ctx, tx, env(), []string{batch, "no-such-batch"})
			return err
		}),
		"EnsureTx and AddMembersTx": inTx(func(ctx context.Context, tx *sql.Tx) error {
			sid, _, err := sessions.EnsureTx(ctx, tx, env(), sessions.Key{Kind: sessions.KindBatch, SourceKey: "batch:fault", Name: "n", StartedAt: sc.At(0)})
			if err != nil {
				return err
			}
			if _, _, err := sessions.EnsureTx(ctx, tx, env(), sessions.Key{Kind: sessions.KindBatch, SourceKey: "batch:fault", Name: "n", StartedAt: sc.At(2)}); err != nil {
				return err
			}
			_, err = sessions.AddMembersTx(ctx, tx, env(), sid, sessions.MemberDraft, []string{d})
			return err
		}),
		"List": func(ctx context.Context, st *store.Store) error {
			if _, err := svc(st).List(ctx, sessions.ListFilter{Limit: 1, IncludeHidden: true}); err != nil {
				return err
			}
			_, err := svc(st).List(ctx, sessions.ListFilter{Kinds: []string{"import"}, Q: "Import"})
			return err
		},
		"Summary": func(ctx context.Context, st *store.Store) error {
			_, err := svc(st).Summary(ctx, id)
			return err
		},
		"Update": func(ctx context.Context, st *store.Store) error {
			_, err := svc(st).Update(ctx, humanP, id, sessions.Patch{Revision: rev, Name: sp("Renamed synthetic"), Hidden: bp(true)})
			return err
		},
		"Update unhide": func(ctx context.Context, st *store.Store) error {
			_, err := svc(st).Update(ctx, humanP, id, sessions.Patch{Revision: rev, Hidden: bp(false)})
			return err
		},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			faultdb.Sweep(t, tpl, op)
		})
	}
}

func idOfFault(n int) string {
	return "01900000-0000-7000-8000-" + hex12(0xf10000+n)
}

func hex12(n int) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 12)
	for i := 11; i >= 0; i-- {
		out[i] = digits[n&0xf]
		n >>= 4
	}
	return string(out)
}
