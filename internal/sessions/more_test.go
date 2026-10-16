package sessions_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/sessions"
)

func TestSessions_CollectionKeyAndNames(t *testing.T) {
	t.Parallel()
	if got := sessions.CollectionKey("Synthetic Collection"); got != "collection:Synthetic Collection" {
		t.Errorf("short key = %q", got)
	}
	long := strings.Repeat("c", 400)
	a, b := sessions.CollectionKey(long), sessions.CollectionKey(long+"x")
	if len(a) > 300 || a == b || !strings.HasPrefix(a, "collection:") || !strings.Contains(a, "#") {
		t.Errorf("long keys: %q / %q", a, b)
	}
}

func TestSessions_LongCollectionNameIsCutAndSurvivesSync(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	long := strings.Repeat("é", 200)
	d := e.sc.Draft("long_v1", librarytest.DraftOpts{})
	e.sc.Collection(d, long)
	e.tx(func(tx *sql.Tx) (sessions.Stats, error) {
		return sessions.SyncCollectionsTx(context.Background(), tx, e.env(), []string{long, "no such collection"})
	})
	if n := e.str(`SELECT name FROM sessions WHERE kind IN ('import','upload')`); len([]rune(n)) != 120 {
		t.Errorf("name has %d runes, want 120", len([]rune(n)))
	}
}

func TestSessions_EnsureTxRules(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	run := func(k sessions.Key) (string, bool, error) {
		tx, err := e.st.W.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		id, created, err := sessions.EnsureTx(ctx, tx, e.env(), k)
		if err == nil {
			err = tx.Commit()
		}
		return id, created, err
	}
	if _, _, err := run(sessions.Key{Kind: sessions.KindBatch, Name: "x", StartedAt: e.sc.At(0)}); err == nil {
		t.Error("an empty source key was accepted")
	}
	if _, _, err := run(sessions.Key{Kind: sessions.KindBatch, SourceKey: "k1", Name: "x", StartedAt: "not a time"}); err == nil {
		t.Error("a bad start was accepted")
	}
	end := "also not a time"
	if _, _, err := run(sessions.Key{Kind: sessions.KindBatch, SourceKey: "k2", Name: "x", StartedAt: e.sc.At(0), EndedAt: &end}); err == nil {
		t.Error("a bad end was accepted")
	}
	// an end before the start is lifted to the start, on create and on refresh
	before := e.sc.At(0)
	id, created, err := run(sessions.Key{Kind: sessions.KindBatch, SourceKey: "k3", Name: "x", StartedAt: e.sc.At(5), EndedAt: &before})
	if err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	if got := e.str(`SELECT started_at = ended_at FROM sessions WHERE id = ?`, id); got != "1" {
		t.Errorf("ended_at was not lifted to started_at")
	}
	id2, created, err := run(sessions.Key{Kind: sessions.KindBatch, SourceKey: "k3", Name: "x", StartedAt: e.sc.At(6), EndedAt: &before})
	if err != nil || created || id2 != id {
		t.Fatalf("refresh: %v %v %v", id2, created, err)
	}
}

func TestSessions_ListPagingAndFilters(t *testing.T) {
	t.Parallel()
	w, _ := scene(t)
	ctx := context.Background()
	for i := range 5 {
		w.Sc.Run("completed", 20+i)
	}
	w.Settle()
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		pg, err := w.Sessions.List(ctx, sessions.ListFilter{Limit: 2, Cursor: cursor, IncludeHidden: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range pg.Items {
			if seen[s.ID] {
				t.Fatalf("session %s on two pages", s.ID)
			}
			seen[s.ID] = true
		}
		if pg.NextCursor == nil {
			break
		}
		if pages > 10 {
			t.Fatal("paging does not end")
		}
		cursor = *pg.NextCursor
	}
	if len(seen) != 7 {
		t.Errorf("paged %d sessions, want 7", len(seen))
	}
	if pg, err := w.Sessions.List(ctx, sessions.ListFilter{Limit: 5000}); err != nil || len(pg.Items) != 7 {
		t.Errorf("clamped list: %d %v", len(pg.Items), err)
	}
	if pg, err := w.Sessions.List(ctx, sessions.ListFilter{Kinds: []string{"upload"}, Q: "Synthetic_%"}); err != nil || len(pg.Items) != 0 {
		t.Errorf("filtered list: %d %v", len(pg.Items), err)
	}
	if _, err := w.Sessions.List(ctx, sessions.ListFilter{Cursor: "!!"}); err == nil {
		t.Error("a malformed cursor was accepted")
	}
	if _, err := w.Sessions.Summary(ctx, "missing"); err == nil {
		t.Error("Summary of a missing session succeeded")
	}
}

func TestSessions_UpdateUnhidesAndRefuses(t *testing.T) {
	t.Parallel()
	w, run := scene(t)
	ctx := context.Background()
	id := w.Str(`SELECT id FROM sessions WHERE source_key = ?`, "import:"+run)
	s, err := w.Sessions.Summary(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	hid, err := w.Sessions.Update(ctx, humanP, id, sessions.Patch{Revision: s.Revision, Hidden: bp(true)})
	if err != nil || hid.HiddenAt == nil {
		t.Fatalf("hide: %+v %v", hid, err)
	}
	shown, err := w.Sessions.Update(ctx, humanP, id, sessions.Patch{Revision: hid.Revision, Hidden: bp(false)})
	if err != nil || shown.HiddenAt != nil {
		t.Fatalf("unhide: %+v %v", shown, err)
	}
	if _, err := w.Sessions.Update(ctx, humanP, "missing", sessions.Patch{Revision: 1, Hidden: bp(true)}); err == nil {
		t.Error("update of a missing session succeeded")
	}
	if c, st, _ := pcode(func() error {
		_, err := w.Sessions.Update(ctx, humanP, id, sessions.Patch{Revision: 1, Hidden: bp(true)})
		return err
	}()); c != "REVISION_CONFLICT" || st != 409 {
		t.Errorf("stale revision = %s %d", c, st)
	}
}
