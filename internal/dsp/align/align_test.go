package align_test

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"math"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/align"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
)

var update = flag.Bool("update", false, "rewrite the golden files under testdata")

func TestRamp_Values(t *testing.T) {
	t.Parallel()
	r := align.Ramp{T0: 92, T1: 128, Beats: 32}
	if got := r.Total(); math.Abs(got-17.77) > 0.01 {
		t.Errorf("Total() = %v, want 17.77 +- 0.01", got)
	}
	if got := r.BeatAt(r.Total()); math.Abs(got-32) > 1e-9 {
		t.Errorf("BeatAt(Total()) = %v, want 32", got)
	}
	if got := r.Alpha(0); got != 1 {
		t.Errorf("Alpha(0) = %v, want 1", got)
	}
	if got := r.Alpha(r.Total()); math.Abs(got-92.0/128) > 1e-9 {
		t.Errorf("Alpha(Total()) = %v, want %v", got, 92.0/128)
	}
	// the local tempo is T0 at the start and T1 at the end, and the closed form matches a numeric integral of 60/T(b)
	if got, want := r.TempoAt(0), 92.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("TempoAt(0) = %v", got)
	}
	if got := r.TempoAt(32); math.Abs(got-128) > 1e-9 {
		t.Errorf("TempoAt(B) = %v", got)
	}
	var sum float64
	const n = 100000
	for i := range n {
		sum += 60 / r.TempoAt((float64(i)+0.5)*32/n) * 32 / n
	}
	if math.Abs(sum-r.Total()) > 1e-6 {
		t.Errorf("integral of 60/T = %v, Total() = %v", sum, r.Total())
	}
	// a ramp that slows down
	d := align.Ramp{T0: 128, T1: 92, Beats: 32}
	if got := d.BeatAt(d.Total()); math.Abs(got-32) > 1e-9 {
		t.Errorf("slowing BeatAt(Total()) = %v", got)
	}
	// beyond the horizon of a speeding-up ramp there is no beat
	if got := r.BeatAt(1e6); !math.IsInf(got, 1) {
		t.Errorf("BeatAt(1e6) = %v, want +Inf", got)
	}
}

func TestRamp_Roundtrip(t *testing.T) {
	t.Parallel()
	for _, r := range []align.Ramp{{T0: 92, T1: 128, Beats: 32}, {T0: 140, T1: 70, Beats: 16}, {T0: 100, T1: 100, Beats: 8},
		{T0: 100, T1: 100.01, Beats: 8}, {T0: 100, T1: 99.99, Beats: 8}} {
		for i := range 100 {
			beta := r.Beats * float64(i) / 99
			if got := r.BeatAt(r.Time(beta)); math.Abs(got-beta) > 1e-9 {
				t.Errorf("%+v: BeatAt(Time(%v)) = %v", r, beta, got)
			}
		}
	}
}

func TestRamp_Degenerate(t *testing.T) {
	t.Parallel()
	r := align.Ramp{T0: 120, T1: 120, Beats: 16}
	if got := r.Total(); got != 8 {
		t.Errorf("Total() = %v, want 8 (16 beats at 120)", got)
	}
	if got := r.Alpha(3); got != 1 {
		t.Errorf("Alpha = %v, want exactly 1", got)
	}
	// continuity across the series/closed-form switch
	a := align.Ramp{T0: 120, T1: 120 * (1 + 1e-4), Beats: 16}
	b := align.Ramp{T0: 120, T1: 120 * (1 + 1e-3), Beats: 16}
	if math.Abs(a.Total()-8) > 1e-3 || math.Abs(b.Total()-8) > 1e-2 {
		t.Errorf("near-unity totals %v %v", a.Total(), b.Total())
	}
	for _, tt := range []float64{0.5, 3} {
		for _, d := range []float64{9.9e-4, 1.1e-3} {
			rr := align.Ramp{T0: 120, T1: 120 * (1 + d), Beats: 1}
			if got := rr.BeatAt(rr.Time(tt)); math.Abs(got-tt) > 1e-9 {
				t.Errorf("roundtrip near the switch: %v", got)
			}
		}
	}
}

