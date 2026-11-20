package abc

import "math"

// Rat is an exact fraction, reduced, with Den > 0. Durations and positions are whole-note fractions.
//
// The methods do plain int64 arithmetic that returns 0/1 on overflow. Parse refuses (bad_length) a tune whose bar
// lengths, cumulative bar starts or note ends do not fit, so sums of positions taken from one Tune stay in range.
type Rat struct{ Num, Den int64 }

func gcd(a, b int64) int64 {
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// R builds a reduced Rat (a zero or negative denominator yields 0/1).
func R(n, d int64) Rat {
	if d <= 0 {
		return Rat{0, 1}
	}
	g := gcd(n, d)
	if g > 1 {
		n, d = n/g, d/g
	}
	return Rat{n, d}
}

func (r Rat) norm() Rat {
	if r.Den == 0 {
		return Rat{0, 1}
	}
	return r
}

func mulOK(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	c := a * b
	if c/b != a || (a == math.MinInt64 || b == math.MinInt64) {
		return 0, false
	}
	return c, true
}

func addOK(a, b int64) (int64, bool) {
	c := a + b
	if (c > a) != (b > 0) {
		return 0, false
	}
	return c, true
}

// mulc and addc are the overflow-checked operations the parser uses.
func mulc(a, b Rat) (Rat, bool) {
	a, b = a.norm(), b.norm()
	g1, g2 := gcd(a.Num, b.Den), gcd(b.Num, a.Den)
	if g1 == 0 {
		g1 = 1
	}
	if g2 == 0 {
		g2 = 1
	}
	n, ok1 := mulOK(a.Num/g1, b.Num/g2)
	d, ok2 := mulOK(a.Den/g2, b.Den/g1)
	if !ok1 || !ok2 {
		return Rat{0, 1}, false
	}
	return R(n, d), true
}

func addc(a, b Rat) (Rat, bool) {
	a, b = a.norm(), b.norm()
	g := gcd(a.Den, b.Den)
	l, ok := mulOK(a.Den/g, b.Den)
	if !ok {
		return Rat{0, 1}, false
	}
	x, ok1 := mulOK(a.Num, l/a.Den)
	y, ok2 := mulOK(b.Num, l/b.Den)
	if !ok1 || !ok2 {
		return Rat{0, 1}, false
	}
	n, ok3 := addOK(x, y)
	if !ok3 {
		return Rat{0, 1}, false
	}
	return R(n, l), true
}

// Add returns r + o.
func (r Rat) Add(o Rat) Rat { x, _ := addc(r, o); return x }

// Sub returns r - o.
func (r Rat) Sub(o Rat) Rat { x, _ := addc(r, Rat{-o.norm().Num, o.norm().Den}); return x }

// Mul returns r * o.
func (r Rat) Mul(o Rat) Rat { x, _ := mulc(r, o); return x }

// Cmp compares r with o: -1, 0 or +1.
func (r Rat) Cmp(o Rat) int {
	r, o = r.norm(), o.norm()
	// compare r.Num*o.Den with o.Num*r.Den through float128-free big-ish path: both products fit for parsed values;
	// fall back to float on overflow
	a, ok1 := mulOK(r.Num, o.Den)
	b, ok2 := mulOK(o.Num, r.Den)
	if !ok1 || !ok2 {
		fa, fb := r.Float(), o.Float()
		switch {
		case fa < fb:
			return -1
		case fa > fb:
			return 1
		}
		return 0
	}
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// IsZero reports r == 0.
func (r Rat) IsZero() bool { return r.Num == 0 }

// Float returns r as a float64.
func (r Rat) Float() float64 { r = r.norm(); return float64(r.Num) / float64(r.Den) }

// ticksAt converts a whole-note position to ticks at ppq per quarter, rounding half up (positions are absolute, so
// rounding never accumulates).
func (r Rat) ticks(ppq int64) int64 {
	r = r.norm()
	n, ok := mulOK(r.Num, 4*ppq)
	if !ok {
		return int64(math.Round(r.Float() * float64(4*ppq)))
	}
	q := n / r.Den
	rem := n % r.Den
	if rem < 0 {
		rem += r.Den
		q--
	}
	if 2*rem >= r.Den {
		q++
	}
	return q
}
