package desk_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/desk"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var epoch = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

var (
	alice = auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice", User: "alice", Role: auth.RoleOwner}
	bob   = auth.Principal{Kind: auth.KindHuman, ID: "bob", Name: "bob", User: "bob", Role: auth.RoleEditor}
	robot = auth.Principal{Kind: auth.KindAgent, ID: "tok1", Name: "robot"}
)

// aligned is the score of the fixture take lt.TrackLuna: one voice, three sections of two bars, 2 s per bar. Its sha256 is the
// one the scores row holds.
const aligned = "X:1\nT:t\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\n% verse\nCDEF|CDEF|\n% chorus\nCDEF|CDEF|\n% outro\nCDEF|CDEF|]\n"

type env struct {
	t    *testing.T
	st   *store.Store
	root *os.Root
	dir  string
	svc  *desk.Service
	now  atomic.Int64
	ids  atomic.Int64
	fx   lt.Fixtures
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "mm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	e := &env{t: t, st: st, root: root, dir: dir}
	e.now.Store(epoch.UnixMilli())
	e.fx = lt.FixtureM3(t, st, epoch)
	e.svc = desk.New(desk.Options{Store: st, Data: root,
		Now:   func() time.Time { return time.UnixMilli(e.now.Load()).UTC() },
		NewID: func() string { return "01900000-0000-7000-a000-" + pad12(e.ids.Add(1)) }})
	// a fresh data dir cleans up read-only plan dirs
	t.Cleanup(func() {
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error { _ = os.Chmod(p, 0o755); return nil })
	})
	return e
}

func pad12(n int64) string {
	s := "000000000000"
	d := []byte(s)
	for i := len(d) - 1; i >= 0 && n > 0; i-- {
		d[i] = byte('0' + n%10)
		n /= 10
	}
	return string(d)
}

func (e *env) advance(d time.Duration) { e.now.Add(d.Milliseconds()) }

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.st.W.Exec(q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *env) scalar(q string, args ...any) string {
	e.t.Helper()
	var v *string
	if err := e.st.R.QueryRow(q, args...).Scan(&v); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	if v == nil {
		return ""
	}
	return *v
}

// writeScore stores text at a data-relative path (the library writes takes/<id>/score.abc at ingest; the fixture does not).
func (e *env) writeScore(rel, text string) {
	e.t.Helper()
	if err := e.root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := e.root.WriteFile(rel, []byte(text), 0o444); err != nil {
		e.t.Fatal(err)
	}
}

// takeWithScore writes the file of the take's score row and returns the take id.
func (e *env) takeWithScore() string {
	e.t.Helper()
	id := lt.TrackLuna
	e.writeScore("takes/"+id+"/score.abc", aligned)
	return id
}

// plan makes the take plan of the fixture take.
func (e *env) plan() desk.Plan {
	e.t.Helper()
	pl, created, err := e.svc.FromTake(context.Background(), alice, e.takeWithScore())
	if err != nil || !created {
		e.t.Fatalf("FromTake: created %v err %v", created, err)
	}
	return pl
}

func lt256(s string) string { return lt.Sha(s) }

func itoa(n int) string { return strconv.Itoa(n) }
