package safety

import (
	"bytes"
	"errors"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
)

const testRate = 16000

// pinkAt returns stereo pink-ish noise scaled to the given integrated loudness.
func pinkAt(seed uint64, secs, lufs float64) pcm.Buffer {
	n := dsptest.Frames(testRate, secs)
	b := dsptest.Stereo(testRate, dsptest.Pink(seed, n), dsptest.Pink(seed+1, n))
	l, _ := LUFS(b)
	g := dsptest.DBToLin(lufs - l)
	for c := range 2 {
		b.Ch[c] = dsptest.Scale(b.Ch[c], g)
	}
	return b
}

func TestFinish_Targets(t *testing.T) {
	t.Parallel()
	b := pinkAt(1, 8, -40)
	rep, err := Finish(&b, FinishIn{TargetLUFS: -16, RefLUFS: -18.5})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := LUFS(b)
	if math.Abs(got+16) > 0.3 {
		t.Errorf("output %.3f LUFS, want -16 +- 0.3", got)
	}
	if rep.TargetLUFSEffective != -16 || rep.TargetLUFSRequested != -16 || rep.Limited || rep.EngagedFraction != 0 || rep.RefLUFS != -18.5 {
		t.Errorf("report %+v", rep)
	}
	if rep.OutputTruePeakDBTP > -0.95 || rep.CeilingDBTP != -1 || math.Abs(rep.GainDB-24) > 0.5 {
		t.Errorf("report %+v", rep)
	}
}

func TestFinish_DefaultTarget(t *testing.T) {
	t.Parallel()
	b := pinkAt(2, 6, -30)
	rep, err := Finish(&b, FinishIn{})
	if err != nil || rep.TargetLUFSRequested != DefaultTargetLUFS {
		t.Fatalf("%+v, %v", rep, err)
	}
}

// clickNoise is low noise with a full-scale click every 100 ms: crest factor about 30 dB.
func clickNoise() pcm.Buffer {
	n := dsptest.Frames(testRate, 6)
	l, r := dsptest.Noise(1, n, 0.02), dsptest.Noise(2, n, 0.02)
	for i := 0; i < n; i += testRate / 10 {
		l[i], r[i] = 1, -1
	}
	return dsptest.Stereo(testRate, l, r)
}

func TestFinish_LowersTarget(t *testing.T) {
	t.Parallel()
	b := clickNoise()
	rep, err := Finish(&b, FinishIn{TargetLUFS: -16})
	if err != nil {
		t.Fatalf("a loud render must not fail: %v", err)
	}
	if !rep.Limited || rep.TargetLUFSRequested != -16 || rep.TargetLUFSEffective >= -16 {
		t.Errorf("report %+v", rep)
	}
	if rep.ShortfallLU > MaxLimitingLU+1e-9 || rep.ShortfallLU < 0 {
		t.Errorf("shortfall %.3f LU, want within [0, 6]", rep.ShortfallLU)
	}
	if rep.OutputTruePeakDBTP > -0.95 || rep.MaxReductionDB <= 0 {
		t.Errorf("report %+v", rep)
	}
	if got, _ := LUFS(b); math.Abs(got-rep.OutputLUFS) > 1e-9 {
		t.Errorf("reported %.3f, measured %.3f", rep.OutputLUFS, got)
	}
}

func TestFinish_NonFinite(t *testing.T) {
	t.Parallel()
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		b := pinkAt(3, 2, -20)
		b.Ch[1][100] = bad
		if _, err := Finish(&b, FinishIn{TargetLUFS: -16}); !errors.Is(err, ErrNonFinite) {
			t.Errorf("%v: %v", bad, err)
		}
	}
}

