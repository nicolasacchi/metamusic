package dsp_test

import (
	"bufio"
	"flag"
	"fmt"
	"math"
	"math/big"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
)

var update = flag.Bool("update", false, "rewrite the golden files under testdata")

// sameSpecial reports whether got and want are equal as special values (NaN, infinities, signed zeros).
func sameSpecial(got, want float64) bool {
	switch {
	case math.IsNaN(want):
		return math.IsNaN(got)
	case math.IsInf(want, 0), want == 0:
		return got == want && math.Signbit(got) == math.Signbit(want)
	}
	return false
}

const refPrec = 320

// bigExp is e**x in 320-bit arithmetic: argument halving by 2^-24, Taylor series, then 24 squarings.
func bigExp(x float64) *big.Float {
	r := new(big.Float).SetPrec(refPrec).SetFloat64(x)
	return bigExpF(r)
}

func bigExpF(r *big.Float) *big.Float {
	const halvings = 24
	r = new(big.Float).SetPrec(refPrec).SetMantExp(r, -halvings)
	sum := new(big.Float).SetPrec(refPrec).SetInt64(1)
	term := new(big.Float).SetPrec(refPrec).SetInt64(1)
	for k := int64(1); k < 26; k++ {
		term.Mul(term, r)
		term.Quo(term, new(big.Float).SetPrec(refPrec).SetInt64(k))
		sum.Add(sum, term)
	}
	for range halvings {
		sum.Mul(sum, sum)
	}
	return sum
}

// bigLog is ln(x) for x > 0: Halley iterations on the exact exponential, seeded by the float result.
func bigLog(x float64) *big.Float {
	bx := new(big.Float).SetPrec(refPrec).SetFloat64(x)
	l := new(big.Float).SetPrec(refPrec).SetFloat64(dsp.Log(x))
	for range 2 {
		e := bigExpF(l)
		num := new(big.Float).SetPrec(refPrec).Sub(bx, e)
		den := new(big.Float).SetPrec(refPrec).Add(bx, e)
		num.Quo(num, den)
		num.Mul(num, new(big.Float).SetPrec(refPrec).SetInt64(2))
		l.Add(l, num)
	}
	return l
}

func bigToFloat(f *big.Float) float64 { v, _ := f.Float64(); return v }

// The references are exact (320-bit), not math: Go's amd64 math.Exp overflows early near 709.7, its assembly Log mishandles
// denormals and its pure-Go Pow carries several ulp of error.
func TestExp_Accuracy(t *testing.T) {
	t.Parallel()
	r := dsp.NewRand(1)
	var worst uint64
	for range 4000 {
		x := (r.Float64() - 0.5) * 200 // [-100, 100]
		if u := dsptest.UlpDiff(dsp.Exp(x), bigToFloat(bigExp(x))); u > worst {
			worst = u
		}
	}
	t.Logf("Exp worst error %d ulp", worst)
	if worst > 2 {
		t.Errorf("Exp worst error %d ulp, want <= 2", worst)
	}
	for _, x := range []float64{1e-20, -1e-20, 1, -1, 700, 709.7, -700, -708} {
		if u := dsptest.UlpDiff(dsp.Exp(x), bigToFloat(bigExp(x))); u > 2 {
			t.Errorf("Exp(%v) is %d ulp off", x, u)
		}
	}
	for x, want := range map[float64]float64{0: 1, math.Inf(1): math.Inf(1), math.Inf(-1): 0, 710: math.Inf(1), -746: 0, 1e6: math.Inf(1), -1e6: 0} {
		if got := dsp.Exp(x); got != want {
			t.Errorf("Exp(%v) = %v, want %v", x, got, want)
		}
	}
	if !math.IsNaN(dsp.Exp(math.NaN())) {
		t.Error("Exp(NaN) is not NaN")
	}
	// the denormal range: the nearest denormal to e^-740
	if got, want := dsp.Exp(-740), bigToFloat(bigExp(-740)); math.Abs(got-want) > 5e-324*4 {
		t.Errorf("Exp(-740) = %v, want %v", got, want)
	}
}

func TestLog_Accuracy(t *testing.T) {
	t.Parallel()
	r := dsp.NewRand(2)
	var worst uint64
	check := func(x float64) {
		if u := dsptest.UlpDiff(dsp.Log(x), bigToFloat(bigLog(x))); u > worst {
			worst = u
		}
	}
	for range 3000 {
		check(math.Exp((r.Float64() - 0.5) * 1400)) // 1e-304 .. 1e304
	}
	for range 600 {
		check(1 + (r.Float64()-0.5)*1e-3) // near 1, where cancellation bites
	}
	for _, x := range []float64{5e-324, 2.5e-310, 1e-308, math.MaxFloat64, math.E} {
		check(x) // denormals and the extremes
	}
	t.Logf("Log worst error %d ulp", worst)
	if worst > 2 {
		t.Errorf("Log worst error %d ulp, want <= 2", worst)
	}
	for x, want := range map[float64]float64{0: math.Inf(-1), math.Inf(1): math.Inf(1), 1: 0} {
		if got := dsp.Log(x); got != want {
			t.Errorf("Log(%v) = %v, want %v", x, got, want)
		}
	}
	if !math.IsNaN(dsp.Log(-1)) || !math.IsNaN(dsp.Log(math.NaN())) {
		t.Error("Log of a negative number or NaN is not NaN")
	}
}

