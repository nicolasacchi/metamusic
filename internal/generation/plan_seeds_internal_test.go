package generation

import (
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// noPlan is yue2 without the PlanScore capability: the engine of a mode check that must refuse plan_seeds.
type noPlan struct{ engine.Engine }

func (n noPlan) Caps() engine.Caps {
	c := n.Engine.Caps()
	c.PlanScore = false
	return c
}

// TestValidatePlanSeeds_EngineNeedsPlanScore: an engine that cannot plan scores refuses the mode with 422 ENGINE_CAPABILITY.
func TestValidatePlanSeeds_EngineNeedsPlanScore(t *testing.T) {
	t.Parallel()
	parent := "t1"
	it := Item{Mode: lineage.ModePlanSeeds, ParentTrackID: &parent}
	if err := validatePlanSeedsItem(0, it, yue2.Engine{}); err != nil {
		t.Fatalf("yue2: %v", err)
	}
	err := validatePlanSeedsItem(0, it, noPlan{yue2.Engine{}})
	var pe *problem.Error
	if !errors.As(err, &pe) || pe.Status != http.StatusUnprocessableEntity || pe.Code != "ENGINE_CAPABILITY" {
		t.Errorf("err = %v", err)
	}
}

// TestSeedsPerJob: the largest count whose quoted timeout still fits the runtime cap (C360.5): 11 seeds of a 200 s song and 8 of a
// 280 s song at the default 900 s cap; at least 1 and at most 16.
func TestSeedsPerJob(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ audioS, maxRuntimeS, want int }{
		{200, 900, 11}, {280, 900, 8}, {30, 900, 16}, {600, 900, 3}, {600, 200, 1}, {360, 120, 1},
	} {
		if got := seedsPerJob(c.audioS, c.maxRuntimeS); got != c.want {
			t.Errorf("seedsPerJob(%d, %d) = %d, want %d", c.audioS, c.maxRuntimeS, got, c.want)
		}
		// the answer fits, and one more would not (unless the cap or the floor binds)
		n := seedsPerJob(c.audioS, c.maxRuntimeS)
		q := func(k int) int {
			return int(spend.QuoteTakes(spend.QuoteParams{PricePPH: 1, MaxRuntimeS: 1 << 20}, []spend.TakeIn{{AudioS: k * c.audioS, RTFMilli: spend.PlanMilli, LoadS: spend.LoadS}}).Takes[0].TimeoutS)
		}
		if n > 1 && q(n) > c.maxRuntimeS {
			t.Errorf("%d seeds of %d s time out at %d s over the cap %d", n, c.audioS, q(n), c.maxRuntimeS)
		}
		if n < 16 && c.maxRuntimeS >= 300 && q(n+1) <= c.maxRuntimeS {
			t.Errorf("%d seeds of %d s would still fit (%d s)", n+1, c.audioS, q(n+1))
		}
	}
}

// TestSplitSeeds: near-equal chunks, larger first, every seed once, in order.
func TestSplitSeeds(t *testing.T) {
	t.Parallel()
	seeds := make([]int64, 16)
	for i := range seeds {
		seeds[i] = int64(i)
	}
	for _, c := range []struct {
		n, per int
		sizes  []int
	}{{16, 11, []int{8, 8}}, {16, 16, []int{16}}, {5, 2, []int{2, 2, 1}}, {1, 4, []int{1}}, {16, 1, []int{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}}} {
		var sizes []int
		var all []int64
		for _, ch := range splitSeeds(seeds[:c.n], c.per) {
			sizes = append(sizes, len(ch))
			all = append(all, ch...)
		}
		if !slices.Equal(sizes, c.sizes) || !slices.Equal(all, seeds[:c.n]) {
			t.Errorf("splitSeeds(%d, %d) = %v", c.n, c.per, sizes)
		}
	}
}

// TestQuote_PlanJobExample_200s: A = 200, n = 8 seeds quotes expected_s 372 and timeout_s 660 (D-DESK-4, C360.8).
func TestQuote_PlanJobExample_200s(t *testing.T) {
	t.Parallel()
	q := spend.QuoteTakes(spend.QuoteParams{PricePPH: 1, MaxRuntimeS: 900}, []spend.TakeIn{{AudioS: 8 * 200, RTFMilli: spend.PlanMilli, LoadS: spend.LoadS}})
	if tc := q.Takes[0]; tc.ExpectedS != 372 || tc.TimeoutS != 660 {
		t.Errorf("expected_s %d timeout_s %d, want 372 and 660", tc.ExpectedS, tc.TimeoutS)
	}
	if got := planSecondsPerSeed(200); got != 44 {
		t.Errorf("s_per_seed = %d", got)
	}
}
