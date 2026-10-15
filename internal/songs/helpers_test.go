package songs_test

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// world is one scene library with the Reconciler over it.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// Count returns how many records carry msg.
func (l *logBuf) Count(msg string) int { return strings.Count(l.String(), `"msg":"`+msg+`"`) }

type world struct {
	log  *logBuf
	t    *testing.T
	st   *store.Store
	sc   *librarytest.Scene
	prov *provenance.Service
}

func newWorld(t *testing.T) *world {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	n := 0
	clock := librarytest.FixedNow
	w := &world{t: t, st: st, sc: librarytest.NewScene(t, st, clock), log: &logBuf{}}
	w.prov = provenance.New(provenance.Options{Store: st, Now: func() time.Time { return clock }, NewID: func() string {
		n++
		return idOf(n)
	}, Log: slog.New(slog.NewJSONHandler(w.log, nil))})
	return w
}

func (w *world) settle() provenance.Report {
	w.t.Helper()
	rep, err := w.prov.Settle(context.Background())
	if err != nil {
		w.t.Fatalf("settle: %v", err)
	}
	return rep
}

func (w *world) verify() {
	w.t.Helper()
	an, err := w.prov.Verify(context.Background())
	if err != nil {
		w.t.Fatal(err)
	}
	if len(an) != 0 {
		w.t.Errorf("Verify: %+v", an)
	}
}

func (w *world) count(q string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		w.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func (w *world) str(q string, args ...any) string {
	w.t.Helper()
	var s sql.NullString
	if err := w.st.R.QueryRow(q, args...).Scan(&s); err != nil && err != sql.ErrNoRows {
		w.t.Fatalf("%s: %v", q, err)
	}
	return s.String
}

// liveSong returns the id of the live Song of a draft.
func (w *world) liveSongOfDraft(d string) string {
	return w.str(`SELECT id FROM songs WHERE draft_id = ? AND retired_at IS NULL`, d)
}

// songOfTrack returns the Song of a take through the take mapping.
func (w *world) songOfTrack(tr string) string {
	return w.str(`SELECT song_id FROM take_songs WHERE track_id = ?`, tr)
}

func (w *world) liveSongs() int {
	return w.count(`SELECT COUNT(*) FROM songs WHERE retired_at IS NULL`)
}

func idOf(n int) string { return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xe00000+n) }
