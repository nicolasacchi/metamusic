package ladder

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

const (
	defPriorSD = 1.5
	defNuShape = 2.0
	defNuRate  = 2.0
	defTol     = 1e-9
	defMaxIter = 50000
)

func (o Options) withDefaults() Options {
	if o.PriorSD == 0 {
		o.PriorSD = defPriorSD
	}
	if o.NuShape == 0 {
		o.NuShape = defNuShape
	}
	if o.NuRate == 0 {
		o.NuRate = defNuRate
	}
	if o.Tol == 0 {
		o.Tol = defTol
	}
	if o.MaxIter == 0 {
		o.MaxIter = defMaxIter
	}
	return o
}

// pairAgg aggregates the comparisons of one unordered pair i<j.
type pairAgg struct {
	i, j     int
	wij, wji float64 // wins of i over j, of j over i
	ties     float64
}

func (p pairAgg) n() float64 { return p.wij + p.wji + p.ties }

// problem is the aggregated, validated input of a fit.
type problem struct {
	n     int
	pairs []pairAgg
	nAnch []float64 // anchor losses per item (both_bad)
	win   []float64 // A_i = wins + ties/2 against real items
	has   []bool    // item has at least one comparison
	ties  float64   // total ties T
}

func build(n int, cmps []Comparison, o Options) (*problem, error) {
	if n < 0 {
		return nil, errors.New("ladder: negative item count")
	}
	if o.PriorSD < 0 || o.NuShape < 0 || o.NuRate < 0 || o.Tol < 0 || o.MaxIter < 0 {
		return nil, errors.New("ladder: negative option")
	}
	if o.FixedNu != nil && (*o.FixedNu < 0 || math.IsNaN(*o.FixedNu) || math.IsInf(*o.FixedNu, 0)) {
		return nil, errors.New("ladder: invalid FixedNu")
	}
	p := &problem{n: n, nAnch: make([]float64, n), win: make([]float64, n), has: make([]bool, n)}
	idx := map[[2]int]int{}
	for k, c := range cmps {
		if c.Loser < 0 || c.Loser >= n {
			return nil, fmt.Errorf("ladder: comparison %d: loser index %d out of range (or anchor as loser)", k, c.Loser)
		}
		if c.Winner != Anchor && (c.Winner < 0 || c.Winner >= n) {
			return nil, fmt.Errorf("ladder: comparison %d: winner index %d out of range", k, c.Winner)
		}
		if c.Winner == c.Loser {
			return nil, fmt.Errorf("ladder: comparison %d: winner equals loser", k)
		}
		if c.Winner == Anchor {
			if c.Tie {
				return nil, fmt.Errorf("ladder: comparison %d: anchor in a tie", k)
			}
			p.nAnch[c.Loser]++
			p.has[c.Loser] = true
			continue
		}
		if c.Tie && o.FixedNu != nil && *o.FixedNu == 0 {
			return nil, fmt.Errorf("ladder: comparison %d: tie with FixedNu=0", k)
		}
		a, b := c.Winner, c.Loser
		aWon := true
		if a > b {
			a, b = b, a
			aWon = false
		}
		key := [2]int{a, b}
		pi, ok := idx[key]
		if !ok {
			pi = len(p.pairs)
			idx[key] = pi
			p.pairs = append(p.pairs, pairAgg{i: a, j: b})
		}
		pr := &p.pairs[pi]
		switch {
		case c.Tie:
			pr.ties++
			p.ties++
			p.win[c.Winner] += 0.5
			p.win[c.Loser] += 0.5
		case aWon:
			pr.wij++
			p.win[c.Winner]++
		default:
			pr.wji++
			p.win[c.Winner]++
		}
		p.has[c.Winner], p.has[c.Loser] = true, true
	}
	sort.Slice(p.pairs, func(x, y int) bool {
		if p.pairs[x].i != p.pairs[y].i {
			return p.pairs[x].i < p.pairs[y].i
		}
		return p.pairs[x].j < p.pairs[y].j
	})
	return p, nil
}

