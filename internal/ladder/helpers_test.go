package ladder

import (
	"math"
	"math/rand/v2"
	"sort"
	"testing"
	"time"
)

func near(tb testing.TB, name string, got, want, tol float64) {
	tb.Helper()
	if math.IsNaN(got) || math.Abs(got-want) > tol {
		tb.Errorf("%s = %.6f, want %.6f (tol %g)", name, got, want, tol)
	}
}

func fixedNu(v float64) *float64 { return &v }

// randomProblem builds a connected-ish random problem with ties and anchor losses.
func randomProblem(r *rand.Rand, withTies, withAnchor bool) (int, []Comparison) {
	n := 3 + r.IntN(6)
	var cmps []Comparison
	for k := 0; k < 15+r.IntN(40); k++ {
		i := r.IntN(n)
		j := r.IntN(n - 1)
		if j >= i {
			j++
		}
		switch x := r.Float64(); {
		case withAnchor && x < 0.15:
			cmps = append(cmps, Comparison{Winner: Anchor, Loser: i})
		case withTies && x < 0.4:
			cmps = append(cmps, Comparison{Winner: i, Loser: j, Tie: true})
		default:
			cmps = append(cmps, Comparison{Winner: i, Loser: j})
		}
	}
	return n, cmps
}

func center(x []float64) []float64 {
	m := 0.0
	for _, v := range x {
		m += v
	}
	m /= float64(len(x))
	out := make([]float64, len(x))
	for i, v := range x {
		out[i] = v - m
	}
	return out
}

func rmse(a, b []float64) float64 {
	s := 0.0
	for i := range a {
		s += (a[i] - b[i]) * (a[i] - b[i])
	}
	return math.Sqrt(s / float64(len(a)))
}

func ranks(x []float64) []float64 {
	idx := make([]int, len(x))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return x[idx[a]] < x[idx[b]] })
	r := make([]float64, len(x))
	for k, i := range idx {
		r[i] = float64(k)
	}
	return r
}

func spearman(a, b []float64) float64 {
	ra, rb := ranks(a), ranks(b)
	n := float64(len(a))
	d2 := 0.0
	for i := range ra {
		d2 += (ra[i] - rb[i]) * (ra[i] - rb[i])
	}
	return 1 - 6*d2/(n*(n*n-1))
}

var (
	nowFn   = time.Now
	sinceFn = time.Since
)
