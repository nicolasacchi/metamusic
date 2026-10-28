package albums_test

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/albums"
	"github.com/nicolasacchi/metamusic/internal/curation"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/perftest"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestPerf_AlbumCatalog: 1 000 Songs, 200 Muses, 30 published Records: List and Get of every album, p95 under 150 ms per call
// (C132.4).
func TestPerf_AlbumCatalog(t *testing.T) {
	budget := perftest.BudgetRace(t, 150*time.Millisecond, 20)
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	info := librarytest.FixtureL2Scale(t, st, librarytest.FixedNow, 1000, 200, 30)
	if _, err := st.W.Exec(`UPDATE records SET status = 'published', published_by = 'human:fixture', published_at = '2026-10-28T09:00:00.000Z', first_published_at = '2026-10-28T09:00:00.000Z'`); err != nil {
		t.Fatal(err)
	}
	svc := albums.New(st, records.New(st, nil, nil), nil, curation.New(st, nil), nil)
	ctx := context.Background()
	var durs []time.Duration
	var ids []string
	for i := 0; i < 20; i++ {
		sw := perftest.Start()
		got, err := svc.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		durs = append(durs, sw.Elapsed())
		if i == 0 {
			if len(got) != len(info.Records) {
				t.Fatalf("%d albums, want %d", len(got), len(info.Records))
			}
			for _, a := range got {
				ids = append(ids, a.ID)
			}
		}
	}
	for _, id := range ids {
		sw := perftest.Start()
		if _, _, err := svc.Get(ctx, id); err != nil {
			t.Fatal(err)
		}
		durs = append(durs, sw.Elapsed())
	}
	slices.Sort(durs)
	if p95 := durs[len(durs)*95/100]; p95 > budget {
		t.Errorf("p95 %v over budget %v", p95, budget)
	}
}
