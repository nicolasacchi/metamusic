package picks_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/ladder"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/picks"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var (
	human  = auth.Principal{Kind: auth.KindHuman, ID: "op", Name: "op", Via: "proxy"}
	device = auth.Principal{Kind: auth.KindDevice, ID: "phone", Name: "phone", Via: "device"}
	agentA = auth.Principal{Kind: auth.KindAgent, ID: "ag1", Name: "critic one", Via: "token"}
	agentB = auth.Principal{Kind: auth.KindAgent, ID: "ag2", Name: "critic two", Via: "token"}
)

// fakeLadder serves settable strengths; it is private to one world, so tests stay parallel safe.
type fakeLadder struct {
	mu sync.Mutex
	m  map[string]ladder.Strength
}

func (f *fakeLadder) set(id string, u, sd float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[id] = ladder.Strength{U: u, SD: sd}
}

func (f *fakeLadder) Strengths(_ context.Context, ids []string) (map[string]ladder.Strength, float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]ladder.Strength{}
	for _, id := range ids {
		if s, ok := f.m[id]; ok {
			out[id] = s
		}
	}
	return out, 0, nil
}

type world struct {
	t    *testing.T
	path string
	st   *store.Store
	sc   *librarytest.Scene
	lad  *fakeLadder
	svc  *picks.Service
	prov *provenance.Service
	n    atomic.Int64
	min  atomic.Int64 // minutes added to the clock by tick

	descending atomic.Bool // later ids sort before earlier ones (TestEvents_CursorMonotonic)
}

func withoutPicksStep(o *provenance.Options) { o.Picks = nil }

func newWorld(t *testing.T, mod ...func(*provenance.Options)) *world {
	t.Helper()
	path := filepath.Join(t.TempDir(), "w.db")
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	w := &world{t: t, path: path, st: st, sc: librarytest.NewScene(t, st, librarytest.FixedNow), lad: &fakeLadder{m: map[string]ladder.Strength{}}}
	now := func() time.Time { return librarytest.FixedNow.Add(time.Duration(w.min.Load()) * time.Minute) }
	newID := func() string {
		n := w.n.Add(1)
		if w.descending.Load() {
			return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xe7ffff-n)
		}
		return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xe00000+n)
	}
	w.svc = picks.New(st, w.lad, now, newID)
	o := provenance.Options{Store: st, Picks: w.svc, Ladder: w.lad, Now: now, NewID: newID}
	for _, m := range mod {
		m(&o)
	}
	w.prov = provenance.New(o)
	return w
}

// song builds a Song with n takes (the first is the root), recorded one minute apart, and settles it.
func (w *world) song(n int) (songID string, takes []string) {
	w.t.Helper()
	r := w.sc.Recipe(fmt.Sprintf("req-%d", w.n.Add(1)), "pop")
	root := w.sc.Track(r, "T", "", w.sc.At(0))
	takes = []string{root}
	for i := 1; i < n; i++ {
		takes = append(takes, w.sc.Track(r, "T", root, w.sc.At(i)))
	}
	w.settle()
	return w.str(`SELECT song_id FROM take_songs WHERE track_id = ?`, root), takes
}

func (w *world) settle() {
	w.t.Helper()
	if _, err := w.prov.Settle(context.Background()); err != nil {
		w.t.Fatalf("settle: %v", err)
	}
}

func (w *world) sweep() picks.Changes {
	w.t.Helper()
	ch, err := w.svc.Sweep(context.Background())
	if err != nil {
		w.t.Fatalf("sweep: %v", err)
	}
	return ch
}

func (w *world) str(q string, args ...any) string {
	w.t.Helper()
	var s *string
	if err := w.st.R.QueryRow(q, args...).Scan(&s); err != nil {
		w.t.Fatalf("%s: %v", q, err)
	}
	if s == nil {
		return ""
	}
	return *s
}

func (w *world) count(q string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		w.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func (w *world) exec(q string, args ...any) {
	w.t.Helper()
	if _, err := w.st.W.Exec(q, args...); err != nil {
		w.t.Fatalf("%s: %v", q, err)
	}
}

func (w *world) pick(song string) picks.PickView {
	w.t.Helper()
	v, err := w.svc.Get(context.Background(), song)
	if err != nil {
		w.t.Fatal(err)
	}
	return v
}

func (w *world) events(song string) []picks.PickEvent {
	w.t.Helper()
	p, err := w.svc.Events(context.Background(), picks.EventFilter{SongID: song})
	if err != nil {
		w.t.Fatal(err)
	}
	return p.Items
}

// verdicts records n blind human verdicts between a and b ("a" wins every time) and returns the duel ids.
func (w *world) verdicts(a, b string, n int) {
	w.t.Helper()
	for range n {
		w.sc.Verdict(w.sc.Duel(a, b), "a")
	}
}

func (w *world) agentVerdict(duel string) {
	w.t.Helper()
	w.exec(`INSERT INTO duel_verdicts(id,duel_id,source,principal_kind,principal_id,verdict,winner_track_id,saw_reveal,created_at)
		VALUES(?,?,'critic','agent','agent:ag1','a',(SELECT slot_a_track_id FROM duels WHERE id = ?),0,?)`, fmt.Sprintf("v-%d", w.n.Add(1)), duel, duel, w.sc.At(4))
}

func wantProblem(t *testing.T, err error, status int, code string) {
	t.Helper()
	var pe *problem.Error
	if !errors.As(err, &pe) {
		t.Fatalf("got %v (%T), want problem %d %s", err, err, status, code)
	}
	if pe.Status != status || pe.Code != code {
		t.Fatalf("got %d %s, want %d %s", pe.Status, pe.Code, status, code)
	}
}

func evidenceOf(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := jsonUnmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func (w *world) svc2Now() func() time.Time {
	return func() time.Time { return librarytest.FixedNow.Add(time.Duration(w.min.Load()) * time.Minute) }
}

func (w *world) envFor() store.Env {
	return store.Env{Now: w.svc2Now()(), NewID: func() string { return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xf80000+w.n.Add(1)) }, Actor: "system:provenance"}
}
