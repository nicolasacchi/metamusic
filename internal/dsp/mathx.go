package dsp

import "math"

// Deterministic math (D-T1-3a). Go's amd64 math.Exp is assembly that selects an FMA path at run time, so two amd64 hosts
// can differ by an ulp; the dsp tree therefore uses its own Exp, Log and Pow: range reduction plus fixed polynomials
// (the FreeBSD msun algorithms), every product written float64(a*b) so no compiler may fuse it into a multiply-add.
// math.Ldexp, Frexp, Floor, Sqrt and the bit helpers are exact and stay allowed.

const (
	ln2Hi  = 6.93147180369123816490e-01
	ln2Lo  = 1.90821492927058770002e-10
	ln2    = ln2Hi + ln2Lo
	invLn2 = 1.44269504088896338700e+00

	expOverflow  = 7.09782712893383973096e+02
	expUnderflow = -7.45133219101941108420e+02

	// polynomial coefficients of exp(r) on |r| <= ln2/2 and of log(1+f)
	exP1 = 1.66666666666666657415e-01
	exP2 = -2.77777777770155933842e-03
	exP3 = 6.61375632143793436117e-05
	exP4 = -1.65339022054652515390e-06
	exP5 = 4.13813679705723846039e-08

	lgL1 = 6.666666666666735130e-01
	lgL2 = 3.999999999940941908e-01
	lgL3 = 2.857142874366239149e-01
	lgL4 = 2.222219843214978396e-01
	lgL5 = 1.818357216161805012e-01
	lgL6 = 1.531383769920937332e-01
	lgL7 = 1.479819860511658591e-01
)

// Exp is e**x to within 1 ulp.
func Exp(x float64) float64 {
	switch {
	case math.IsNaN(x) || math.IsInf(x, 1):
		return x
	case math.IsInf(x, -1):
		return 0
	case x > expOverflow:
		return math.Inf(1)
	case x < expUnderflow:
		return 0
	case -3.725290298461915e-09 < x && x < 3.725290298461915e-09:
		return 1 + x
	}
	var k int
	switch {
	case x < 0:
		k = int(float64(invLn2*x) - 0.5)
	case x > 0:
		k = int(float64(invLn2*x) + 0.5)
	}
	hi := x - float64(float64(k)*ln2Hi)
	lo := float64(float64(k) * ln2Lo)
	return expmulti(hi, lo, k)
}

// expmulti is exp(hi-lo) * 2**k.
func expmulti(hi, lo float64, k int) float64 {
	r := hi - lo
	t := float64(r * r)
	c := r - float64(t*(exP1+float64(t*(exP2+float64(t*(exP3+float64(t*(exP4+float64(t*exP5)))))))))
	y := 1 - ((lo - float64(float64(r*c)/(2-c))) - hi)
	return math.Ldexp(y, k)
}

// Log is the natural logarithm to within 1 ulp.
func Log(x float64) float64 {
	switch {
	case math.IsNaN(x) || math.IsInf(x, 1):
		return x
	case x < 0:
		return math.NaN()
	case x == 0:
		return math.Inf(-1)
	}
	f, k, s, hfsq, r := logParts(x)
	kf := float64(k)
	return float64(kf*ln2Hi) - ((hfsq - (float64(s*(hfsq+r)) + float64(kf*ln2Lo))) - f)
}

// logParts reduces x = 2**k * (1+f) with 1+f in [sqrt(2)/2, sqrt(2)) and evaluates the series pieces of log(1+f).
func logParts(x float64) (f float64, k int, s, hfsq, r float64) {
	f1, ki := math.Frexp(x)
	if f1 < math.Sqrt2/2 {
		f1 *= 2
		ki--
	}
	f = f1 - 1
	s = f / (2 + f)
	s2 := float64(s * s)
	s4 := float64(s2 * s2)
	t1 := float64(s2 * (lgL1 + float64(s4*(lgL3+float64(s4*(lgL5+float64(s4*lgL7)))))))
	t2 := float64(s4 * (lgL2 + float64(s4*(lgL4+float64(s4*lgL6)))))
	r = t1 + t2
	hfsq = float64(0.5 * float64(f*f))
	return f, ki, s, hfsq, r
}