// FitBTD fits the model by MM iterations (C47).
func FitBTD(n int, cmps []Comparison, o Options) (Fit, error) {
	o = o.withDefaults()
	p, err := build(n, cmps, o)
	if err != nil {
		return Fit{}, err
	}
	f := Fit{Theta: make([]float64, n), SD: make([]float64, n)}
	for i := range f.SD {
		f.SD[i] = o.PriorSD
	}
	nu := (o.NuShape - 1) / o.NuRate
	if o.FixedNu != nil {
		nu = *o.FixedNu
	} else if nu <= 0 {
		nu = 1e-3 // improper prior mode; start away from the boundary
	}
	if len(cmps) == 0 {
		f.Nu = nu
		f.Converged = true
		f.LogPosterior = LogPosterior(n, cmps, f.Theta, nu, o)
		return f, nil
	}
	if o.FixedNu == nil {
		nu = 1 // start of the iteration
	}
	theta := f.Theta
	pi := make([]float64, n)
	bsum := make([]float64, n)
	prev := make([]float64, n)
	for it := 1; it <= o.MaxIter; it++ {
		copy(prev, theta)
		for i := range pi {
			pi[i] = math.Exp(theta[i])
			bsum[i] = 0
		}
		for _, pr := range p.pairs {
			c := pr.n()
			s := math.Sqrt(pi[pr.i] * pi[pr.j])
			d := pi[pr.i] + pi[pr.j] + nu*s
			bsum[pr.i] += c * (1 + nu/2*math.Sqrt(pi[pr.j]/pi[pr.i])) / d
			bsum[pr.j] += c * (1 + nu/2*math.Sqrt(pi[pr.i]/pi[pr.j])) / d
		}
		maxd := 0.0
		for i := 0; i < n; i++ {
			if !p.has[i] {
				continue
			}
			b := bsum[i] + p.nAnch[i]/(1+pi[i])
			theta[i] = solveTheta(p.win[i], b, o.PriorSD, prev[i])
			maxd = math.Max(maxd, math.Abs(theta[i]-prev[i]))
		}
		dnu := 0.0
		if o.FixedNu == nil {
			// Recompute pi at theta^{k+1}.
			s := 0.0
			for _, pr := range p.pairs {
				a, b := math.Exp(theta[pr.i]), math.Exp(theta[pr.j])
				g := math.Sqrt(a * b)
				s += pr.n() * g / (a + b + nu*g)
			}
			next := (p.ties + o.NuShape - 1) / (s + o.NuRate)
			dnu = math.Abs(next - nu)
			nu = next
		}
		f.Iterations = it
		if maxd < o.Tol && dnu < o.Tol {
			f.Converged = true
			break
		}
	}
	f.Nu = nu
	f.LogPosterior = LogPosterior(n, cmps, theta, nu, o)
	laplaceSD(p, theta, nu, o.PriorSD, f.SD)
	return f, nil
}

// solveTheta maximises a*t - b*e^t - t^2/(2 s^2): the root of h(t) = a - b e^t - t/s^2.
// h is strictly decreasing, so the root is bracketed by [-b s^2, a s^2]; Newton steps
// that leave the bracket fall back to bisection.
func solveTheta(a, b, s, start float64) float64 {
	s2 := s * s
	lo, hi := -b*s2, a*s2
	t := math.Min(math.Max(start, lo), hi)
	for step := 0; step < 100; step++ {
		e := b * math.Exp(t)
		h := a - e - t/s2
		if h > 0 {
			lo = t
		} else {
			hi = t
		}
		nt := t + h/(e+1/s2)
		if !(nt > lo && nt < hi) {
			nt = (lo + hi) / 2
		}
		if math.Abs(nt-t) < 1e-13 {
			return nt
		}
		t = nt
	}
	return t
}

// LogPosterior evaluates the log posterior up to constants.
func LogPosterior(n int, cmps []Comparison, theta []float64, nu float64, o Options) float64 {
	o = o.withDefaults()
	lp := 0.0
	for _, c := range cmps {
		if c.Winner == Anchor {
			lp -= math.Log1p(math.Exp(theta[c.Loser]))
			continue
		}
		a, b := theta[c.Winner], theta[c.Loser]
		g := (a + b) / 2
		d := math.Exp(a) + math.Exp(b) + nu*math.Exp(g)
		if c.Tie {
			lp += g
			if o.FixedNu == nil {
				lp += math.Log(nu)
			}
		} else {
			lp += a
		}
		lp -= math.Log(d)
	}
	for i := 0; i < n; i++ {
		lp -= theta[i] * theta[i] / (2 * o.PriorSD * o.PriorSD)
	}
	if o.FixedNu == nil {
		lp += (o.NuShape-1)*math.Log(nu) - o.NuRate*nu
	}
	return lp
}
