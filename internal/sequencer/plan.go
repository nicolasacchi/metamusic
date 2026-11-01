package sequencer

import (
	"sort"
)

// planOpts are the test-only limits of the planner: the exported Plan passes ExactMax and no forcing (C137.4).
type planOpts struct {
	exactMax       int
	forceHeuristic bool
}

// Plan orders songs for the strategy (C137.4). Without endpoints CostOutput never exceeds CostInput; with endpoints
// the input order may violate them and then is not a candidate. The same ordered input always gives the same Result;
// only the exact path (n <= ExactMax) is also independent of the input order.
func Plan(songs []Song, o Options) (Result, error) {
	return planWith(songs, o, planOpts{exactMax: ExactMax})
}

// problem is a plan in index space: songs are indexed by their sorted id (stable for equal ids), so the smallest
// index is the lexicographically smallest id, and every cost is a table lookup.
type problem struct {
	n           int
	trans       [][]int64 // trans[i][j]: weighted transition cost
	shape       [][]int64 // shape[i][p]: weighted shape cost of song i at position p
	first, last int       // -1 when free
}

// cost is J of an order of indices.
func (pr *problem) cost(order []int) int64 {
	var j int64
	for p, i := range order {
		j += pr.shape[i][p]
		if p+1 < len(order) {
			j += pr.trans[i][order[p+1]]
		}
	}
	return j
}

func newProblem(sorted []Song, spec Spec) *problem {
	n := len(sorted)
	w := effWeights(spec.Weights, coverageOf(sorted))
	rel := withRel(sorted)
	pr := &problem{n: n, trans: make([][]int64, n), shape: make([][]int64, n), first: -1, last: -1}
	for i := range rel {
		pr.trans[i] = make([]int64, n)
		pr.shape[i] = make([]int64, n)
		for j := range rel {
			if i != j {
				pr.trans[i][j] = transJ(w, rel[i], rel[j])
			}
		}
		for p := 0; p < n; p++ {
			pr.shape[i][p] = shapeJ(w, spec, rel[i].Energyx10000, p, n)
		}
	}
	return pr
}

func planWith(songs []Song, o Options, po planOpts) (Result, error) {
	n := len(songs)
	if n > MaxSongs {
		return Result{}, ErrTooMany
	}
	strategy := o.Strategy
	if strategy == "" {
		strategy = StrategyArc
	}
	spec, ok := SpecOf(strategy)
	if !ok {
		return Result{}, ErrUnknownStrategy
	}
	if n > 1 && o.First != "" && o.First == o.Last {
		return Result{}, ErrEndpointsConflict
	}
	for _, e := range []string{o.First, o.Last} {
		if e == "" {
			continue
		}
		found := false
		for _, s := range songs {
			if s.ID == e {
				found = true
				break
			}
		}
		if !found {
			return Result{}, ErrEndpointMissing
		}
	}
	res := Result{Digest: Digest(songs, o), Coverage: coverageOf(songs), Order: []string{}}
	if n == 0 {
		res.Exact = true
		return res, nil
	}

	// sortedIdx[k] is the input position of the k-th song by id; pos is its inverse.
	sortedIdx := make([]int, n)
	for i := range sortedIdx {
		sortedIdx[i] = i
	}
	sort.SliceStable(sortedIdx, func(a, b int) bool { return songs[sortedIdx[a]].ID < songs[sortedIdx[b]].ID })
	sorted := make([]Song, n)
	pos := make([]int, n)
	for k, i := range sortedIdx {
		sorted[k] = songs[i]
		pos[i] = k
	}
	pr := newProblem(sorted, spec)
	for k, s := range sorted {
		if o.First != "" && s.ID == o.First && pr.first < 0 {
			pr.first = k
		}
		if o.Last != "" && s.ID == o.Last && pr.last < 0 {
			pr.last = k
		}
	}
	input := make([]int, n)
	for i := range input {
		input[i] = pos[i]
	}

	exact := n <= po.exactMax && !po.forceHeuristic
	var best []int
	if exact {
		best = pr.exact()
	} else {
		best = pr.heuristic(input)
	}

	ordered := make([]Song, n)
	res.Order = make([]string, n)
	for p, k := range best {
		ordered[p] = sorted[k]
		res.Order[p] = sorted[k].ID
	}
	res.Exact = exact
	res.CostInput = objective(songs, spec)
	res.CostOutput = objective(ordered, spec)
	if res.CostInput != 0 {
		res.ImprovementPctX10 = new(int)
		*res.ImprovementPctX10 = roundDiv((res.CostInput-res.CostOutput)*1000, res.CostInput)
	}
	res.Steps = explainSteps(ordered, spec)
	res.Warnings = coverageWarnings(ordered)
	if !exact {
		res.Warnings = append(res.Warnings, Warning{Code: WarnHeuristic})
	}
	if res.CostOutput > res.CostInput {
		res.Warnings = append(res.Warnings, Warning{Code: WarnEndpointCost})
	}
	return res, nil
}

// roundDiv is a/b rounded half away from zero (b > 0).
func roundDiv(a, b int64) int {
	if a < 0 {
		return -int((-a*2 + b) / (2 * b))
	}
	return int((a*2 + b) / (2 * b))
}

// repair moves First to the front and Last to the end, keeping the relative order of the others.
func (pr *problem) repair(order []int) []int {
	out := make([]int, 0, len(order))
	if pr.first >= 0 {
		out = append(out, pr.first)
	}
	for _, i := range order {
		if i != pr.first && i != pr.last {
			out = append(out, i)
		}
	}
	if pr.last >= 0 {
		out = append(out, pr.last)
	}
	return out
}
