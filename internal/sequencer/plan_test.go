package sequencer

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/perftest"
)

// The signatures of C137 that lanes SQ1 and SQ2 keep.
var (
	_ func([]Song, Options) (Result, error) = Plan
	_ func(Song, Song) Parts                = TransitionMicro
	_ func(Song, Song) int64                = Distance
	_ func(Step, Strategy) string           = ExplainText
	_ func([]Song, Options) string          = Digest
)

func TestStrategies_Registry(t *testing.T) {
	t.Parallel()
	want := []Strategy{"arc", "ramp_up", "steady"}
	if !slices.Equal(Strategies, want) {
		t.Errorf("Strategies = %v, want %v", Strategies, want)
	}
	if MaxSongs != 60 || ExactMax != 12 || BeamWidth != 8 || MaxLocalPasses != 50 {
		t.Error("a C133 constant changed")
	}
}

func rnd(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)) }

// randSet is a seeded synthetic set; some features are unknown.
func randSet(r *rand.Rand, n int, unknown bool) []Song {
	ss := make([]Song, n)
	for i := range ss {
		s := Song{ID: fmt.Sprintf("s%02d", i), BPMx100: 7000 + r.IntN(7000), Energyx10000: r.IntN(10001), Key: &Camelot{1 + r.IntN(12), "AB"[r.IntN(2)]}}
		if unknown {
			if r.IntN(5) == 0 {
				s.BPMx100 = 0
			}
			if r.IntN(5) == 0 {
				s.Key = nil
			}
			if r.IntN(5) == 0 {
				s.Energyx10000 = NoEnergy
			}
		}
		ss[i] = s
	}
	r.Shuffle(n, func(a, b int) { ss[a], ss[b] = ss[b], ss[a] })
	return ss
}

func mustPlan(t testing.TB, songs []Song, o Options, po planOpts) Result {
	t.Helper()
	res, err := planWith(songs, o, po)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

var exactOpts = planOpts{exactMax: ExactMax}

func TestPlan_Table(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		o       Options
		want    []string
		inCost  int64 // J, 4 decimals (rounded)
		outCost int64
	}{
		{"arc", Options{Strategy: StrategyArc}, []string{"t1", "t2", "t3", "t4", "t5", "t6"}, 89_847, 55_198},
		{"ramp_up", Options{Strategy: StrategyRampUp}, []string{"t1", "t6", "t2", "t3", "t4", "t5"}, 95_761, 25_712},
		{"steady", Options{Strategy: StrategySteady}, []string{"t1", "t6", "t2", "t3", "t4", "t5"}, 56_840, 33_709},
		{"arc first t5 last t1", Options{Strategy: StrategyArc, First: "t5", Last: "t1"}, []string{"t5", "t4", "t3", "t2", "t6", "t1"}, 89_847, 92_694},
		{"arc last t5", Options{Strategy: StrategyArc, Last: "t5"}, []string{"t6", "t2", "t3", "t1", "t4", "t5"}, 89_847, 75_033},
		{"arc first t6", Options{Strategy: StrategyArc, First: "t6"}, []string{"t6", "t2", "t1", "t5", "t4", "t3"}, 89_847, 57_820},
	} {
		res := mustPlan(t, tSet(), c.o, exactOpts)
		if !slices.Equal(res.Order, c.want) {
			t.Errorf("%s: order = %v, want %v", c.name, res.Order, c.want)
		}
		if !res.Exact {
			t.Errorf("%s: not exact", c.name)
		}
		round := func(j int64) int64 { return (j + 500_000) / 1_000_000 }
		if round(res.CostInput) != c.inCost {
			t.Errorf("%s: input cost = %d, want about %d", c.name, res.CostInput, c.inCost)
		}
		if round(res.CostOutput) != c.outCost {
			t.Errorf("%s: output cost = %d, want about %d", c.name, res.CostOutput, c.outCost)
		}
		if len(res.Steps) != 6 || res.Steps[0].Position != 1 {
			t.Errorf("%s: steps = %d", c.name, len(res.Steps))
		}
	}
	arc := mustPlan(t, tSet(), Options{Strategy: StrategyArc}, exactOpts)
	if arc.CostInput != 89_847_130_000 || arc.CostOutput != 55_198_370_000 {
		t.Errorf("arc costs = %d, %d", arc.CostInput, arc.CostOutput)
	}
	if arc.ImprovementPctX10 == nil || *arc.ImprovementPctX10 != 386 {
		t.Errorf("arc improvement = %v, want 386", arc.ImprovementPctX10)
	}
	golden(t, "plan_tset_arc.golden.json", arc)
	golden(t, "plan_tset_ramp_up.golden.json", mustPlan(t, tSet(), Options{Strategy: StrategyRampUp}, exactOpts))
	golden(t, "plan_tset_steady.golden.json", mustPlan(t, tSet(), Options{Strategy: StrategySteady}, exactOpts))
	golden(t, "plan_tset_arc_endpoints.golden.json", mustPlan(t, tSet(), Options{Strategy: StrategyArc, First: "t5", Last: "t1"}, exactOpts))
}

