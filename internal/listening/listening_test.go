package listening_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/listening"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var (
	human = auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice"}
	agent = auth.Principal{Kind: auth.KindAgent, ID: "tok-1", Name: "bot"}
	dev   = auth.Principal{Kind: auth.KindDevice, ID: "dev-1", Name: "Phone"}
)

func newSvc(t *testing.T) (*listening.Service, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	lt.Fixture(t, st, lt.FixedNow)
	return listening.New(st, func() time.Time { return lt.FixedNow }), st
}

func id(n int) string { return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xa000+n) }

func i64(n int64) *int64 { return &n }

func ev(n int, kind string) listening.Event {
	return listening.Event{ID: id(n), TrackID: lt.TrackLuna, SessionID: "session-1", Kind: kind, ClientAt: lt.FixedNow.Add(-time.Minute)}
}

func TestListen_ValidationAndDedup(t *testing.T) {
	t.Parallel()
	s, _ := newSvc(t)
	ctx := context.Background()
	bad := func(n int, mod func(*listening.Event)) listening.Event {
		e := ev(n, "start")
		mod(&e)
		return e
	}
	evs := []listening.Event{
		ev(1, "start"),
		bad(2, func(e *listening.Event) { e.Kind = "rewind" }),
		bad(3, func(e *listening.Event) { e.TrackID = "nope" }),
		bad(4, func(e *listening.Event) { e.Kind = "segment"; e.FromMS, e.ToMS = i64(5000), i64(1000) }),
		bad(5, func(e *listening.Event) { e.Kind = "segment"; e.FromMS = i64(0) }), // needs both
		bad(6, func(e *listening.Event) { e.Kind = "seek"; e.FromMS, e.ToMS = i64(0), i64(12000+2001) }),
		bad(7, func(e *listening.Event) { e.ClientAt = lt.FixedNow.Add(10 * time.Minute) }),
		bad(8, func(e *listening.Event) { e.ClientAt = lt.FixedNow.Add(-31 * 24 * time.Hour) }),
		bad(9, func(e *listening.Event) { e.ID = "not-a-uuid" }),
		bad(10, func(e *listening.Event) { e.SessionID = "short" }),
		bad(11, func(e *listening.Event) { f := "wav"; e.Format = &f }),
		bad(12, func(e *listening.Event) { e.Kind = "segment"; e.FromMS, e.ToMS = i64(0), i64(12000+2000) }), // exactly at the bound
		ev(13, "complete"),
	}
	acc, dup, rej, err := s.Log(ctx, human, "web", evs)
	if err != nil {
		t.Fatal(err)
	}
	want := []listening.Rejected{
		{ID: id(2), Code: "bad_kind"}, {ID: id(3), Code: "unknown_track"}, {ID: id(4), Code: "bad_range"}, {ID: id(5), Code: "bad_range"},
		{ID: id(6), Code: "bad_range"}, {ID: id(7), Code: "bad_client_at"}, {ID: id(8), Code: "bad_client_at"}, {ID: "not-a-uuid", Code: "bad_id"},
		{ID: id(10), Code: "bad_session"}, {ID: id(11), Code: "bad_format"},
	}
	if diff := cmp.Diff(want, rej); diff != "" {
		t.Errorf("rejected (-want +got):\n%s", diff)
	}
	if acc != 3 || dup != 0 {
		t.Errorf("accepted %d duplicates %d, want 3 and 0", acc, dup)
	}
	acc, dup, rej, err = s.Log(ctx, human, "web", evs)
	if err != nil || acc != 0 || dup != 3 || len(rej) != len(want) {
		t.Errorf("re-post: %d %d %v %v", acc, dup, rej, err)
	}
	// a duplicate id inside one batch counts once
	acc, dup, _, _ = s.Log(ctx, human, "api", []listening.Event{ev(20, "start"), ev(20, "start")})
	if acc != 1 || dup != 1 {
		t.Errorf("in-batch duplicate: %d %d", acc, dup)
	}
	if _, _, _, err := s.Log(ctx, human, "carrier-pigeon", []listening.Event{ev(21, "start")}); err == nil {
		t.Error("an unknown source must fail")
	}
}

