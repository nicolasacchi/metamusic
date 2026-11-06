package pv_test

import (
	"context"
	"errors"
	"flag"
	"math"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/dsp/pv"
)

var update = flag.Bool("update", false, "rewrite the golden files under testdata")

const rate = 16000

func p1024() pv.Params { return pv.Params{N: 1024, Lock: true, Rate: rate} }

func constant(v float64) func(int) float64 { return func(int) float64 { return v } }

func cents(a, b float64) float64 { return 1200 * math.Log2(a/b) }

func rms(x []float64) float64 { return dsptest.RMS(x) }

// mid is the part of x between a and b seconds.
func mid(x []float64, a, b float64) []float64 {
	return x[int(a*rate):int(b*rate)]
}

func TestStretch_Length(t *testing.T) {
	t.Parallel()
	for _, n := range []int{1, 5, 1000, 32000, 32001} {
		for _, al := range []float64{0.5, 0.8, 1, 1.25, 1.5, 2, 3.3} {
			x := dsptest.Noise(1, n, 0.1)
			y := pv.Stretch(x, constant(al), p1024())
			want := int(math.Floor(al*float64(n) + 0.5))
			if len(y) != want || pv.StretchLen(n, constant(al), p1024()) != want {
				t.Errorf("n=%d alpha=%v: len %d, StretchLen %d, want %d", n, al, len(y), pv.StretchLen(n, constant(al), p1024()), want)
			}
		}
	}
	// a varying ratio: Stretch and StretchLen agree, and the length is between the two constant extremes
	ramp := func(m int) float64 { return 1 + float64(m)/200 }
	x := dsptest.Noise(2, 32000, 0.1)
	y := pv.Stretch(x, ramp, p1024())
	if l := pv.StretchLen(len(x), ramp, p1024()); len(y) != l || l < 32000 || l > 64000 {
		t.Errorf("ramp: len %d, StretchLen %d", len(y), l)
	}
	if got := pv.Stretch(nil, constant(1), p1024()); len(got) != 0 {
		t.Errorf("empty input gave %d samples", len(got))
	}
}

func TestStretch_Errors(t *testing.T) {
	t.Parallel()
	x := dsptest.Sine(rate, 440, 0.5, 0.2)
	for name, fn := range map[string]func() error{
		"alpha low":  func() error { _, e := pv.StretchErr(x, constant(0.1), p1024()); return e },
		"alpha high": func() error { _, e := pv.StretchErr(x, constant(9), p1024()); return e },
		"alpha nan":  func() error { _, e := pv.StretchErr(x, constant(math.NaN()), p1024()); return e },
		"alpha nil":  func() error { _, e := pv.StretchErr(x, nil, p1024()); return e },
		"N":          func() error { _, e := pv.StretchErr(x, constant(1), pv.Params{N: 1000}); return e },
		"N small":    func() error { _, e := pv.StretchErr(x, constant(1), pv.Params{N: 32}); return e },
		"Hs":         func() error { _, e := pv.StretchErr(x, constant(1), pv.Params{N: 1024, Hs: 600}); return e },
		"rho":        func() error { _, e := pv.ShiftErr(x, constant(0), p1024()); return e },
		"rho big":    func() error { _, e := pv.ShiftErr(x, constant(1000), p1024()); return e },
		"no voices":  func() error { _, e := pv.ShiftBank(x, nil, p1024()); return e },
		"no rho":     func() error { _, e := pv.ShiftBank(x, []pv.Voice{{}}, p1024()); return e },
		"no rate": func() error {
			_, e := pv.ShiftErr(x, constant(1), pv.Params{N: 1024, FormantRatio: constant(1)})
			return e
		},
	} {
		if err := fn(); !errors.Is(err, pv.ErrParam) {
			t.Errorf("%s: err = %v, want ErrParam", name, err)
		}
	}
	if pv.Stretch(x, constant(0.1), p1024()) != nil || pv.Shift(x, constant(0), p1024()) != nil {
		t.Error("the plain entry points must return nil on error")
	}
	if pv.StretchLen(10, constant(0.1), p1024()) != 0 || pv.StretchLen(10, constant(1), pv.Params{N: 3}) != 0 {
		t.Error("StretchLen must be 0 on invalid parameters")
	}
	// defaults: zero Params is N=4096
	if y := pv.Stretch(dsptest.Sine(rate, 440, 0.5, 0.5), constant(1), pv.Params{}); len(y) != 8000 {
		t.Errorf("default params: len %d", len(y))
	}
}