func TestKeyShift_Table(t *testing.T) {
	t.Parallel()
	const (
		C, D, Eb, F, Fs, A, Bb, B = 0, 2, 3, 5, 6, 9, 10, 11
	)
	for _, tc := range []struct {
		name string
		aPC  int
		aMin bool
		bPC  int
		bMin bool
		want float64
	}{
		{"A min vs C maj", A, true, C, false, 0},
		{"C vs D", C, false, D, false, -2},
		{"C vs F#", C, false, Fs, false, -6},
		{"F# vs C", Fs, false, C, false, -6},
		{"A min vs B min", A, true, B, true, -2},
		{"Eb maj vs C min", Eb, false, C, true, 0},
		{"C vs Bb", C, false, Bb, false, 2},
		{"C vs F", C, false, F, false, -5},
		{"F vs C", F, false, C, false, 5},
		{"pc reduced mod 12", C + 12, false, D - 12, false, -2},
	} {
		if got := align.KeyShift(tc.aPC, tc.aMin, tc.bPC, tc.bMin); got != tc.want {
			t.Errorf("%s: KeyShift = %v, want %v", tc.name, got, tc.want)
		}
	}
	// every pair stays inside [-6, +5] and shifting B by the result lands on A's pitch set
	for a := range 24 {
		for b := range 24 {
			s := align.KeyShift(a%12, a >= 12, b%12, b >= 12)
			if s < -6 || s > 5 {
				t.Fatalf("shift %v out of range", s)
			}
		}
	}
}

func TestFold_Table(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		partner, measured float64
		want              int
		bpm               float64
	}{
		{92, 128, 0, 128}, {140, 70, 1, 140}, {90, 200, -1, 100}, {120, 60, 1, 120}, {100, 100, 0, 100},
		{100, 141.42, 0, 141.42}, // the exact tie (a ratio of sqrt 2) goes to 0
		{100, 142, -1, 71},
		{0, 100, 0, 100}, {100, 0, 0, 0},
	} {
		got := align.Fold(tc.partner, tc.measured)
		if got != tc.want {
			t.Errorf("Fold(%v, %v) = %d, want %d", tc.partner, tc.measured, got, tc.want)
		}
		if f := align.Folded(tc.measured, got); math.Abs(f-tc.bpm) > 1e-9 {
			t.Errorf("Folded(%v, %d) = %v, want %v", tc.measured, got, f, tc.bpm)
		}
	}
	// the exact tie
	if got := align.Fold(100, 100*math.Sqrt2); got != 0 {
		t.Errorf("exact tie went to %d", got)
	}
}

func TestGridBar(t *testing.T) {
	t.Parallel()
	g := align.Grid{BPM: 120, FirstDownbeatS: 0.31, BeatsPerBar: 4}
	if g.BarS() != 2 || g.BeatS() != 0.5 {
		t.Errorf("BarS %v BeatS %v", g.BarS(), g.BeatS())
	}
	if got := g.BarStart(3); math.Abs(got-6.31) > 1e-12 {
		t.Errorf("BarStart(3) = %v", got)
	}
	if got := g.BarStart(-1); math.Abs(got-(-1.69)) > 1e-12 {
		t.Errorf("BarStart(-1) = %v", got)
	}
	for _, tc := range []struct {
		t    float64
		want int
	}{{0.31, 0}, {1.2, 0}, {1.31, 1}, {2.3, 1}, {6.3, 3}, {-0.6, 0}, {-0.69, 0}, {-0.7, -1}} {
		if got := g.NearestBar(tc.t); got != tc.want {
			t.Errorf("NearestBar(%v) = %d, want %d", tc.t, got, tc.want)
		}
	}
	// a 3/4 grid at 90 BPM
	if got := (align.Grid{BPM: 90, BeatsPerBar: 3}).BarS(); math.Abs(got-2) > 1e-12 {
		t.Errorf("3/4 BarS = %v", got)
	}
}

// ---- onset front end pin (D-T1-20) ----