func TestPow_Accuracy(t *testing.T) {
	t.Parallel()
	r := dsp.NewRand(3)
	var worst uint64
	var wx, wy float64
	for range 1500 { // the exact reference is costly: 1500 draws, 7000 more are checked against math at a loose bound
		x := math.Exp((r.Float64() - 0.5) * 9.2) // 0.01 .. 100
		y := (r.Float64() - 0.5) * 12
		if r.Uint64()%5 == 0 {
			y = math.Floor(y) // integer exponents too
		}
		ref := bigExpF(new(big.Float).SetPrec(refPrec).Mul(new(big.Float).SetPrec(refPrec).SetFloat64(y), bigLog(x)))
		if u := dsptest.UlpDiff(dsp.Pow(x, y), bigToFloat(ref)); u > worst {
			worst, wx, wy = u, x, y
		}
	}
	t.Logf("Pow worst error %d ulp", worst)
	if worst > 2 {
		t.Errorf("Pow worst error %d ulp at (%v, %v), want <= 2", worst, wx, wy)
	}
	for range 7000 {
		x := math.Exp((r.Float64() - 0.5) * 9.2)
		y := (r.Float64() - 0.5) * 12
		if u := dsptest.UlpDiff(dsp.Pow(x, y), math.Pow(x, y)); u > 8 {
			t.Fatalf("Pow(%v, %v) is %d ulp from math.Pow", x, y, u)
		}
	}
	inf, nan := math.Inf(1), math.NaN()
	for _, c := range [][2]float64{
		{0, 0}, {nan, 0}, {1, nan}, {nan, 1}, {-8, 3}, {-8, -3}, {-8, 2}, {-8, 0.5}, {0, 3}, {0, -3}, {math.Copysign(0, -1), 3},
		{math.Copysign(0, -1), -3}, {0, 0.5}, {0, -0.5}, {inf, 2}, {inf, -2}, {-inf, 3}, {-inf, -3}, {-inf, 2}, {2, inf}, {2, -inf},
		{0.5, inf}, {0.5, -inf}, {-1, inf}, {10, 400}, {10, -400}, {10, 1}, {3, 1e300}, {1e-3, 1e300}, {1e-3, -1e300},
	} {
		got, want := dsp.Pow(c[0], c[1]), math.Pow(c[0], c[1])
		if math.IsNaN(want) || math.IsInf(want, 0) || want == 0 {
			if !sameSpecial(got, want) {
				t.Errorf("Pow(%v, %v) = %v, want %v", c[0], c[1], got, want)
			}
		} else if got != want {
			t.Errorf("Pow(%v, %v) = %v, want %v", c[0], c[1], got, want)
		}
	}
	for _, c := range [][2]float64{{2, 0.5}, {7, 4}, {7, -4}, {-3, 3}, {-2, 5}, {10, -2}} {
		if u := dsptest.UlpDiff(dsp.Pow(c[0], c[1]), math.Pow(c[0], c[1])); u > 2 {
			t.Errorf("Pow(%v, %v) is %d ulp off", c[0], c[1], u)
		}
	}
}

// goldenInputs are the 64 fixed inputs per function; the golden file stores their bit patterns and results.
func goldenInputs() map[string][][]float64 {
	g := map[string][][]float64{}
	for k := 0; k < 64; k++ {
		fk := float64(k)
		g["exp"] = append(g["exp"], []float64{(fk-32)*21.7/3 + 0.013*fk})
		g["log"] = append(g["log"], []float64{math.Ldexp(1+0.0137*fk, (k-32)*9)})
		g["pow"] = append(g["pow"], []float64{0.3 + 0.151*fk, float64((k*7)%23-11) * 0.37})
	}
	return g
}

func goldenCompute(fn string, in []float64) float64 {
	switch fn {
	case "exp":
		return dsp.Exp(in[0])
	case "log":
		return dsp.Log(in[0])
	}
	return dsp.Pow(in[0], in[1])
}

func bits(f float64) string { return fmt.Sprintf("%016x", math.Float64bits(f)) }

const mathGoldenPath = "testdata/math-golden.txt"

// TestMath_Golden pins the bit patterns of Exp, Log and Pow on 64 fixed inputs each: identical on any linux/amd64 host
// built with GOAMD64=v1. On amd64 with another GOAMD64 it fails (v3 fuses multiply-adds); other architectures skip.
func TestMath_Golden(t *testing.T) {
	dsptest.RequireGolden(t)
	inputs := goldenInputs()
	order := []string{"exp", "log", "pow"}
	if *update {
		var sb strings.Builder
		sb.WriteString("# bit patterns of dsp.Exp, dsp.Log, dsp.Pow: function, input bits (one or two), result bits. linux/amd64, GOAMD64=v1.\n")
		for _, fn := range order {
			for _, in := range inputs[fn] {
				sb.WriteString(fn)
				for _, v := range in {
					sb.WriteString(" " + bits(v))
				}
				sb.WriteString(" " + bits(goldenCompute(fn, in)) + "\n")
			}
		}
		if err := os.WriteFile(mathGoldenPath, []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.Open(mathGoldenPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	counts := map[string]int{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		fld := strings.Fields(line)
		fn := fld[0]
		var in []float64
		for _, h := range fld[1 : len(fld)-1] {
			u, err := strconv.ParseUint(h, 16, 64)
			if err != nil {
				t.Fatalf("bad golden line %q: %v", line, err)
			}
			in = append(in, math.Float64frombits(u))
		}
		if want := inputs[fn][counts[fn]]; len(want) != len(in) || bits(want[0]) != bits(in[0]) {
			t.Fatalf("golden input %d of %s differs from the generator: regenerate with -update", counts[fn], fn)
		}
		counts[fn]++
		if got := bits(goldenCompute(fn, in)); got != fld[len(fld)-1] {
			t.Errorf("%s(%v): got %s, golden %s", fn, in, got, fld[len(fld)-1])
		}
	}
	for _, fn := range order {
		if counts[fn] != 64 {
			t.Errorf("%s: %d golden rows, want 64", fn, counts[fn])
		}
	}
}
