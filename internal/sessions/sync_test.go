package sessions_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/sessions"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type env struct {
	t  *testing.T
	st *store.Store
	sc *librarytest.Scene
	n  int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &env{t: t, st: st, sc: librarytest.NewScene(t, st, librarytest.FixedNow)}
}

func (e *env) env() store.Env {
	return store.Env{Now: librarytest.FixedNow, Actor: "system:provenance", NewID: func() string { e.n++; return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xf00000+e.n) }}
}

func (e *env) tx(fn func(tx *sql.Tx) (sessions.Stats, error)) sessions.Stats {
	e.t.Helper()
	tx, err := e.st.W.BeginTx(context.Background(), nil)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	st, err := fn(tx)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		e.t.Fatal(err)
	}
	return st
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func (e *env) str(q string, args ...any) string {
	e.t.Helper()
	var s sql.NullString
	if err := e.st.R.QueryRow(q, args...).Scan(&s); err != nil && err != sql.ErrNoRows {
		e.t.Fatalf("%s: %v", q, err)
	}
	return s.String
}

func TestSessions_SyncRun(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	sc := e.sc
	run := sc.Run("completed", 0)
	r := sc.Recipe("a_v1", "pop")
	sc.Source(r, run, 1)
	d := sc.Draft("a_v1", librarytest.DraftOpts{Recipe: r})
	tr := sc.RootInRun(sc.Recipe("t_v1", "pop"), "T", run, sc.At(0))
	// a second run that mentions the same recipe later: the draft belongs to the FIRST occurrence only
	run2 := sc.Run("completed", 10)
	sc.Source(r, run2, 5)
	st := e.tx(func(tx *sql.Tx) (sessions.Stats, error) {
		return sessions.SyncRunsTx(context.Background(), tx, e.env(), []string{run, run2})
	})
	if st.Created != 2 {
		t.Errorf("created = %d, want 2", st.Created)
	}
	if got := e.str(`SELECT kind||'|'||started_at||'|'||COALESCE(ended_at,'-') FROM sessions WHERE source_key = ?`, "import:"+run); got == "" || got[:6] != "import" {
		t.Errorf("run session = %q", got)
	}
	var sid string
	if err := e.st.R.QueryRow(`SELECT id FROM sessions WHERE source_key = ?`, "import:"+run).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT COUNT(*) FROM session_members WHERE session_id = ? AND member_kind='track' AND member_id = ?`, sid, tr) != 1 ||
		e.count(`SELECT COUNT(*) FROM session_members WHERE session_id = ? AND member_kind='draft' AND member_id = ?`, sid, d) != 1 {
		t.Errorf("members of the first run: track and draft expected")
	}
	if e.count(`SELECT COUNT(*) FROM session_members m JOIN sessions s ON s.id = m.session_id WHERE s.source_key = ? AND m.member_kind = 'draft'`, "import:"+run2) != 0 {
		t.Errorf("a draft belongs to the run of its first recipe_sources occurrence only")
	}
	// idempotence: a second sync adds nothing
	st = e.tx(func(tx *sql.Tx) (sessions.Stats, error) {
		return sessions.SyncRunsTx(context.Background(), tx, e.env(), []string{run, run2})
	})
	if st.Created != 0 || st.MembersAdded != 0 {
		t.Errorf("second sync: %+v", st)
	}
	// a running run has no end
	running := sc.Run("running", 20)
	e.tx(func(tx *sql.Tx) (sessions.Stats, error) {
		return sessions.SyncRunsTx(context.Background(), tx, e.env(), []string{running})
	})
	if got := e.str(`SELECT COALESCE(ended_at,'NULL') FROM sessions WHERE source_key = ?`, "import:"+running); got != "NULL" {
		t.Errorf("a running run's session ended_at = %q", got)
	}
}

func TestSessions_TimestampsNormalised(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	run := e.sc.Run("completed", 0) // At(0) is the 20-character form
	e.tx(func(tx *sql.Tx) (sessions.Stats, error) {
		return sessions.SyncRunsTx(context.Background(), tx, e.env(), []string{run})
	})
	var started, ended string
	if err := e.st.R.QueryRow(`SELECT started_at, ended_at FROM sessions WHERE source_key = ?`, "import:"+run).Scan(&started, &ended); err != nil {
		t.Fatal(err)
	}
	if len(started) != 24 || len(ended) != 24 {
		t.Errorf("stored widths %d/%d, want 24 (20-character sources are normalised)", len(started), len(ended))
	}
}

func TestSessions_SyncCollection_Kind(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	sc := e.sc
	// an upload collection: every draft is jsonl_upload
	u1 := sc.Draft("u1", librarytest.DraftOpts{Origin: "jsonl_upload", CreatedAt: sc.At(2)})
	u2 := sc.Draft("u2", librarytest.DraftOpts{Origin: "jsonl_upload", CreatedAt: sc.At(5)})
	sc.Collection(u1, "Synthetic Upload Set")
	sc.Collection(u2, "Synthetic Upload Set")
	e.tx(func(tx *sql.Tx) (sessions.Stats, error) {
		return sessions.SyncCollectionsTx(context.Background(), tx, e.env(), []string{"Synthetic Upload Set"})
	})
	if got := e.str(`SELECT kind FROM sessions WHERE source_key = 'collection:Synthetic Upload Set'`); got != "upload" {
		t.Errorf("kind = %q, want upload", got)
	}
	// started/ended are the min/max draft created_at as instants (At(2) is 20 characters, At(5) is 24)
	var started, ended string
	if err := e.st.R.QueryRow(`SELECT started_at, ended_at FROM sessions WHERE source_key = 'collection:Synthetic Upload Set'`).Scan(&started, &ended); err != nil {
		t.Fatal(err)
	}
	if started >= ended || len(started) != 24 || len(ended) != 24 {
		t.Errorf("started %q ended %q", started, ended)
	}
	// the kind is frozen at the first sync: an import draft joining later does not change it
	imp := sc.Draft("i1", librarytest.DraftOpts{})
	sc.Collection(imp, "Synthetic Upload Set")
	st := e.tx(func(tx *sql.Tx) (sessions.Stats, error) {
		return sessions.SyncCollectionsTx(context.Background(), tx, e.env(), []string{"Synthetic Upload Set"})
	})
	if got := e.str(`SELECT kind FROM sessions WHERE source_key = 'collection:Synthetic Upload Set'`); got != "upload" {
		t.Errorf("kind changed to %q after a later sync", got)
	}
	if st.Created != 0 || st.MembersAdded != 1 {
		t.Errorf("later sync: %+v, want one new member", st)
	}
	// a mixed collection at first sync is an import
	m1 := sc.Draft("m1", librarytest.DraftOpts{Origin: "jsonl_upload"})
	m2 := sc.Draft("m2", librarytest.DraftOpts{})
	sc.Collection(m1, "Synthetic Mixed Set")
	sc.Collection(m2, "Synthetic Mixed Set")
	e.tx(func(tx *sql.Tx) (sessions.Stats, error) {
		return sessions.SyncCollectionsTx(context.Background(), tx, e.env(), []string{"Synthetic Mixed Set"})
	})
	if got := e.str(`SELECT kind FROM sessions WHERE source_key = 'collection:Synthetic Mixed Set'`); got != "import" {
		t.Errorf("mixed kind = %q, want import", got)
	}
}

func TestSessions_SyncBatch(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	sc := e.sc
	r := sc.Recipe("b_v1", "pop")
	d := sc.Draft("b_v1", librarytest.DraftOpts{Recipe: r})
	live := sc.Batch(`[{"item_index":0}]`, 1, "active")
	job := sc.Job(live, 0, d, "", r)
	tr := sc.Track(r, "Produced", "", sc.At(0))
	sc.JobProduced(job, tr)
	e.tx(func(tx *sql.Tx) (sessions.Stats, error) {
		return sessions.SyncBatchesTx(context.Background(), tx, e.env(), []string{live})
	})
	var sid, name, kind string
	var ended sql.NullString
	if err := e.st.R.QueryRow(`SELECT id, name, kind, ended_at FROM sessions WHERE source_key = ?`, "batch:"+live).Scan(&sid, &name, &kind, &ended); err != nil {
		t.Fatal(err)
	}
	if kind != "batch" || ended.Valid || name[:6] != "Batch " || name[len(name)-6:] != "1 take" {
		t.Errorf("batch session = %q %q ended=%v", kind, name, ended)
	}
	if e.count(`SELECT COUNT(*) FROM session_members WHERE session_id = ?`, sid) != 2 {
		t.Errorf("a batch session holds the job's draft and take")
	}
	// the batch finishes: ended_at appears, nothing else changes
	if _, err := e.st.W.Exec(`UPDATE batches SET state = 'finished', finished_at = ? WHERE id = ?`, store.FormatTime(librarytest.FixedNow.Add(3*3600e9)), live); err != nil {
		t.Fatal(err)
	}
	e.tx(func(tx *sql.Tx) (sessions.Stats, error) {
		return sessions.SyncBatchesTx(context.Background(), tx, e.env(), []string{live})
	})
	if e.str(`SELECT ended_at FROM sessions WHERE id = ?`, sid) == "" {
		t.Errorf("a finished batch's session must end")
	}
}

func TestSessions_NoEmptyBatchSessions(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	sc := e.sc
	r := sc.Recipe("n_v1", "pop")
	var ids []string
	for _, state := range []string{"awaiting_confirmation", "rejected", "expired"} {
		ids = append(ids, sc.Batch(`[{"item_index":0}]`, 1, state))
	}
	_ = r
	jobless := sc.Batch(`[{"item_index":0}]`, 1, "active") // active but no job
	ids = append(ids, jobless)
	e.tx(func(tx *sql.Tx) (sessions.Stats, error) {
		return sessions.SyncBatchesTx(context.Background(), tx, e.env(), ids)
	})
	if n := e.count(`SELECT COUNT(*) FROM sessions`); n != 0 {
		t.Errorf("%d sessions for batches that must create none", n)
	}
}

func TestSessions_RenameSurvivesSync(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	sc := e.sc
	run := sc.Run("completed", 0)
	e.tx(func(tx *sql.Tx) (sessions.Stats, error) {
		return sessions.SyncRunsTx(context.Background(), tx, e.env(), []string{run})
	})
	if _, err := e.st.W.Exec(`UPDATE sessions SET name = 'My import', hidden_at = ?, hidden_by = 'human:h', revision = 2 WHERE source_key = ?`, store.FormatTime(librarytest.FixedNow), "import:"+run); err != nil {
		t.Fatal(err)
	}
	r := sc.Recipe("a_v1", "pop")
	sc.Source(r, run, 1)
	sc.Draft("a_v1", librarytest.DraftOpts{Recipe: r}) // a new member appears while the session is hidden
	st := e.tx(func(tx *sql.Tx) (sessions.Stats, error) {
		return sessions.SyncRunsTx(context.Background(), tx, e.env(), []string{run})
	})
	if got := e.str(`SELECT name||'|'||revision FROM sessions WHERE source_key = ?`, "import:"+run); got != "My import|2" {
		t.Errorf("a rename did not survive a sync: %q", got)
	}
	if st.Created != 0 || st.MembersAdded != 1 {
		t.Errorf("a hidden session keeps collecting: %+v", st)
	}
}

func TestSessions_KindFrozenAtFirstSync(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	run := e.sc.Run("completed", 0)
	e.tx(func(tx *sql.Tx) (sessions.Stats, error) {
		return sessions.SyncRunsTx(context.Background(), tx, e.env(), []string{run})
	})
	if _, err := e.st.W.Exec(`UPDATE sessions SET kind = 'upload' WHERE source_key = ?`, "import:"+run); err == nil {
		t.Errorf("the kind column is frozen by trigger")
	}
}
