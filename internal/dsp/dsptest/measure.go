package dsptest

import (
	"math"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
)

// PeakAbs is the largest |x|.
func PeakAbs(x []float64) float64 {
	var m float64
	for _, v := range x {
		m = math.Max(m, math.Abs(v))
	}
	return m
}

// RMS is the root mean square of x.
func RMS(x []float64) float64 {
	if len(x) == 0 {
		return 0
	}
	var s float64
	for _, v := range x {
		s += float64(v * v)
	}
	return math.Sqrt(s / float64(len(x)))
}

// DBFS is 20 log10(lin) (-Inf for 0).
func DBFS(lin float64) float64 { return float64(20 * dsp.Log(lin) / math.Ln10) }

// Mean is the arithmetic mean of x.
func Mean(x []float64) float64 {
	var s float64
	for _, v := range x {
		s += v
	}
	return s / float64(len(x))
}

// Spectrum is the magnitude spectrum of x under a Hann window, zero-padded to n (a power of two >= len(x)); bin k is at
// k*rate/n Hz.
func Spectrum(x []float64, n int) []float64 {
	w := make([]float64, len(x))
	for i, v := range x {
		w[i] = float64(v * (0.5 - float64(0.5*math.Cos(2*math.Pi*float64(i)/float64(len(x))))))
	}
	re, im := dsp.RFFT(w, n)
	mag := make([]float64, len(re))
	for k := range re {
		mag[k] = math.Hypot(re[k], im[k])
	}
	return mag
}

// PeakFreq is the frequency of the strongest spectral peak of x in Hz: Hann window, zero-padding to 4x, parabolic
// interpolation on the log magnitude.
func PeakFreq(x []float64, rate int) float64 {
	n := 4 * dsp.NextPow2(len(x))
	mag := Spectrum(x, n)
	best := 1
	for k := 1; k < len(mag)-1; k++ {
		if mag[k] > mag[best] {
			best = k
		}
	}
	a, b, c := dsp.Log(mag[best-1]+1e-300), dsp.Log(mag[best]+1e-300), dsp.Log(mag[best+1]+1e-300)
	off := 0.0
	if d := a - 2*b + c; d != 0 {
		off = 0.5 * (a - c) / d
	}
	return (float64(best) + off) * float64(rate) / float64(n)
}

// BandDB is the level in dB of the spectrum x (as from Spectrum) between lo and hi Hz relative to a reference magnitude.
func BandDB(mag []float64, rate, n int, lo, hi, ref float64) float64 {
	var e float64
	for k, m := range mag {
		if f := float64(k) * float64(rate) / float64(n); f >= lo && f <= hi {
			e += float64(m * m)
		}
	}
	return float64(10 * dsp.Log(e/float64(ref*ref)+1e-300) / math.Ln10)
}

// ClickPositions returns the sample index of every run of |x| >= thresh, one per run (a refractory gap of minGap samples).
func ClickPositions(x []float64, thresh float64, minGap int) []int {
	var pos []int
	for i, v := range x {
		if math.Abs(v) >= thresh && (len(pos) == 0 || i-pos[len(pos)-1] >= minGap) {
			pos = append(pos, i)
		}
	}
	return pos
}

// UlpDiff is the distance in units in the last place between two finite floats of any sign (0 when equal).
func UlpDiff(a, b float64) uint64 {
	key := func(f float64) int64 {
		u := int64(math.Float64bits(f)) // #nosec G115 -- bit pattern
		if u < 0 {
			return math.MinInt64 - u
		}
		return u
	}
	d := key(a) - key(b)
	if d < 0 {
		d = -d
	}
	return uint64(d) // #nosec G115 -- d >= 0
}

// RequireGolden makes a golden-bearing test refuse to run on a build the goldens do not pin (D-T1-3d). The goldens are pinned
// for linux/amd64 built with GOAMD64=v1 (v3 emits fused multiply-adds): on another architecture the test is skipped, and on
// amd64 with another GOAMD64 it FAILS, never skips.
func RequireGolden(tb testing.TB) {
	tb.Helper()
	if runtime.GOARCH != "amd64" {
		tb.Skipf("goldens are pinned for amd64, this is %s", runtime.GOARCH)
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		tb.Fatal("no build info: cannot tell the GOAMD64 level")
	}
	for _, s := range bi.Settings {
		if s.Key == "GOAMD64" {
			if s.Value != "v1" {
				tb.Fatalf("GOAMD64=%s: the determinism goldens are pinned for GOAMD64=v1", s.Value)
			}
			return
		}
	}
	tb.Fatal("build info has no GOAMD64 setting")
}

// ReexecFMAOff re-runs the current test binary for the tests matching pattern under GODEBUG=cpu.fma=off and fails if it
// fails or if the runtime does not know the setting (stderr "unknown cpu feature": a typo or a GOAMD64=v3 binary would
// otherwise make the check vacuous). The child sees MM_DSP_REEXEC=1 so it does not recurse.
func ReexecFMAOff(tb testing.TB, pattern string) {
	tb.Helper()
	if os.Getenv("MM_DSP_REEXEC") == "1" {
		tb.Skip("already the re-executed child")
	}
	cmd := exec.Command(os.Args[0], "-test.run", pattern, "-test.count=1", "-test.v") //nolint:gosec // G204/G702: the argument is our own test binary
	cmd.Env = append(os.Environ(), "GODEBUG=cpu.fma=off", "MM_DSP_REEXEC=1")
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if strings.Contains(out.String(), "unknown cpu feature") {
		tb.Fatalf("GODEBUG=cpu.fma=off is not a known setting of this build:\n%s", out.String())
	}
	if err != nil {
		tb.Fatalf("under GODEBUG=cpu.fma=off: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "--- PASS") {
		tb.Fatalf("the re-executed child ran no passing test (pattern %q):\n%s", pattern, out.String())
	}
}
