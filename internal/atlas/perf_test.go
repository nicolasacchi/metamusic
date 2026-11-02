package atlas_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/atlas"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/perftest"
)

// TestPerf_AtlasGet: 5 000 Songs (FixtureL3Scale), tempo x energy: the first page cold (empty layout cache, 20 runs) has a p95
// under 300 ms of CPU, and each of pages 2-9 (scope query and token hash every page, layout from the token-keyed cache) under
// 80 ms. Serial on purpose: the budget is process CPU time, which concurrent tests of the package would inflate.
func TestPerf_AtlasGet(t *testing.T) {
	cold := perftest.BudgetRace(t, 300*time.Millisecond, 25)
	warm := perftest.BudgetRace(t, 80*time.Millisecond, 25)
	st := openStore(t)
	librarytest.FixtureL3Scale(t, st, librarytest.FixedNow, 5000, 200)
	ctx := context.Background()
	q := atlas.Query{X: "tempo", Y: "energy", Limit: atlas.MaxPage}

	var colds []time.Duration
	var first atlas.Atlas
	for range 20 {
		svc := atlas.New(st, fixedNow) // a fresh service has an empty layout cache
		sw := perftest.Start()
		a, err := svc.Get(ctx, q)
		colds = append(colds, sw.Elapsed())
		if err != nil {
			t.Fatal(err)
		}
		first = a
	}
	if first.Totals.Plotted != 5000 || first.Totals.Scope != 5000 || first.Totals.Truncated || first.NextCursor == nil {
		t.Fatalf("totals %+v, cursor %v", first.Totals, first.NextCursor)
	}
	slices.Sort(colds)
	if p95 := colds[18]; p95 > cold {
		t.Errorf("first page cold p95 = %v, budget %v", p95, cold)
	}

	// Warm pages: three passes over the pages of one service, the best of the three per page (CPU time on a shared machine
	// has outliers; a real regression raises every pass).
	svc := atlas.New(st, fixedNow)
	best := map[int]time.Duration{}
	pages, points := 0, 0
	for range 3 {
		q.Cursor = ""
		pages, points = 0, 0
		for {
			sw := perftest.Start()
			a, err := svc.Get(ctx, q)
			el := sw.Elapsed()
			if err != nil {
				t.Fatal(err)
			}
			pages++
			points += len(a.Points)
			if pages >= 2 {
				if b, ok := best[pages]; !ok || el < b {
					best[pages] = el
				}
			}
			if a.NextCursor == nil {
				break
			}
			q.Cursor = *a.NextCursor
		}
	}
	for page := 2; page <= pages; page++ {
		if best[page] > warm {
			t.Errorf("page %d took %v (best of 3), budget %v", page, best[page], warm)
		}
	}
	if points != 5000 || pages < 8 || pages > 12 {
		t.Errorf("%d pages, %d points", pages, points)
	}
	t.Logf("cold p95 %v (budget %v); %d pages, best warm %v", colds[18], cold, pages, best)
}