func hashFloats(x []float64) string {
	h := sha256.New()
	var b [8]byte
	for _, v := range x {
		binary.LittleEndian.PutUint64(b[:], math.Float64bits(v))
		h.Write(b[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func f32(x []float64) []float32 {
	out := make([]float32, len(x))
	for i, v := range x {
		out[i] = float32(v)
	}
	return out
}

// pinSignal is the fixed synthetic signal of the pin: 4 s at 16 kHz of 120 BPM clicks, a decaying 80 Hz kick on every
// second beat and a little noise.
func pinSignal() []float32 {
	const rate = 16000
	x := dsptest.Clicks(rate, 120, 0.8, 4)
	for k := 0; k < 8; k += 2 {
		for i := range 2400 {
			j := k*rate/2 + i
			if j < len(x) {
				x[j] += float64(0.5 * math.Sin(2*math.Pi*80*float64(i)/rate) * dsp.Exp(-float64(i)/600))
			}
		}
	}
	for i, v := range dsptest.Noise(7, len(x), 0.01) {
		x[i] += v
	}
	return f32(x)
}

// onsetGolden maps each pinned scenario of the envelope to its hash.
func onsetGolden() map[string]string {
	x := pinSignal()
	env := analysis.OnsetEnvelope(x, 16000)
	// the same content at 48 kHz (repeated samples): a different frame length, the same hop
	x3 := make([]float32, 0, 3*len(x))
	for _, v := range x {
		x3 = append(x3, v, v, v)
	}
	return map[string]string{
		"onset16k": hashFloats(env),
		"onset48k": hashFloats(analysis.OnsetEnvelope(x3, 48000)),
	}
}

const onsetGoldenPath = "testdata/onset-golden.txt"

func readGolden(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0][0] != '#' {
			want[f[0]] = f[1]
		}
	}
	return want
}

func writeGolden(t *testing.T, path, header string, got map[string]string) {
	t.Helper()
	names := make([]string, 0, len(got))
	for k := range got {
		names = append(names, k)
	}
	sort.Strings(names)
	var sb strings.Builder
	sb.WriteString(header)
	for _, k := range names {
		sb.WriteString(k + " " + got[k] + "\n")
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestOnset_PinnedForT1 pins analysis.OnsetEnvelope bit for bit on a fixed synthetic signal (D-T1-20). The beat-phase front
// end of every alignment is this exported function, so a later change to internal/analysis (a retune, an mm-tempo/2) that
// moves a single bit fails here. The response to a failure is NEVER -update on its own: bump dsp_version (D-T1-3), which
// changes every transform hash on purpose, and only then refresh the golden.
func TestOnset_PinnedForT1(t *testing.T) {
	dsptest.RequireGolden(t)
	got := onsetGolden()
	if *update {
		writeGolden(t, onsetGoldenPath, "# sha256 of the little-endian bit patterns of analysis.OnsetEnvelope on the fixed signal of pinSignal (D-T1-20). linux/amd64, GOAMD64=v1.\n# A change here means a dsp_version bump (D-T1-3), not just a refresh.\n", got)
	}
	want := readGolden(t, onsetGoldenPath)
	for k, h := range got {
		if h != want[k] {
			t.Errorf("%s: hash %s, golden %s: analysis.OnsetEnvelope changed; bump dsp_version (mm-dsp/N) before refreshing this golden", k, h, want[k])
		}
	}
	if len(want) != len(got) {
		t.Errorf("golden has %d entries, test has %d", len(want), len(got))
	}
}

// TestOnset_Geometry checks what LocalGrid assumes of the front end: a hop of 128 samples at any rate, frames of 1024, nil
// below one frame.
func TestOnset_Geometry(t *testing.T) {
	t.Parallel()
	for _, rate := range []int{16000, 48000} {
		x := make([]float32, rate)
		if n, want := len(analysis.OnsetEnvelope(x, rate)), (rate-1024)/128+1; n != want {
			t.Errorf("rate %d: %d frames, want %d", rate, n, want)
		}
	}
	if analysis.OnsetEnvelope(make([]float32, 1023), 16000) != nil {
		t.Error("below one window the envelope must be nil")
	}
}
