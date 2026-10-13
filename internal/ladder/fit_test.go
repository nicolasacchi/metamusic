package ladder

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

func TestFit_Validation(t *testing.T) {
	t.Parallel()
	tie := Comparison{Winner: 0, Loser: 1, Tie: true}
	tests := []struct {
		name string
		n    int
		cmps []Comparison
		o    Options
	}{
		{"winner==loser", 2, []Comparison{{Winner: 1, Loser: 1}}, Options{}},
		{"loser out of range", 2, []Comparison{{Winner: 0, Loser: 2}}, Options{}},
		{"winner out of range", 2, []Comparison{{Winner: 5, Loser: 0}}, Options{}},
		{"negative winner", 2, []Comparison{{Winner: -2, Loser: 0}}, Options{}},
		{"anchor as loser", 2, []Comparison{{Winner: 0, Loser: Anchor}}, Options{}},
		{"anchor in a tie", 2, []Comparison{{Winner: Anchor, Loser: 0, Tie: true}}, Options{}},
		{"fixed nu 0 with tie", 2, []Comparison{tie}, Options{FixedNu: fixedNu(0)}},
		{"negative n", -1, nil, Options{}},
		{"negative option", 2, nil, Options{PriorSD: -1}},
		{"negative fixed nu", 2, nil, Options{FixedNu: fixedNu(-1)}},
		{"nan fixed nu", 2, nil, Options{FixedNu: fixedNu(math.NaN())}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := FitBTD(tc.n, tc.cmps, tc.o); err == nil {
				t.Fatal("want error")
			}
		})
	}
	f, err := FitBTD(0, nil, Options{})
	if err != nil || len(f.Theta) != 0 || len(f.SD) != 0 {
		t.Fatalf("n=0: %+v %v", f, err)
	}
}

