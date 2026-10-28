package crates_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/crates"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/perftest"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type songCounter struct{ s *songs.Service }

func (c songCounter) CountSongs(ctx context.Context, f songs.ListFilter) (int, error) {
	return c.s.Count(ctx, f)
}

func (songCounter) CountTracks(context.Context, library.SearchParams) (int, error) { return 0, nil }

// TestPerf_SmartCrate: a smart Crate over 10 000 Songs, the shape of the C125.5 filter (without the Sound facet, which needs a
// settled library): count plus the first page in under 300 ms of CPU.
func TestPerf_SmartCrate(t *testing.T) {
	ctx := context.Background()
	budget := perftest.Budget(t, 300*time.Millisecond)
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "perf.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	librarytest.FixtureL2Scale(t, st, librarytest.FixedNow, 10_000, 5, 1)
	n := 0
	newID := func() string { n++; return fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xd2d000+n) }
	now := func() time.Time { return librarytest.FixedNow }
	ssvc := songs.New(st, now, newID)
	svc := crates.New(st, songCounter{ssvc}, now, newID)
	c, count, err := svc.Create(ctx, human, crates.NewCrate{Name: "Perf", Kind: crates.KindSmart, Filter: json.RawMessage(`{"unheard":true,"min_duration_s":150,"pick_decided":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	if count != 10_000 {
		t.Fatalf("count = %d, want 10000", count)
	}
	sw := perftest.Start()
	_, got, err := svc.Get(ctx, c.ID)
	if err != nil || got != 10_000 {
		t.Fatalf("count %d %v", got, err)
	}
	f, err := svc.ExpandSongs(ctx, c.ID, songs.ListFilter{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	page, err := ssvc.List(ctx, f)
	if err != nil || len(page.Items) != 50 {
		t.Fatalf("first page: %d %v", len(page.Items), err)
	}
	el := sw.Elapsed()
	t.Logf("count and first page: %v CPU (budget %v)", el, budget)
	if el > budget {
		t.Errorf("count and first page took %v (budget %v)", el, budget)
	}
}
