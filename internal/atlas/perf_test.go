package atlas_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/atlas"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/perftest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestPerf_AtlasGet: 5 000 Songs (FixtureL3Scale), tempo x energy: the first page cold (empty layout cache, 20 runs) has a p95
// under 300 ms of CPU, and each of pages 2-9 (scope query and token hash every page, layout from the token-keyed cache) under
// 80 ms. Serial on purpose: the budget is process CPU time, which concurrent tests of the package would inflate.
// calibration is a fixed workload of the same kind as an Atlas page (scan the feature rows through SQLite, hash what was read): its
// CPU time on an idle machine is nominalCalibration. CPU time is not independent of load (SMT neighbours and cache eviction
// stretched a page from 63 ms to 100 ms on a runner twice oversubscribed), so the budgets are multiplied by how much slower this
// reference runs right now, never by less than 1 and never by more than 3: an idle runner keeps the exact budget, a loaded one is
// compared with itself, and a real regression (more work per page, hence more than the budget even after the factor on an idle
// runner) still fails.
const nominalCalibration = 55 * time.Millisecond

func calibration(t *testing.T, st *store.Store) float64 {
	t.Helper()
	best := time.Duration(1<<63 - 1)
	for range 7 {
		sw := perftest.Start()
		for range 4 {
			rows, err := st.R.Query(`SELECT song_id, bpm_x100, energy_x10000, family, vocal, mood FROM song_features`)
			if err != nil {
				t.Fatal(err)
			}
			h := sha256.New()
			for rows.Next() {
				var id string
				var bpm, energy *int
				var family, vocal, mood *string
				if err := rows.Scan(&id, &bpm, &energy, &family, &vocal, &mood); err != nil {
					t.Fatal(err)
				}
				fmt.Fprintf(h, "%s %v %v %v %v %v", id, bpm, energy, family, vocal, mood)
			}
			_ = rows.Close()
			_ = h.Sum(nil)
		}
		best = min(best, sw.Elapsed())
	}
	f := max(1, min(3, float64(best)/float64(nominalCalibration)))
	t.Logf("calibration %v (nominal %v): budgets x%.2f", best, nominalCalibration, f)
	return f
}

// warmPasses is the number of timed passes over the warm pages (after one untimed warm-up pass).
const warmPasses = 5

func TestPerf_AtlasGet(t *testing.T) {
	cold := perftest.BudgetRace(t, 300*time.Millisecond, 25)
	warm := perftest.BudgetRace(t, 80*time.Millisecond, 25)
	st := openStore(t)
	librarytest.FixtureL3Scale(t, st, librarytest.FixedNow, 5000, 200)
	factor := calibration(t, st)
	cold, warm = time.Duration(float64(cold)*factor), time.Duration(float64(warm)*factor)
	ctx := context.Background()
	q := atlas.Query{X: "tempo", Y: "energy", Limit: atlas.MaxPage}

	var colds []time.Duration
	var first atlas.Atlas
	for i := range 3 + 20 { // the first three runs only warm the SQLite page cache and the heap; they are not measured
		runtime.GC()
		svc := atlas.New(st, fixedNow) // a fresh service has an empty layout cache
		sw := perftest.Start()
		a, err := svc.Get(ctx, q)
		if el := sw.Elapsed(); i >= 3 {
			colds = append(colds, el)
		}
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

	// Warm pages: one untimed warm-up pass (it fills the layout cache, the SQLite page cache and the heap, and a collection runs
	// before each timed pass so a GC cycle of the previous pass is not charged to the next), then five timed passes over the
	// pages of one service, the best of the five per page. CPU time on a shared machine has outliers (SMT neighbours, cache
	// eviction); a real regression raises every pass, so the budget stays 80 ms per page and the estimator is the minimum.
	svc := atlas.New(st, fixedNow)
	best := map[int]time.Duration{}
	pages, points := 0, 0
	for pass := range 1 + warmPasses {
		runtime.GC()
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
			if pages >= 2 && pass > 0 {
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
			t.Errorf("page %d took %v (best of %d), budget %v", page, best[page], warmPasses, warm)
		}
	}
	if points != 5000 || pages < 8 || pages > 12 {
		t.Errorf("%d pages, %d points", pages, points)
	}
	t.Logf("cold p95 %v (budget %v); %d pages, best warm %v", colds[18], cold, pages, best)
}