func TestPlan_Symmetry(t *testing.T) {
	t.Parallel()
	r := rnd(1)
	for range 1000 {
		a, b := randSet(r, 2, true)[0], randSet(r, 2, true)[1]
		if tempoMicro(a.BPMx100, b.BPMx100) != tempoMicro(b.BPMx100, a.BPMx100) || keyMicro(a.Key, b.Key) != keyMicro(b.Key, a.Key) {
			t.Fatalf("asymmetric: %+v %+v", a, b)
		}
	}
}

func TestPlan_UnknownFeatures(t *testing.T) {
	t.Parallel()
	ss := tSet()
	ss[0].Key, ss[1].Key = nil, nil
	res := mustPlan(t, ss, Options{Strategy: StrategyArc}, exactOpts)
	if res.Coverage.Key != 66 || res.Coverage.Tempo != 100 {
		t.Errorf("coverage = %+v", res.Coverage)
	}
	spec, _ := SpecOf(StrategyArc)
	if effWeights(spec.Weights, res.Coverage).Key != 80*66 {
		t.Error("key weight not coverage scaled")
	}
	if keyMicro(ss[0].Key, ss[2].Key) != 500_000 {
		t.Error("unknown key pair is not neutral")
	}
	if slices.ContainsFunc(res.Warnings, func(w Warning) bool { return w.Code == WarnLowCoverage }) {
		t.Error("66 % key coverage must not warn")
	}
	if len(res.Order) != 6 {
		t.Errorf("order = %v", res.Order)
	}
}

func TestPlan_AllUnknown(t *testing.T) {
	t.Parallel()
	ss := []Song{{ID: "c", Energyx10000: NoEnergy}, {ID: "a", Energyx10000: NoEnergy}, {ID: "b", Energyx10000: NoEnergy}}
	for _, po := range []planOpts{exactOpts, {exactMax: ExactMax, forceHeuristic: true}} {
		res := mustPlan(t, ss, Options{Strategy: StrategyArc}, po)
		// With nothing known every order costs the same, so the planner returns the input order when it is a candidate
		// (heuristic) or the smallest id order (exact); both carry one no_features warning per song.
		if res.Exact && !slices.Equal(res.Order, []string{"a", "b", "c"}) {
			t.Errorf("exact order = %v", res.Order)
		}
		if !res.Exact && !slices.Equal(res.Order, []string{"c", "a", "b"}) && !slices.Equal(res.Order, []string{"a", "b", "c"}) {
			t.Errorf("heuristic order = %v", res.Order)
		}
		n := 0
		for _, w := range res.Warnings {
			if w.Code == WarnNoFeatures {
				n++
			}
		}
		if n != 3 || res.CostInput != 0 || res.CostOutput != 0 || res.ImprovementPctX10 != nil {
			t.Errorf("warnings %d, costs %d %d", n, res.CostInput, res.CostOutput)
		}
	}
}

