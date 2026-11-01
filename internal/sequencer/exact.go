package sequencer

import "math/bits"

const infCost = int64(1) << 60

// exact is the suffix Held-Karp of C137.4. g[S*n+i] is the cheapest suffix that starts with song i at position
// n-|S|-1 and then orders the set S; ties go to the smallest next song, so the whole answer is the
// lexicographically smallest optimum and does not depend on the input order.
func (pr *problem) exact() []int {
	n := pr.n
	full := 1<<n - 1
	g := make([]int64, (full+1)*n)
	next := make([]int8, (full+1)*n)
	for s := 0; s <= full; s++ {
		p := n - bits.OnesCount(uint(s)) - 1
		if p < 0 {
			continue
		}
		for i := 0; i < n; i++ {
			if s>>i&1 == 1 {
				continue
			}
			at := s*n + i
			if s == 0 {
				g[at], next[at] = pr.shape[i][n-1], -1
				if pr.last >= 0 && i != pr.last {
					g[at] = infCost
				}
				continue
			}
			if i == pr.last {
				g[at], next[at] = infCost, -1
				continue
			}
			bestC, bestJ := infCost, int8(-1)
			for j := 0; j < n; j++ {
				if s>>j&1 == 0 {
					continue
				}
				rest := g[(s&^(1<<j))*n+j]
				if rest >= infCost {
					continue
				}
				if c := pr.trans[i][j] + rest; c < bestC {
					bestC, bestJ = c, int8(j)
				}
			}
			if bestJ < 0 {
				g[at], next[at] = infCost, -1
			} else {
				g[at], next[at] = pr.shape[i][p]+bestC, bestJ
			}
		}
	}
	start, bestC := -1, infCost
	for i := 0; i < n; i++ {
		if pr.first >= 0 && i != pr.first {
			continue
		}
		if c := g[(full&^(1<<i))*n+i]; c < bestC {
			start, bestC = i, c
		}
	}
	out := make([]int, 0, n)
	s, i := full&^(1<<start), start
	for {
		out = append(out, i)
		j := next[s*n+i]
		if j < 0 {
			break
		}
		s &^= 1 << int(j)
		i = int(j)
	}
	return out
}