func TestFinish_Silent(t *testing.T) {
	t.Parallel()
	for name, b := range map[string]pcm.Buffer{
		"zeros":    dsptest.Dual(testRate, dsptest.Silence(testRate*2)),
		"-90 dBFS": dsptest.Dual(testRate, dsptest.Sine(testRate, 440, dsptest.DBToLin(-90), 2)),
		"empty":    {Rate: testRate},
	} {
		if _, err := Finish(&b, FinishIn{TargetLUFS: -16}); !errors.Is(err, ErrSilent) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// quiet but not silent: -60 dBFS is rendered, not failed
	q := dsptest.Dual(testRate, dsptest.Sine(testRate, 440, dsptest.DBToLin(-60), 3))
	if _, err := Finish(&q, FinishIn{TargetLUFS: -16}); err != nil {
		t.Errorf("-60 dBFS sine: %v", err)
	}
	// mismatched channels are a defect, reported as an error rather than a panic
	bad := pcm.Buffer{Rate: testRate, Ch: [2][]float64{{1}, {1, 2}}}
	if _, err := Finish(&bad, FinishIn{}); err == nil {
		t.Error("unequal channels accepted")
	}
}

func TestFinish_DCRemoved(t *testing.T) {
	t.Parallel()
	x := dsptest.Offset(dsptest.Sine(testRate, 440, 0.2, 4), 0.01)
	b := dsptest.Dual(testRate, x)
	rep, err := Finish(&b, FinishIn{TargetLUFS: -20})
	if err != nil || !rep.DCRemoved {
		t.Fatalf("%+v, %v", rep, err)
	}
	for c := range 2 {
		if m := math.Abs(dsptest.Mean(b.Ch[c])); m >= 1e-6 {
			t.Errorf("channel %d mean %g, want < 1e-6", c, m)
		}
	}
	// a small offset under 1e-4 is left alone
	small := dsptest.Dual(testRate, dsptest.Offset(dsptest.Sine(testRate, 440, 0.2, 2), 5e-5))
	if rep, _ := Finish(&small, FinishIn{TargetLUFS: -20}); rep.DCRemoved {
		t.Error("a 5e-5 offset was removed")
	}
}

func TestFinish_Fades(t *testing.T) {
	t.Parallel()
	mk := func() pcm.Buffer { return dsptest.Dual(testRate, dsptest.Sine(testRate, 440, 0.3, 2)) }
	b := mk()
	if _, err := Finish(&b, FinishIn{TargetLUFS: -20}); err != nil {
		t.Fatal(err)
	}
	n := b.Frames()
	if b.Ch[0][0] != 0 || math.Abs(b.Ch[0][n-1]) > 1e-6 {
		t.Errorf("ends are not faded: %g %g", b.Ch[0][0], b.Ch[0][n-1])
	}
	own := mk()
	if _, err := Finish(&own, FinishIn{TargetLUFS: -20, OwnFades: true}); err != nil {
		t.Fatal(err)
	}
	if own.Ch[0][1] == 0 && own.Ch[0][2] == 0 {
		t.Error("OwnFades still faded")
	}
	// a buffer shorter than the fades is still handled
	tiny := dsptest.Dual(testRate, dsptest.Sine(testRate, 440, 0.3, 0.1))
	if _, err := Finish(&tiny, FinishIn{TargetLUFS: -20}); err != nil {
		t.Errorf("tiny buffer: %v", err)
	}
}

func TestFinish_Ceiling(t *testing.T) {
	t.Parallel()
	for _, c := range []float64{-1, -2, -3} {
		b := clickNoise()
		b = dsptest.Stereo(testRate, b.Ch[0][:testRate*3], b.Ch[1][:testRate*3])
		rep, err := Finish(&b, FinishIn{TargetLUFS: -14, CeilingDBTP: c})
		if err != nil {
			t.Fatal(err)
		}
		if rep.CeilingDBTP != c || TruePeakDBTP(b) > c+0.05 {
			t.Errorf("ceiling %v: report %v, true peak %.3f", c, rep.CeilingDBTP, TruePeakDBTP(b))
		}
	}
	// no kind can raise the ceiling above -1.0
	b := clickNoise()
	if rep, err := Finish(&b, FinishIn{TargetLUFS: -14, CeilingDBTP: 0}); err != nil || rep.CeilingDBTP != -1 || TruePeakDBTP(b) > -0.95 {
		t.Errorf("%+v %v", rep, err)
	}
}

func finishedHash(t *testing.T) string {
	t.Helper()
	b := clickNoise()
	q := pinkAt(9, 3, -30)
	b = dsptest.Stereo(testRate, append(b.Ch[0][:testRate*2:testRate*2], q.Ch[0]...), append(b.Ch[1][:testRate*2:testRate*2], q.Ch[1]...))
	if _, err := Finish(&b, FinishIn{TargetLUFS: -16}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	sum, err := pcm.WritePCM24(&buf, b)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func TestFinish_Deterministic(t *testing.T) {
	t.Parallel()
	if a, b := finishedHash(t), finishedHash(t); a != b {
		t.Errorf("two runs differ: %s vs %s", a, b)
	}
}

const finishGoldenPath = "testdata/finish-golden.txt"

// TestFinish_Golden pins pcm_sha256 of a finished buffer (amd64, GOAMD64=v1): LUFS, limiter, fades and DC removal bit for bit.
func TestFinish_Golden(t *testing.T) {
	dsptest.RequireGolden(t)
	got := finishedHash(t)
	if os.Getenv("MM_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(finishGoldenPath, []byte("# pcm_sha256 of Finish on a fixed click-noise + pink buffer. linux/amd64, GOAMD64=v1; MM_UPDATE_GOLDEN=1 rewrites.\n"+got+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(finishGoldenPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if want := lines[len(lines)-1]; got != want {
		t.Errorf("pcm_sha256 %s, golden %s", got, want)
	}
}

// TestFinish_FMAOff re-runs the golden under GODEBUG=cpu.fma=off.
func TestFinish_FMAOff(t *testing.T) {
	dsptest.RequireGolden(t)
	dsptest.ReexecFMAOff(t, `^TestFinish_Golden$`)
}
