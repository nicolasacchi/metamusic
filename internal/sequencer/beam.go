package sequencer

import "sort"

type partial struct {
	seq  []int
	used uint64
	cost int64
}

func lessSeq(a, b []int) bool {
	for k := range a {
		if k >= len(b) {
			return false
		}
		if a[k] != b[k] {
			return a[k] < b[k]
		}
	}
	return len(a) < len(b)
}

// beam is the beam search of C137.4: partial sequences ranked by (cost, id sequence), the best 3*BeamWidth kept per
// depth, started from every song or from First. Last is held back to the final position.
func (pr *problem) beam() []int {
	n := pr.n
	keep := 3 * BeamWidth
	var cur []partial
	for i := 0; i < n; i++ {
		if pr.first >= 0 && i != pr.first {
			continue
		}
		if pr.last >= 0 && i == pr.last && n > 1 {
			continue
		}
		cur = append(cur, partial{seq: []int{i}, used: 1 << uint(i), cost: pr.shape[i][0]})
	}
	for depth := 1; depth < n; depth++ {
		var nxt []partial
		for _, c := range cur {
			tail := c.seq[len(c.seq)-1]
			for j := 0; j < n; j++ {
				if c.used>>uint(j)&1 == 1 {
					continue
				}
				if pr.last >= 0 && (j == pr.last) != (depth == n-1) {
					continue
				}
				seq := make([]int, depth+1)
				copy(seq, c.seq)
				seq[depth] = j
				nxt = append(nxt, partial{seq: seq, used: c.used | 1<<uint(j), cost: c.cost + pr.trans[tail][j] + pr.shape[j][depth]})
			}
		}
		sort.Slice(nxt, func(a, b int) bool {
			if nxt[a].cost != nxt[b].cost {
				return nxt[a].cost < nxt[b].cost
			}
			return lessSeq(nxt[a].seq, nxt[b].seq)
		})
		if len(nxt) > keep {
			nxt = nxt[:keep]
		}
		cur = nxt
	}
	best := cur[0]
	return best.seq
}

// heuristic runs the beam and the local search from its result and from the (repaired) input order and returns the
// better one, ties lexicographic.
func (pr *problem) heuristic(input []int) []int {
	a := pr.localSearch(pr.beam())
	start := input
	if pr.first >= 0 || pr.last >= 0 {
		start = pr.repair(input)
	}
	b := pr.localSearch(append([]int(nil), start...))
	ca, cb := pr.cost(a), pr.cost(b)
	if cb < ca || (cb == ca && lessSeq(b, a)) {
		return b
	}
	return a
}