func TestFit_EmptyIsPrior(t *testing.T) {
	t.Parallel()
	f, err := FitBTD(3, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if f.Iterations != 0 || !f.Converged {
		t.Fatalf("iterations=%d converged=%v", f.Iterations, f.Converged)
	}
	for i := range f.Theta {
		if f.Theta[i] != 0 || f.SD[i] != 1.5 {
			t.Fatalf("item %d: %v %v", i, f.Theta[i], f.SD[i])
		}
	}
	f, _ = FitBTD(2, nil, Options{FixedNu: fixedNu(0.7)})
	near(t, "fixed nu", f.Nu, 0.7, 0)
}

func TestFit_SingleWinFixedNu0(t *testing.T) {
	t.Parallel()
	f, err := FitBTD(2, []Comparison{{Winner: 0, Loser: 1}}, Options{FixedNu: fixedNu(0)})
	if err != nil || !f.Converged {
		t.Fatal(err, f.Converged)
	}
	near(t, "theta0", f.Theta[0], 0.556474, 1e-4)
	near(t, "theta1", f.Theta[1], -0.556474, 1e-4)
	near(t, "sd0", f.SD[0], 1.318022, 1e-4)
	near(t, "sd1", f.SD[1], 1.318022, 1e-4)
}

func TestFit_AllTies(t *testing.T) {
	t.Parallel()
	c := Comparison{Winner: 0, Loser: 1, Tie: true}
	f, err := FitBTD(2, []Comparison{c, c, c}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	near(t, "theta0", f.Theta[0], 0, 1e-6)
	near(t, "theta1", f.Theta[1], 0, 1e-6)
	near(t, "nu", f.Nu, (-3+math.Sqrt(73))/4, 1e-4)
}

func TestLadder_BothBadAnchor(t *testing.T) {
	t.Parallel()
	f, err := FitBTD(2, []Comparison{{Winner: Anchor, Loser: 0}, {Winner: Anchor, Loser: 1}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	near(t, "theta0", f.Theta[0], -0.731130, 1e-4)
	near(t, "theta1", f.Theta[1], -0.731130, 1e-4)
	near(t, "sd0", f.SD[0], 1.227386, 1e-4)
	near(t, "sd1", f.SD[1], 1.227386, 1e-4)
	// nu is unaffected by anchor pairs: it equals its prior-only estimate.
	near(t, "nu", f.Nu, 0.5, 1e-9)
}

func TestFit_BothBadLowersBoth(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(7, 7))
	for k := 0; k < 50; k++ {
		n, cmps := randomProblem(r, true, true)
		base, err := FitBTD(n, cmps, Options{})
		if err != nil {
			t.Fatal(err)
		}
		i := r.IntN(n)
		j := r.IntN(n - 1)
		if j >= i {
			j++
		}
		more := append(slices.Clone(cmps), Comparison{Winner: Anchor, Loser: i}, Comparison{Winner: Anchor, Loser: j})
		got, err := FitBTD(n, more, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if !(got.Theta[i] < base.Theta[i]) || !(got.Theta[j] < base.Theta[j]) {
			t.Fatalf("problem %d: theta_i %v -> %v, theta_j %v -> %v", k, base.Theta[i], got.Theta[i], base.Theta[j], got.Theta[j])
		}
	}
}

func TestFit_MonotoneLogPosterior(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(11, 3))
	for k := 0; k < 50; k++ {
		n, cmps := randomProblem(r, true, true)
		prev := math.Inf(-1)
		for it := 1; it <= 40; it++ {
			f, err := FitBTD(n, cmps, Options{MaxIter: it, Tol: 1e-300})
			if err != nil {
				t.Fatal(err)
			}
			if f.LogPosterior < prev-1e-12*math.Abs(prev) {
				t.Fatalf("problem %d iteration %d: LP fell %v -> %v", k, it, prev, f.LogPosterior)
			}
			prev = f.LogPosterior
		}
	}
}

func TestFit_StationaryPoint(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(5, 9))
	o := Options{Tol: 1e-13}
	for k := 0; k < 10; k++ {
		n, cmps := randomProblem(r, true, true)
		f, err := FitBTD(n, cmps, o)
		if err != nil || !f.Converged {
			t.Fatalf("problem %d: %v converged=%v", k, err, f.Converged)
		}
		const h = 1e-5
		lp := func(th []float64, nu float64) float64 { return LogPosterior(n, cmps, th, nu, o) }
		for i := 0; i < n; i++ {
			up, dn := slices.Clone(f.Theta), slices.Clone(f.Theta)
			up[i] += h
			dn[i] -= h
			if g := (lp(up, f.Nu) - lp(dn, f.Nu)) / (2 * h); math.Abs(g) > 1e-6 {
				t.Errorf("problem %d: dLP/dtheta_%d = %g", k, i, g)
			}
		}
		if g := (lp(f.Theta, f.Nu+h) - lp(f.Theta, f.Nu-h)) / (2 * h); math.Abs(g) > 1e-6 {
			t.Errorf("problem %d: dLP/dnu = %g", k, g)
		}
	}
}

func TestLaplace_MatchesNumericHessian(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(21, 4))
	o := Options{Tol: 1e-12}
	for k := 0; k < 8; k++ {
		n, cmps := randomProblem(r, true, true)
		f, err := FitBTD(n, cmps, o)
		if err != nil {
			t.Fatal(err)
		}
		// Items without comparisons keep the prior and are excluded from the Hessian.
		p, _ := build(n, cmps, o.withDefaults())
		var items []int
		for i := 0; i < n; i++ {
			if p.has[i] {
				items = append(items, i)
			}
		}
		m := len(items)
		H := make([][]float64, m)
		const h = 1e-4
		lp := func(th []float64) float64 { return LogPosterior(n, cmps, th, f.Nu, o) }
		for a := range H {
			H[a] = make([]float64, m)
			for b := range H[a] {
				at := func(da, db float64) float64 {
					th := slices.Clone(f.Theta)
					th[items[a]] += da
					th[items[b]] += db
					return lp(th)
				}
				H[a][b] = -(at(h, h) - at(h, -h) - at(-h, h) + at(-h, -h)) / (4 * h * h)
			}
		}
		inv := invert(H)
		for a, i := range items {
			want := math.Sqrt(inv[a][a])
			if math.Abs(f.SD[i]-want)/want > 1e-3 {
				t.Errorf("problem %d item %d: SD %v vs numeric %v", k, i, f.SD[i], want)
			}
		}
	}
}

// invert is a plain Gauss-Jordan inverse used only as an independent reference.
func invert(a [][]float64) [][]float64 {
	m := len(a)
	x := make([][]float64, m)
	for i := range x {
		x[i] = make([]float64, 2*m)
		copy(x[i], a[i])
		x[i][m+i] = 1
	}
	for c := 0; c < m; c++ {
		piv := c
		for r := c + 1; r < m; r++ {
			if math.Abs(x[r][c]) > math.Abs(x[piv][c]) {
				piv = r
			}
		}
		x[c], x[piv] = x[piv], x[c]
		d := x[c][c]
		for j := range x[c] {
			x[c][j] /= d
		}
		for r := 0; r < m; r++ {
			if r == c {
				continue
			}
			f := x[r][c]
			for j := range x[r] {
				x[r][j] -= f * x[c][j]
			}
		}
	}
	out := make([][]float64, m)
	for i := range out {
		out[i] = x[i][m:]
	}
	return out
}

func TestFit_PermutationInvariant(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(2, 2))
	n, cmps := randomProblem(r, true, true)
	base, _ := FitBTD(n, cmps, Options{})
	perm := r.Perm(n)
	pc := make([]Comparison, len(cmps))
	for i, c := range cmps {
		w := c.Winner
		if w != Anchor {
			w = perm[w]
		}
		pc[i] = Comparison{Winner: w, Loser: perm[c.Loser], Tie: c.Tie}
	}
	r.Shuffle(len(pc), func(i, j int) { pc[i], pc[j] = pc[j], pc[i] })
	got, _ := FitBTD(n, pc, Options{})
	near(t, "nu", got.Nu, base.Nu, 1e-6)
	for i := 0; i < n; i++ {
		near(t, "theta", got.Theta[perm[i]], base.Theta[i], 1e-6)
		near(t, "sd", got.SD[perm[i]], base.SD[i], 1e-6)
	}
}