func TestStretch_Sine(t *testing.T) {
	t.Parallel()
	x := dsptest.Sine(rate, 440, 0.5, 2)
	for _, lock := range []bool{true, false} {
		p := p1024()
		p.Lock = lock
		y := pv.Stretch(x, constant(1.5), p)
		if len(y) != 48000 {
			t.Fatalf("len %d, want 48000", len(y))
		}
		seg := mid(y, 0.5, 2.5)
		if f := dsptest.PeakFreq(seg, rate); math.Abs(f-440) > 2 {
			t.Errorf("lock=%v: peak %.2f Hz, want 440 +- 2", lock, f)
		}
		src := rms(mid(x, 0.5, 1.5))
		if d := dsptest.DBFS(rms(seg) / src); math.Abs(d) > 1 {
			t.Errorf("lock=%v: level off by %.2f dB", lock, d)
		}
	}
}

func TestStretch_Identity(t *testing.T) {
	t.Parallel()
	x := dsptest.Concat(dsptest.Sine(rate, 440, 0.4, 2), dsptest.Noise(3, 0, 1))
	n := dsptest.Noise(4, len(x), 0.05)
	for i := range x {
		x[i] += float64(0.3*math.Sin(2*math.Pi*1330*float64(i)/rate)) + n[i]
	}
	for _, lock := range []bool{true, false} {
		p := p1024()
		p.Lock = lock
		y := pv.Stretch(x, constant(1), p)
		if len(y) != len(x) {
			t.Fatalf("len %d", len(y))
		}
		var e float64
		for i := p.N; i < len(x); i++ {
			e = math.Max(e, math.Abs(y[i]-x[i]))
		}
		if r := e / rms(x); r >= 0.02 {
			t.Errorf("lock=%v: max|y-x|/rms = %.4f", lock, r)
		}
	}
}

// meanBurst is the energy centroid of the burst of x around sample c (a window of +-w samples).
func centroid(x []float64, c, w int) float64 {
	var s, e float64
	for i := max(c-w, 0); i < min(c+w, len(x)); i++ {
		v := float64(x[i] * x[i])
		s += float64(v * float64(i))
		e += v
	}
	return s / e
}

func TestStretch_Clicks(t *testing.T) {
	t.Parallel()
	x := dsptest.Clicks(rate, 120, 0.8, 6.01) // 2 Hz
	y := pv.Stretch(x, constant(1.25), p1024())
	var cs []float64
	for k := 1; k < 10; k++ {
		cs = append(cs, centroid(y, int(float64(k)*0.5*1.25*rate), 4000))
	}
	for i := 1; i < len(cs); i++ {
		if d := (cs[i] - cs[i-1]) / rate; math.Abs(d-0.625) > 0.005 {
			t.Errorf("click spacing %d: %.4f s, want 0.625 +- 0.005", i, d)
		}
	}
}

// bursts returns the energy centroid in samples of every burst of y: a run of samples whose box-smoothed energy is above
// frac of the loudest, merged across gaps shorter than 0.1 s.
func bursts(y []float64, frac float64) []float64 {
	const box = 256
	env := make([]float64, len(y))
	var run, top float64
	for i := range y {
		run += float64(y[i] * y[i])
		if i >= box {
			run -= float64(y[i-box] * y[i-box])
		}
		env[i] = run
		top = math.Max(top, run)
	}
	var out []float64
	start, last := -1, -1
	flush := func() {
		if start >= 0 {
			out = append(out, centroid(y, (start+last)/2, (last-start)/2+box))
		}
		start = -1
	}
	for i, v := range env {
		if v > float64(frac*top) {
			if start >= 0 && i-last > rate/10 {
				flush()
			}
			if start < 0 {
				start = i
			}
			last = i
		}
	}
	flush()
	return out
}