func TestPlan_NeverWorse(t *testing.T) {
	t.Parallel()
	r := rnd(2)
	for i := range 200 {
		n := 2 + r.IntN(13)
		ss := randSet(r, n, i%2 == 0)
		st := Strategies[i%3]
		for _, po := range []planOpts{exactOpts, {exactMax: ExactMax, forceHeuristic: true}} {
			res := mustPlan(t, ss, Options{Strategy: st}, po)
			if res.CostOutput > res.CostInput {
				t.Fatalf("case %d n=%d %s force=%v: output %d > input %d", i, n, st, po.forceHeuristic, res.CostOutput, res.CostInput)
			}
			if res.CostOutput != objective(orderOf(ss, res.Order), mustSpec(st)) {
				t.Fatalf("case %d: CostOutput is not the objective of the order", i)
			}
		}
	}
}

func mustSpec(s Strategy) Spec { sp, _ := SpecOf(s); return sp }

func orderOf(ss []Song, ids []string) []Song {
	out := make([]Song, 0, len(ids))
	for _, id := range ids {
		for _, s := range ss {
			if s.ID == id {
				out = append(out, s)
			}
		}
	}
	return out
}

func TestPlan_EndpointsMayCostMore(t *testing.T) {
	t.Parallel()
	res := mustPlan(t, tSet(), Options{Strategy: StrategyArc, First: "t5", Last: "t1"}, exactOpts)
	if res.CostOutput <= res.CostInput {
		t.Fatalf("costs %d %d", res.CostInput, res.CostOutput)
	}
	if res.ImprovementPctX10 == nil || *res.ImprovementPctX10 != -32 {
		t.Errorf("improvement = %v, want -32", res.ImprovementPctX10)
	}
	if !slices.ContainsFunc(res.Warnings, func(w Warning) bool { return w.Code == WarnEndpointCost }) {
		t.Error("no endpoint_cost warning")
	}
	// Heuristic path: the output is never worse than the repaired input.
	r := rnd(3)
	for i := range 60 {
		ss := randSet(r, 13+r.IntN(10), false)
		o := Options{Strategy: Strategies[i%3], First: ss[r.IntN(len(ss))].ID}
		for o.Last == "" || o.Last == o.First {
			o.Last = ss[r.IntN(len(ss))].ID
		}
		res := mustPlan(t, ss, o, exactOpts)
		var rep []Song
		rep = append(rep, orderOf(ss, []string{o.First})...)
		for _, s := range ss {
			if s.ID != o.First && s.ID != o.Last {
				rep = append(rep, s)
			}
		}
		rep = append(rep, orderOf(ss, []string{o.Last})...)
		if res.CostOutput > objective(rep, mustSpec(o.Strategy)) {
			t.Fatalf("case %d: worse than the repaired input", i)
		}
		if res.Order[0] != o.First || res.Order[len(res.Order)-1] != o.Last {
			t.Fatalf("case %d: endpoints moved: %v", i, res.Order)
		}
	}
}

func TestPlan_Deterministic(t *testing.T) {
	t.Parallel()
	r := rnd(4)
	for _, n := range []int{6, 12, 13, 30} {
		ss := randSet(r, n, true)
		for _, st := range Strategies {
			a := mustPlan(t, ss, Options{Strategy: st}, exactOpts)
			b := mustPlan(t, ss, Options{Strategy: st}, exactOpts)
			if !reflect.DeepEqual(a, b) {
				t.Errorf("n=%d %s: results differ", n, st)
			}
			if a.Digest == "" || a.Digest != Digest(ss, Options{Strategy: st}) {
				t.Errorf("digest")
			}
		}
	}
}