func TestFit_DisconnectedComponents(t *testing.T) {
	t.Parallel()
	o := Options{FixedNu: fixedNu(0.4)}
	a := []Comparison{{0, 1, false}, {0, 1, false}, {1, 0, false}, {0, 1, true}}
	b := []Comparison{{0, 1, false}, {2, 1, false}, {Anchor, 2, false}}
	shift := func(cs []Comparison, d int) []Comparison {
		var out []Comparison
		for _, c := range cs {
			if c.Winner != Anchor {
				c.Winner += d
			}
			c.Loser += d
			out = append(out, c)
		}
		return out
	}
	fa, _ := FitBTD(2, a, o)
	fb, _ := FitBTD(3, b, o)
	both, err := FitBTD(5, append(slices.Clone(a), shift(b, 2)...), o)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		near(t, "a theta", both.Theta[i], fa.Theta[i], 1e-6)
		near(t, "a sd", both.SD[i], fa.SD[i], 1e-6)
	}
	for i := 0; i < 3; i++ {
		near(t, "b theta", both.Theta[2+i], fb.Theta[i], 1e-6)
		near(t, "b sd", both.SD[2+i], fb.SD[i], 1e-6)
	}
}

func TestFit_Undefeated(t *testing.T) {
	t.Parallel()
	var cmps []Comparison
	for k := 0; k < 10; k++ {
		cmps = append(cmps, Comparison{Winner: 0, Loser: 1})
	}
	f, err := FitBTD(2, cmps, Options{})
	if err != nil || !f.Converged {
		t.Fatal(err, f.Converged)
	}
	if !(f.Theta[0] > 0 && f.Theta[0] < 3) || math.IsNaN(f.SD[0]) {
		t.Fatalf("theta0=%v sd0=%v", f.Theta[0], f.SD[0])
	}
}