func TestStretch_VariableAlpha(t *testing.T) {
	t.Parallel()
	const hs = 256 // N/4
	x := dsptest.Clicks(rate, 120, 0.8, 8.01)
	// alpha rises from 1 to 2 over the first 12 s of output time
	alpha := func(m int) float64 { return 1 + math.Min(float64(m*hs)/rate/12, 1) }
	y := pv.Stretch(x, alpha, p1024())
	pos := bursts(y, 0.1)
	if len(pos) < 12 {
		t.Fatalf("%d bursts found, want 16", len(pos))
	}
	for _, f := range []float64{0.25, 0.5, 0.75} {
		i := int(f * float64(len(pos)-2))
		d := (pos[i+1] - pos[i]) / rate
		tau := (pos[i] + pos[i+1]) / 2 / rate
		want := 0.5 * alpha(int(tau*rate)/hs)
		if math.Abs(d-want) > 0.005 {
			t.Errorf("at %.0f%% (tau=%.2fs): spacing %.4f s, want %.4f +- 0.005", 100*f, tau, d, want)
		}
	}
}

func TestShift_Sine(t *testing.T) {
	t.Parallel()
	x := dsptest.Sine(rate, 440, 0.5, 2)
	rho := math.Pow(2, 3.0/12)
	for _, lock := range []bool{true} {
		p := p1024()
		p.Lock = lock
		y := pv.Shift(x, constant(rho), p)
		if len(y) != len(x) {
			t.Fatalf("len %d", len(y))
		}
		if f := dsptest.PeakFreq(mid(y, 0.3, 1.7), rate); math.Abs(f-523.25) > 2 {
			t.Errorf("peak %.2f Hz, want 523.25 +- 2", f)
		}
	}
	// unity ratio is an identity
	y := pv.Shift(x, constant(1), p1024())
	var e float64
	for i := 1024; i < len(x)-1024; i++ {
		e = math.Max(e, math.Abs(y[i]-x[i]))
	}
	if e > 1e-6 {
		t.Errorf("rho=1 deviates by %g", e)
	}
}

func TestShift_RampSine(t *testing.T) {
	t.Parallel()
	x := dsptest.Sine(rate, 440, 0.5, 2)
	const period = 1.5
	hs := 256
	rho := func(m int) float64 { return math.Pow(2, float64(m*hs)/rate/period) }
	y := pv.Shift(x, rho, p1024())
	for _, f := range []float64{0.25, 0.5, 0.75} {
		tc := f * 2
		got := dsptest.PeakFreq(y[int(tc*rate)-512:int(tc*rate)+512], rate)
		want := 440 * math.Pow(2, tc/period)
		if c := cents(got, want); math.Abs(c) > 20 {
			t.Errorf("at %.0f%% (t=%.1fs): %.1f Hz, want %.1f (%.1f cents)", 100*f, tc, got, want, c)
		}
	}
}

// vowel is a synthetic vowel: a pulse train of f0 with a little breath noise through two resonators (formants 700 and 1200
// Hz), so the line spectrum has a noise floor like a real voice and the envelope is well defined.
func vowel(f0 float64, seconds float64) []float64 {
	n := dsptest.Frames(rate, seconds)
	e := dsptest.Noise(11, n, 0.05)
	period := rate / f0
	for k := 0; ; k++ {
		i := int(float64(k) * period)
		if i >= n {
			break
		}
		e[i]++
	}
	res := func(x []float64, f, bw float64) []float64 {
		r := math.Exp(-math.Pi * bw / rate)
		a1, a2 := -2*r*math.Cos(2*math.Pi*f/rate), r*r
		y := make([]float64, len(x))
		for i := range x {
			y[i] = (1 - r) * x[i]
			if i >= 1 {
				y[i] -= a1 * y[i-1]
			}
			if i >= 2 {
				y[i] -= a2 * y[i-2]
			}
		}
		return y
	}
	x := res(res(e, 700, 120), 1200, 160)
	return dsptest.Scale(x, 0.5/dsptest.PeakAbs(x))
}