func TestPlan_ExactPermutationInvariant(t *testing.T) {
	t.Parallel()
	r := rnd(5)
	for i := range 30 {
		ss := randSet(r, 3+r.IntN(10), i%2 == 0)
		st := Strategies[i%3]
		want := mustPlan(t, ss, Options{Strategy: st}, exactOpts).Order
		for range 3 {
			sh := slices.Clone(ss)
			r.Shuffle(len(sh), func(a, b int) { sh[a], sh[b] = sh[b], sh[a] })
			if got := mustPlan(t, sh, Options{Strategy: st}, exactOpts).Order; !slices.Equal(got, want) {
				t.Fatalf("case %d: %v != %v", i, got, want)
			}
		}
	}
}

func TestPlan_Endpoints(t *testing.T) {
	t.Parallel()
	ss := tSet()
	for _, c := range []struct {
		o    Options
		want error
	}{
		{Options{First: "zz"}, ErrEndpointMissing},
		{Options{Last: "zz"}, ErrEndpointMissing},
		{Options{First: "t1", Last: "t1"}, ErrEndpointsConflict},
		{Options{Strategy: "bogus"}, ErrUnknownStrategy},
	} {
		if _, err := Plan(ss, c.o); !errors.Is(err, c.want) {
			t.Errorf("%+v: err = %v, want %v", c.o, err, c.want)
		}
	}
	if _, err := Plan(randSet(rnd(6), MaxSongs+1, false), Options{}); !errors.Is(err, ErrTooMany) {
		t.Errorf("too many: %v", err)
	}
	one, err := Plan(ss[:1], Options{First: "t4", Last: "t4"})
	if err != nil || len(one.Order) != 1 || !slices.ContainsFunc(one.Warnings, func(w Warning) bool { return w.Code == WarnSingleSong }) {
		t.Errorf("single: %+v %v", one, err)
	}
	if res, err := Plan(nil, Options{}); err != nil || len(res.Order) != 0 {
		t.Errorf("empty: %+v %v", res, err)
	}
}

func permutations(n int, f func([]int)) {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	var rec func(k int)
	used := make([]bool, n)
	cur := make([]int, 0, n)
	rec = func(k int) {
		if k == n {
			f(cur)
			return
		}
		for i := range n {
			if !used[i] {
				used[i] = true
				cur = append(cur, i)
				rec(k + 1)
				cur = cur[:len(cur)-1]
				used[i] = false
			}
		}
	}
	rec(0)
}

func TestPlan_ExactMatchesBruteForce(t *testing.T) {
	t.Parallel()
	r := rnd(7)
	for c := range 50 {
		n := 2 + c%6
		ss := randSet(r, n, c%3 == 0)
		if c%5 == 0 { // force ties
			for i := range ss {
				ss[i].BPMx100, ss[i].Energyx10000 = 10000, 5000
			}
		}
		st := Strategies[c%3]
		o := Options{Strategy: st}
		if c%4 == 0 && n > 2 {
			o.First, o.Last = "s00", "s01"
		}
		byID := slices.Clone(ss)
		slices.SortFunc(byID, func(a, b Song) int { return compareID(a.ID, b.ID) })
		var bestOrder []string
		bestCost := int64(-1)
		permutations(n, func(p []int) {
			if o.First != "" && byID[p[0]].ID != o.First || o.Last != "" && byID[p[n-1]].ID != o.Last {
				return
			}
			ord := make([]Song, n)
			for i, k := range p {
				ord[i] = byID[k]
			}
			if cost := objective(ord, mustSpec(st)); bestCost < 0 || cost < bestCost {
				bestCost = cost
				bestOrder = bestOrder[:0]
				for _, s := range ord {
					bestOrder = append(bestOrder, s.ID)
				}
			}
		})
		res := mustPlan(t, ss, o, exactOpts)
		if res.CostOutput != bestCost || !slices.Equal(res.Order, bestOrder) {
			t.Fatalf("set %d (n=%d %s): got %v @%d, want %v @%d", c, n, st, res.Order, res.CostOutput, bestOrder, bestCost)
		}
	}
}