func TestFit_Deterministic(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(8, 8))
	n, cmps := randomProblem(r, true, true)
	a, _ := FitBTD(n, cmps, Options{})
	b, _ := FitBTD(n, cmps, Options{})
	if !slices.Equal(a.Theta, b.Theta) || !slices.Equal(a.SD, b.SD) || a.Nu != b.Nu || a.Iterations != b.Iterations {
		t.Fatal("fits differ bit for bit")
	}
}

func TestFit_NotConverged(t *testing.T) {
	t.Parallel()
	f, err := FitBTD(2, []Comparison{{Winner: 0, Loser: 1}}, Options{MaxIter: 1, Tol: 1e-300})
	if err != nil || f.Converged || f.Iterations != 1 {
		t.Fatalf("%+v %v", f, err)
	}
}

func TestFit_ZeroNuPriorModeAndLargeCounts(t *testing.T) {
	t.Parallel()
	// NuShape=1 makes the prior mode 0: the start must stay off the boundary.
	f, err := FitBTD(2, nil, Options{NuShape: 1})
	if err != nil || f.Nu <= 0 {
		t.Fatalf("%+v %v", f, err)
	}
	// Huge win counts exercise the bracketed Newton fallback.
	var cmps []Comparison
	for k := 0; k < 500; k++ {
		cmps = append(cmps, Comparison{Winner: 0, Loser: 1})
	}
	g, err := FitBTD(2, cmps, Options{FixedNu: fixedNu(0)})
	if err != nil || math.IsNaN(g.Theta[0]) || math.IsInf(g.Theta[0], 0) {
		t.Fatalf("%+v %v", g, err)
	}
}

func TestPBeats(t *testing.T) {
	t.Parallel()
	w, ti, l := PBeats(1.3, -0.2, 0)
	near(t, "tie at nu=0", ti, 0, 0)
	near(t, "logistic", w, 1/(1+math.Exp(-1.5)), 1e-12)
	near(t, "sum", w+ti+l, 1, 1e-12)
	for _, c := range [][3]float64{{0.3, -2, 0.7}, {5, 5, 2}, {-40, 40, 0.1}, {800, -800, 1}} {
		w, ti, l := PBeats(c[0], c[1], c[2])
		near(t, "sum", w+ti+l, 1, 1e-12)
		w2, t2, l2 := PBeats(c[1], c[0], c[2])
		near(t, "win/loss symmetry", w, l2, 1e-12)
		near(t, "tie symmetry", ti, t2, 1e-12)
		near(t, "loss/win symmetry", l, w2, 1e-12)
	}
	w, ti, _ = PBeats(0, 0, 1)
	near(t, "equal items", w, 1.0/3, 1e-12)
	near(t, "equal tie", ti, 1.0/3, 1e-12)
}

func TestFit_TimeBudget(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing test")
	}
	t.Parallel()
	n, cmps := budgetProblem()
	start := nowFn()
	f, err := FitBTD(n, cmps, Options{})
	if err != nil || !f.Converged {
		t.Fatal(err, f.Converged)
	}
	if d := sinceFn(start); d.Seconds() >= 2 {
		t.Fatalf("fit took %v", d)
	}
}

func budgetProblem() (int, []Comparison) {
	r := rand.New(rand.NewPCG(1, 1))
	const n = 500
	truth := make([]float64, n)
	for i := range truth {
		truth[i] = r.NormFloat64()
	}
	var cmps []Comparison
	for k := 0; k < 5000; k++ {
		i := r.IntN(n)
		j := r.IntN(n - 1)
		if j >= i {
			j++
		}
		w, ti, _ := PBeats(truth[i], truth[j], 0.3)
		switch x := r.Float64(); {
		case x < w:
			cmps = append(cmps, Comparison{Winner: i, Loser: j})
		case x < w+ti:
			cmps = append(cmps, Comparison{Winner: i, Loser: j, Tie: true})
		default:
			cmps = append(cmps, Comparison{Winner: j, Loser: i})
		}
	}
	return n, cmps
}

func BenchmarkFit_500x5000(b *testing.B) {
	n, cmps := budgetProblem()
	b.ResetTimer()
	for b.Loop() {
		if _, err := FitBTD(n, cmps, Options{}); err != nil {
			b.Fatal(err)
		}
	}
}