// envMax is the frequency of the maximum of the magnitude spectrum smoothed over +-bw Hz.
func envMax(x []float64, bw float64) float64 {
	const n = 16384
	mag := dsptest.Spectrum(x, n)
	hz := func(k int) float64 { return float64(k) * rate / n }
	best, bf := -1.0, 0.0
	for k := range mag {
		if hz(k) < 80 || hz(k) > 3000 {
			continue
		}
		var s float64
		var c int
		for j := k - int(bw*n/rate); j <= k+int(bw*n/rate); j++ {
			if j >= 0 && j < len(mag) {
				s += mag[j] * mag[j]
				c++
			}
		}
		if s/float64(c) > best {
			best, bf = s/float64(c), hz(k)
		}
	}
	return bf
}

func TestShift_Formant(t *testing.T) {
	t.Parallel()
	x := vowel(200, 2)
	if f := envMax(x, 130); math.Abs(f-700) > 56 {
		t.Fatalf("test signal envelope max at %.0f Hz, want ~700", f)
	}
	p := p1024()
	p.FormantRatio = constant(1)
	held := pv.Shift(x, constant(0.5), p)
	if f := envMax(mid(held, 0.3, 1.7), 130); math.Abs(f-700) > 56 {
		t.Errorf("phi=1: envelope max at %.0f Hz, want 700 +- 8%%", f)
	}
	p.FormantRatio = constant(0)
	followed := pv.Shift(x, constant(0.5), p)
	if f := envMax(mid(followed, 0.3, 1.7), 130); math.Abs(f-350) > 28 {
		t.Errorf("phi=0: envelope max at %.0f Hz, want 350 +- 8%%", f)
	}
}

func TestFormantWarp(t *testing.T) {
	t.Parallel()
	x := vowel(200, 0.5)[:1024]
	mag := dsptest.Spectrum(x, 1024)
	bin := func(f float64) int { return int(f * 1024 / rate) }
	up := pv.FormantWarp(mag, rate, 1.5)
	var peak int
	for k := bin(300); k < bin(2400); k++ {
		if up[k] > up[peak] {
			peak = k
		}
	}
	if f := float64(peak) * rate / 1024; f < 800 {
		t.Errorf("warped by 1.5: strongest bin at %.0f Hz, want the envelope to move up", f)
	}
	same := pv.FormantWarp(mag, rate, 1)
	for k := range mag {
		if math.Abs(same[k]-mag[k]) > 1e-9*(1+mag[k]) {
			t.Fatalf("phi=1 changed bin %d", k)
		}
	}
	off := pv.FormantWarp(mag, rate, 0)
	if off[10] != mag[10] || len(off) != len(mag) {
		t.Error("phi<=0 must copy")
	}
	if got := pv.FormantWarp([]float64{1, 2}, rate, 2); len(got) != 2 {
		t.Error("tiny spectra are copied")
	}
	// a silent frame must not blow up
	for _, v := range pv.FormantWarp(make([]float64, 513), rate, 2) {
		if v != 0 {
			t.Fatal("silence changed")
		}
	}
}