// twoSum is the exact sum a+b as hi+lo.
func twoSum(a, b float64) (hi, lo float64) {
	hi = a + b
	bb := hi - a
	lo = (a - (hi - bb)) + (b - bb)
	return hi, lo
}

// split is the Veltkamp split of a into two 26-bit halves.
func split(a float64) (hi, lo float64) {
	t := float64(134217729 * a) // 2**27 + 1
	hi = t - (t - a)
	return hi, a - hi
}

// twoProd is the exact product a*b as hi+lo (Dekker), without a fused multiply-add.
func twoProd(a, b float64) (hi, lo float64) {
	hi = float64(a * b)
	ah, al := split(a)
	bh, bl := split(b)
	lo = float64(al*bl) - (((hi - float64(ah*bh)) - float64(al*bh)) - float64(ah*bl))
	return hi, lo
}

// logDD is log(x) for finite x > 0 as an unevaluated sum hi+lo, accurate to about 1e-19 relative.
func logDD(x float64) (hi, lo float64) {
	f, k, s, hfsq, r := logParts(x)
	kf := float64(k)
	a := float64(kf * ln2Hi) // exact: ln2Hi has trailing zero bits
	// log(1+f) = f - hfsq + s*(hfsq+r); hfsq = f*f/2 exactly as hh+hl
	hh, hl := twoProd(f, f)
	hh *= 0.5
	hl *= 0.5
	hi, e1 := twoSum(a, f)
	hi, e2 := twoSum(hi, -hh)
	lo = ((e1 + e2) - hl) + (float64(s*(hfsq+r)) + float64(kf*ln2Lo))
	hi, lo = twoSum(hi, lo)
	return hi, lo
}

// isInt reports whether y is an integer, and whether it is odd.
func isInt(y float64) (integer, odd bool) {
	if math.Abs(y) >= 1<<53 {
		return true, false
	}
	t := math.Floor(y)
	if t != y {
		return false, false
	}
	return true, math.Mod(t, 2) != 0
}

// Pow is x**y. Small integer exponents use repeated multiplication; otherwise exp(y*log(x)) with log carried in
// double-double so the result is within 2 ulp for moderate arguments.
func Pow(x, y float64) float64 {
	switch {
	case y == 0 || x == 1:
		return 1
	case math.IsNaN(x) || math.IsNaN(y):
		return math.NaN()
	case y == 1:
		return x
	}
	yInt, yOdd := isInt(y)
	switch {
	case x == 0:
		neg := math.Signbit(x) && yOdd
		if y < 0 {
			if neg {
				return math.Inf(-1)
			}
			return math.Inf(1)
		}
		if neg {
			return x
		}
		return 0
	case math.IsInf(y, 0):
		ax := math.Abs(x)
		switch {
		case ax == 1:
			return 1
		case (ax < 1) == math.IsInf(y, 1):
			return 0
		}
		return math.Inf(1)
	case math.IsInf(x, 0):
		if math.IsInf(x, -1) {
			return Pow(1/x, -y) // Pow(-Inf, y) = Pow(-0, -y)
		}
		if y < 0 {
			return 0
		}
		return math.Inf(1)
	}
	if x < 0 && !yInt {
		return math.NaN()
	}
	if yInt && math.Abs(y) <= 4 {
		n := int(math.Abs(y))
		p := 1.0
		for i := 0; i < n; i++ {
			p = float64(p * x)
		}
		if y < 0 {
			return 1 / p
		}
		return p
	}
	ax := math.Abs(x)
	lh, ll := logDD(ax)
	th, tl := twoProd(y, lh)
	tl += float64(y * ll)
	th, tl = twoSum(th, tl)
	var r float64
	switch {
	case th > expOverflow:
		r = math.Inf(1)
	case th < expUnderflow:
		r = 0
	default:
		e := Exp(th)
		r = e + float64(e*tl) // exp(th+tl) = exp(th) * (1+tl), |tl| <= 1e-16*|th|
	}
	if x < 0 && yOdd {
		return -r
	}
	return r
}
