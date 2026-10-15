package muses_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var (
	human = auth.Principal{Kind: auth.KindHuman, ID: "h", Name: "h", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "proxy"}
	agent = auth.Principal{Kind: auth.KindAgent, ID: "a", Name: "a", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "token"}
)

type world struct {
	t    *testing.T
	st   *store.Store
	sc   *librarytest.Scene
	svc  *muses.Service
	prov *provenance.Service
	n    int
}

func idOf(n int) string { return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xe10000+n) }

func newWorld(t *testing.T) *world {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	clock := librarytest.FixedNow
	w := &world{t: t, st: st, sc: librarytest.NewScene(t, st, clock)}
	newID := func() string { w.n++; return idOf(w.n) }
	w.svc = muses.New(st, func() time.Time { return clock }, newID)
	w.prov = provenance.New(provenance.Options{Store: st, Now: func() time.Time { return clock }, NewID: newID})
	return w
}

// songs creates n unreferenced root Songs (one take each) and settles; it returns their Song ids.
func (w *world) songs(n int) []string {
	w.t.Helper()
	for i := 0; i < n; i++ {
		r := w.sc.Recipe(fmt.Sprintf("req_%d", i), "pop")
		w.sc.Track(r, fmt.Sprintf("Synthetic Title %d", i), "", w.sc.At(i))
	}
	if _, err := w.prov.Settle(context.Background()); err != nil {
		w.t.Fatal(err)
	}
	return w.col(`SELECT id FROM songs WHERE retired_at IS NULL ORDER BY created_at, id`)
}

func (w *world) col(q string, args ...any) []string {
	w.t.Helper()
	rows, err := w.st.R.Query(q, args...)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func (w *world) str(q string, args ...any) string {
	w.t.Helper()
	var s *string
	if err := w.st.R.QueryRow(q, args...).Scan(&s); err != nil {
		w.t.Fatal(err)
	}
	if s == nil {
		return ""
	}
	return *s
}

// code returns the problem code and status of err ("" / 0 when nil or not a problem).
func code(err error) (string, int) {
	var pe *problem.Error
	if errors.As(err, &pe) {
		return pe.Code, pe.Status
	}
	return "", 0
}

// retiredSong inserts a take-less Song retired into another and returns its id.
func (w *world) retiredSong(into string) string {
	w.t.Helper()
	d := w.sc.Draft("retired_req", librarytest.DraftOpts{})
	w.n++
	id := idOf(900000 + w.n)
	if _, err := w.st.W.Exec(`INSERT INTO songs(id,draft_id,title,origin,created_by,created_at,updated_by,updated_at) VALUES(?,?,'Retired','draft','system:test',?,'system:test',?)`,
		id, d, w.sc.At(0), w.sc.At(0)); err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.st.W.Exec(`UPDATE songs SET retired_at=?, retired_into=? WHERE id=?`, w.sc.At(1), into, id); err != nil {
		w.t.Fatal(err)
	}
	return id
}

// asProblem unwraps a *problem.Error.
func asProblem(err error) (*problem.Error, bool) {
	var pe *problem.Error
	ok := errors.As(err, &pe)
	return pe, ok
}