func TestShiftBank_MatchesShift(t *testing.T) {
	t.Parallel()
	x := dsptest.Sine(rate, 330, 0.3, 1)
	for i, v := range dsptest.Noise(5, len(x), 0.05) {
		x[i] += v
	}
	rhos := []float64{math.Pow(2, 3.0/12), math.Pow(2, -5.0/12), 1.5}
	phis := []float64{0, 1, 0.8}
	p := p1024()
	var singles [][]float64
	var voices []pv.Voice
	for i := range rhos {
		q := p
		q.FormantRatio = constant(phis[i])
		singles = append(singles, pv.Shift(x, constant(rhos[i]), q))
		voices = append(voices, pv.Voice{Rho: constant(rhos[i]), Formant: constant(phis[i])})
	}
	one, err := pv.ShiftBank(x, voices[:1], p)
	if err != nil {
		t.Fatal(err)
	}
	for i := range x {
		if math.Float64bits(one[0][i]) != math.Float64bits(singles[0][i]) {
			t.Fatalf("bank of one differs from Shift at %d", i)
		}
	}
	three, err := pv.ShiftBank(x, voices, p)
	if err != nil {
		t.Fatal(err)
	}
	for i := range x {
		var a, b float64
		for v := range voices {
			a += three[v][i]
			b += singles[v][i]
		}
		if math.Abs(a-b) > 1e-9 {
			t.Fatalf("sum of three differs at %d by %g", i, math.Abs(a-b))
		}
	}
	// gains: a voice at gain 0 is silent and does not disturb the others; gain scales
	g := []pv.Voice{{Rho: constant(1.5), Gain: constant(0)}, {Rho: constant(1.5), Gain: constant(0.5)}}
	out, err := pv.ShiftBank(x, g, p)
	if err != nil {
		t.Fatal(err)
	}
	if dsptest.PeakAbs(out[0]) != 0 {
		t.Error("gain 0 voice is not silent")
	}
	if r := rms(out[1]) / rms(pv.Shift(x, constant(1.5), p)); math.Abs(r-0.5) > 1e-6 {
		t.Errorf("gain 0.5 gave level ratio %v", r)
	}
	// gain that switches on mid-run
	sw := []pv.Voice{{Rho: constant(1.5), Gain: func(m int) float64 {
		if m < 20 {
			return 0
		}
		return 1
	}}}
	if o, err := pv.ShiftBank(x, sw, p); err != nil || dsptest.PeakAbs(o[0][len(x)-1000:]) == 0 {
		t.Errorf("switched voice: err=%v", err)
	}
	if o, err := pv.ShiftBank(nil, voices, p); err != nil || len(o) != 3 || len(o[0]) != 0 {
		t.Errorf("empty input: %v", err)
	}
}

func TestShift_NyquistGuard(t *testing.T) {
	t.Parallel()
	x := dsptest.Sine(rate, 4000, 0.5, 1)
	y := pv.Shift(x, constant(8), p1024())
	if len(y) != len(x) {
		t.Fatalf("len %d", len(y))
	}
	for i, v := range y {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Fatalf("non-finite sample at %d", i)
		}
	}
	if m := dsptest.PeakAbs(y); m > 1e-9 {
		t.Errorf("the tone is above Nyquist after the shift but the output peaks at %g", m)
	}
	// a shift down past DC drops bins below zero as well
	y = pv.Shift(dsptest.Sine(rate, 100, 0.5, 1), constant(1.0/64), p1024())
	for _, v := range y {
		if math.IsNaN(v) {
			t.Fatal("NaN")
		}
	}
}