func compareID(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func TestSequencer_HeuristicGap(t *testing.T) {
	t.Parallel()
	r := rnd(8)
	var sum float64
	var worst float64
	const total = 120
	for i := range total {
		n := 13 + i%3
		ss := randSet(r, n, false)
		o := Options{Strategy: Strategies[(i/3)%3]}
		h := mustPlan(t, ss, o, planOpts{exactMax: 15, forceHeuristic: true})
		e := mustPlan(t, ss, o, planOpts{exactMax: 15})
		if h.Exact || !e.Exact || e.CostOutput > h.CostOutput {
			t.Fatalf("case %d: exact=%v/%v costs %d/%d", i, h.Exact, e.Exact, e.CostOutput, h.CostOutput)
		}
		gap := 0.0
		if e.CostOutput > 0 {
			gap = float64(h.CostOutput-e.CostOutput) / float64(e.CostOutput)
		}
		sum += gap
		worst = max(worst, gap)
	}
	if mean := sum / total; mean > 0.02 || worst > 0.10 {
		t.Errorf("gap mean %.4f worst %.4f, want <= 0.02 / 0.10", mean, worst)
	}
}

// greedyCost is the cost of a nearest-neighbour order started from the first input song.
func greedyCost(ss []Song, spec Spec) int64 {
	w := effWeights(spec.Weights, coverageOf(ss))
	rel := withRel(ss)
	used := make([]bool, len(ss))
	order := []int{0}
	used[0] = true
	for len(order) < len(ss) {
		cur, best, bc := order[len(order)-1], -1, int64(0)
		for j := range ss {
			if used[j] {
				continue
			}
			if c := transJ(w, rel[cur], rel[j]); best < 0 || c < bc {
				best, bc = j, c
			}
		}
		used[best] = true
		order = append(order, best)
	}
	out := make([]Song, len(ss))
	for i, k := range order {
		out[i] = ss[k]
	}
	return objective(out, spec)
}

func TestSequencer_Heuristic_LargeN(t *testing.T) {
	t.Parallel()
	r := rnd(9)
	for _, n := range []int{30, 60} {
		for _, st := range Strategies {
			ss := randSet(r, n, true)
			first := mustPlan(t, ss, Options{Strategy: st}, exactOpts)
			if first.Exact {
				t.Fatal("large n ran exact")
			}
			if !slices.ContainsFunc(first.Warnings, func(w Warning) bool { return w.Code == WarnHeuristic }) {
				t.Error("no heuristic warning")
			}
			var wg sync.WaitGroup
			for range 4 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if got, _ := Plan(ss, Options{Strategy: st}); !reflect.DeepEqual(got, first) {
						t.Errorf("n=%d %s: runs differ", n, st)
					}
				}()
			}
			wg.Wait()
			spec := mustSpec(st)
			if first.CostOutput > first.CostInput || first.CostOutput > greedyCost(ss, spec) {
				t.Errorf("n=%d %s: output %d, input %d, greedy %d", n, st, first.CostOutput, first.CostInput, greedyCost(ss, spec))
			}
		}
	}
}

func TestPerf_Sequencer(t *testing.T) {
	r := rnd(10)
	for _, c := range []struct{ n int }{{12}, {60}} {
		budget := perftest.Budget(t, 100*time.Millisecond)
		ss := randSet(r, c.n, true)
		for _, st := range Strategies {
			start := time.Now()
			if _, err := Plan(ss, Options{Strategy: st}); err != nil {
				t.Fatal(err)
			}
			if d := time.Since(start); d > budget {
				t.Errorf("n=%d %s: %v > %v", c.n, st, d, budget)
			}
		}
	}
}
