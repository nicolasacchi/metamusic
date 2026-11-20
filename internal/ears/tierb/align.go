package tierb

import "math"

// Levenshtein is the edit distance between a and b in runes.
func Levenshtein(a, b []rune) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			c := prev[j-1]
			if a[i-1] != b[j-1] {
				c++
			}
			cur[j] = min(c, prev[j]+1, cur[j-1]+1)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// CharSim is 1 - Levenshtein/max(runes); two empty strings are identical (1).
func CharSim(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	m := max(len(ra), len(rb))
	if m == 0 {
		return 1
	}
	return 1 - float64(Levenshtein(ra, rb))/float64(m)
}

// FoldSim is CharSim of the Fold of both words: the similarity the alignment uses.
func FoldSim(a, b string) float64 { return CharSim(Fold(a), Fold(b)) }

// Alignment operations.
const (
	OpMatch  = "M" // ref and hyp word identical after Fold
	OpSub    = "S"
	OpDelete = "D" // ref word not heard
	OpInsert = "I" // heard word not in the reference
)

// Pair is one step of an alignment. Ref or Hyp is -1 when the step has no such word.
type Pair struct {
	Ref int
	Hyp int
	Op  string
	Sim float64 // similarity of the two words, 0 for D and I
}

const eps = 1e-9

// Align is Needleman-Wunsch over words: match 0, substitution 1 - sim, deletion and insertion 1; ties are
// resolved M or S first, then D, then I, so the result is deterministic. Every ref and hyp index appears in
// exactly one Pair, in order. sim must return a value in [0, 1] and 1 only for words to be treated as equal.
func Align(ref, hyp []string, sim func(a, b string) float64) []Pair {
	n, m := len(ref), len(hyp)
	w := m + 1
	// cost[i*w+j] is the cheapest alignment of ref[i:] with hyp[j:]; walking forward from (0,0) then lets
	// the priority M/S, D, I break ties towards the start of both sequences.
	cost := make([]float64, (n+1)*w)
	sims := make([]float64, n*m)
	for i := n - 1; i >= 0; i-- {
		cost[i*w+m] = float64(n - i)
	}
	for j := m - 1; j >= 0; j-- {
		cost[n*w+j] = float64(m - j)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			s := sim(ref[i], hyp[j])
			sims[i*m+j] = s
			cost[i*w+j] = math.Min(cost[(i+1)*w+j+1]+1-s, math.Min(cost[(i+1)*w+j]+1, cost[i*w+j+1]+1))
		}
	}
	var out []Pair
	i, j := 0, 0
	for i < n || j < m {
		c := cost[i*w+j]
		switch {
		case i < n && j < m && math.Abs(cost[(i+1)*w+j+1]+1-sims[i*m+j]-c) < eps:
			s := sims[i*m+j]
			op := OpSub
			if s >= 1 {
				op = OpMatch
			}
			out = append(out, Pair{Ref: i, Hyp: j, Op: op, Sim: s})
			i, j = i+1, j+1
		case i < n && math.Abs(cost[(i+1)*w+j]+1-c) < eps:
			out = append(out, Pair{Ref: i, Hyp: -1, Op: OpDelete})
			i++
		default:
			out = append(out, Pair{Ref: -1, Hyp: j, Op: OpInsert})
			j++
		}
	}
	return out
}