func TestPeaks_Table(t *testing.T) {
	t.Parallel()
	const n = 1024
	two := dsptest.Spectrum(func() []float64 {
		a, b := dsptest.Sine(rate, 1000, 1, 0.0625), dsptest.Sine(rate, 3000, 0.5, 0.0625)
		for i := range a {
			a[i] += b[i]
		}
		return a
	}(), n)
	pk := pv.FindPeaks(two, pv.PeakFloor, nil)
	if len(pk) != 2 {
		t.Fatalf("%d peaks: %+v", len(pk), pk)
	}
	b1, b2 := 1000*n/rate, 3000*n/rate
	if abs(pk[0].Bin-b1) > 1 || abs(pk[1].Bin-b2) > 1 {
		t.Errorf("peak bins %d, %d; want %d, %d", pk[0].Bin, pk[1].Bin, b1, b2)
	}
	if pk[0].Lo != 0 || pk[1].Hi != len(two)-1 || pk[1].Lo != pk[0].Hi+1 {
		t.Errorf("regions do not tile the spectrum: %+v", pk)
	}
	if pk[0].Hi <= b1 || pk[0].Hi >= b2 {
		t.Errorf("the split %d is not between the peaks", pk[0].Hi)
	}
	for k := pk[0].Bin + 1; k < pk[1].Bin; k++ {
		if two[k] < two[pk[0].Hi] {
			t.Errorf("bin %d is lower than the split bin %d", k, pk[0].Hi)
		}
	}

	// a hand-made table: plateau gives one peak, neighbours within two bins suppress, floor drops tiny maxima
	tbl := []float64{0, 1, 3, 3, 1, 0, 0.5, 2, 0.1, 0.4, 0.2, 0.0001, 0.00005, 0.0001}
	got := pv.FindPeaks(tbl, 1e-3, nil)
	// peaks at 2 (plateau 2-3) and 7; the bin at 9 (0.4) is within two bins of 7 and below its right neighbour's reach;
	// the 1e-4 maxima are below the floor of 1e-3 * 3. Splits: the minimum between 2 and 7 is bin 5 (first of the zeros).
	if len(got) != 2 || got[0] != (pv.Peak{Bin: 2, Lo: 0, Hi: 5}) || got[1] != (pv.Peak{Bin: 7, Lo: 6, Hi: 13}) {
		t.Errorf("table peaks %+v", got)
	}
	if got := pv.FindPeaks(make([]float64, 16), pv.PeakFloor, nil); len(got) != 0 {
		t.Errorf("silence has peaks: %+v", got)
	}
	// dst is reused
	buf := make([]pv.Peak, 0, 8)
	if got := pv.FindPeaks(tbl, 1e-3, buf); &got[0] != &buf[:1][0] {
		t.Error("dst not reused")
	}
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

func signal() []float64 {
	x := dsptest.Sine(rate, 440, 0.3, 1)
	for i, v := range dsptest.Noise(9, len(x), 0.05) {
		x[i] += v + float64(0.1*math.Sin(2*math.Pi*1750*float64(i)/rate))
	}
	return x
}

func TestPV_Deterministic(t *testing.T) {
	t.Parallel()
	x := signal()
	p := p1024()
	p.FormantRatio = constant(1)
	a, b := pv.Shift(x, constant(1.3), p), pv.Shift(x, constant(1.3), p)
	c, d := pv.Stretch(x, constant(1.4), p), pv.Stretch(x, constant(1.4), p)
	for i := range a {
		if math.Float64bits(a[i]) != math.Float64bits(b[i]) {
			t.Fatalf("Shift differs at %d", i)
		}
	}
	for i := range c {
		if math.Float64bits(c[i]) != math.Float64bits(d[i]) {
			t.Fatalf("Stretch differs at %d", i)
		}
	}
}

func TestPV_Budget(t *testing.T) {
	t.Parallel()
	x := signal()
	p := p1024()
	for name, run := range map[string]func(p pv.Params) error{
		"stretch": func(p pv.Params) error { _, e := pv.StretchErr(x, constant(1.2), p); return e },
		"shift":   func(p pv.Params) error { _, e := pv.ShiftErr(x, constant(1.2), p); return e },
		"formant": func(p pv.Params) error {
			p.FormantRatio = constant(1)
			_, e := pv.ShiftErr(x, constant(1.2), p)
			return e
		},
	} {
		// the exact work is what an unlimited meter counts
		m := &dsp.Meter{}
		q := p
		q.Meter = m
		if err := run(q); err != nil || m.Used() == 0 {
			t.Fatalf("%s: unmetered run: %v, used %d", name, err, m.Used())
		}
		q.Meter = &dsp.Meter{Max: m.Used() / 2}
		if err := run(q); !errors.Is(err, dsp.ErrBudget) {
			t.Errorf("%s: half the budget gave %v, want ErrBudget", name, err)
		}
		q.Meter = &dsp.Meter{Max: m.Used()}
		if err := run(q); err != nil {
			t.Errorf("%s: exact budget gave %v", name, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		q.Meter = &dsp.Meter{Ctx: ctx}
		if err := run(q); !errors.Is(err, context.Canceled) {
			t.Errorf("%s: cancelled context gave %v", name, err)
		}
	}
	// a stretch of a frame costs at most dsp.Units.PVFrame
	m := &dsp.Meter{}
	p.Meter = m
	pv.Stretch(x, constant(1), p)
	if frames := int64(len(x)/(p.N/4) + 1); m.Used() > frames*dsp.Units.PVFrame(p.N)+dsp.Units.PVFrame(p.N) {
		t.Errorf("stretch used %d units for ~%d frames", m.Used(), frames)
	}
}