func TestListen_Stats(t *testing.T) {
	t.Parallel()
	s, st := newSvc(t)
	ctx := context.Background()
	got, err := s.Stats(ctx, lt.TrackCitta)
	if err != nil || got.Plays != 0 || got.CompletionRate != nil || got.LastPlayedAt != nil {
		t.Fatalf("no events: %+v %v", got, err)
	}
	log := func(p auth.Principal, evs ...listening.Event) {
		t.Helper()
		if _, _, rej, err := s.Log(ctx, p, "api", evs); err != nil || len(rej) != 0 {
			t.Fatalf("log: %v %v", err, rej)
		}
	}
	e := func(n int, kind string, at time.Duration) listening.Event {
		x := ev(n, kind)
		x.TrackID = lt.TrackCitta
		x.ClientAt = lt.FixedNow.Add(at)
		return x
	}
	log(human, e(1, "start", -10*time.Minute), e(2, "complete", -9*time.Minute))
	log(dev, e(3, "start", -5*time.Minute))
	log(agent, e(4, "start", -1*time.Minute), e(5, "start", -1*time.Minute+time.Second), e(6, "complete", 0)) // the critic does not count
	got, err = s.Stats(ctx, lt.TrackCitta)
	if err != nil {
		t.Fatal(err)
	}
	if got.Plays != 2 || got.CompletionRate == nil || *got.CompletionRate != 0.5 || got.LastPlayedAt == nil || *got.LastPlayedAt != lt.FixedNow.Add(-5*time.Minute).UTC().Format(time.RFC3339) {
		t.Errorf("stats %+v", got)
	}
	// more completes than starts: capped at 1
	log(human, e(7, "complete", -4*time.Minute), e(8, "complete", -3*time.Minute))
	got, _ = s.Stats(ctx, lt.TrackCitta)
	if got.CompletionRate == nil || *got.CompletionRate != 1 {
		t.Errorf("rate %v, want capped at 1", got.CompletionRate)
	}
	// the stored principal kind and source
	var kind, src string
	if err := st.R.QueryRow(`SELECT principal_kind, source FROM listen_events WHERE id = ?`, id(4)).Scan(&kind, &src); err != nil || kind != "agent" || src != "api" {
		t.Errorf("row: %q %q %v", kind, src, err)
	}
}

// A beacon, a keepalive fetch and the next load's flush can deliver the same client event ids at once;
// the id is the primary key and the insert ignores a conflict, so exactly one row per id is stored and
// every delivery adds up to accepted + duplicates == deliveries.
func TestListen_ConcurrentDuplicateDeliveries(t *testing.T) {
	t.Parallel()
	s, st := newSvc(t)
	ctx := context.Background()
	const ids, deliveries = 5, 12
	var base, baseDistinct int
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(DISTINCT id) FROM listen_events`).Scan(&base, &baseDistinct); err != nil {
		t.Fatal(err)
	}
	basePlays, err := s.Stats(ctx, lt.TrackLuna)
	if err != nil {
		t.Fatal(err)
	}
	batch := func() []listening.Event {
		evs := make([]listening.Event, 0, ids)
		for n := 1; n <= ids; n++ {
			evs = append(evs, ev(n, "start"))
		}
		return evs
	}
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		acc, dups int
	)
	start := make(chan struct{})
	for range deliveries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			a, d, rej, err := s.Log(ctx, human, "web", batch())
			if err != nil || len(rej) != 0 {
				t.Errorf("Log: %v rejected=%v", err, rej)
				return
			}
			mu.Lock()
			acc, dups = acc+a, dups+d
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()
	if acc != ids || dups != ids*(deliveries-1) {
		t.Fatalf("accepted=%d duplicates=%d, want %d and %d", acc, dups, ids, ids*(deliveries-1))
	}
	var rows, distinct int
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(DISTINCT id) FROM listen_events`).Scan(&rows, &distinct); err != nil {
		t.Fatal(err)
	}
	if rows != base+ids || distinct != baseDistinct+ids {
		t.Fatalf("stored %d rows (%d distinct ids), want %d new", rows, distinct, ids)
	}
	if got, err := s.Stats(ctx, lt.TrackLuna); err != nil || got.Plays != basePlays.Plays+ids {
		t.Fatalf("plays = %+v err=%v, want %d more than %d", got, err, ids, basePlays.Plays)
	}
}
