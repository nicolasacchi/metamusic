package safety

import (
	"bytes"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

type oracle struct {
	name         string
	peakDBFS     float64
	both         bool
	lufs, tpDBTP float64
}

func readOracles(t *testing.T) []oracle {
	t.Helper()
	raw, err := os.ReadFile("testdata/lufs-oracles.txt")
	if err != nil {
		t.Fatal(err)
	}
	var out []oracle
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) != 5 || strings.HasPrefix(f[0], "#") {
			continue
		}
		num := func(s string) float64 {
			v, err := strconv.ParseFloat(s, 64)
			if err != nil {
				t.Fatalf("bad oracle line %q: %v", line, err)
			}
			return v
		}
		out = append(out, oracle{f[0], num(f[1]), f[2] == "both", num(f[3]), num(f[4])})
	}
	if len(out) != 3 {
		t.Fatalf("%d oracles, want 3", len(out))
	}
	return out
}

// sine997 builds the oracle signals: 997 Hz, 48 kHz, 10 s, on both channels or on the left only.
func sine997(peakDBFS float64, both bool, secs float64) pcm.Buffer {
	x := dsptest.Sine(48000, 997, dsptest.DBToLin(peakDBFS), secs)
	if both {
		return dsptest.Dual(48000, x)
	}
	return dsptest.Stereo(48000, x, dsptest.Silence(len(x)))
}

func TestLUFS_Sine(t *testing.T) {
	t.Parallel()
	for _, o := range readOracles(t) {
		got, ok := LUFS(sine997(o.peakDBFS, o.both, 10))
		if !ok || math.Abs(got-o.lufs) > 0.05 {
			t.Errorf("%s: LUFS %.3f (ok=%v), ffmpeg ebur128 %.3f +- 0.05", o.name, got, ok, o.lufs)
		}
		// the plan's rounded targets hold as well: -20.0, -3.01 (-3.0), -23.0 (-23.01), each +- 0.05
		round := map[string]float64{"both_20": -20.0, "both_3": -3.01, "one_20": -23.01}[o.name]
		if math.Abs(got-round) > 0.05 {
			t.Errorf("%s: LUFS %.3f, plan %.2f +- 0.05", o.name, got, round)
		}
		if tp := TruePeakDBTP(sine997(o.peakDBFS, o.both, 1)); math.Abs(tp-o.tpDBTP) > 0.1 {
			t.Errorf("%s: true peak %.3f, ffmpeg %.1f", o.name, tp, o.tpDBTP)
		}
	}
}

func TestLUFS_GatesSilence(t *testing.T) {
	t.Parallel()
	loud := dsptest.Sine(48000, 997, dsptest.DBToLin(-20), 10)
	b := dsptest.Dual(48000, dsptest.Concat(loud, dsptest.Silence(len(loud))))
	got, ok := LUFS(b)
	if !ok || math.Abs(got+20) > 0.1 {
		t.Errorf("10 s sine + 10 s silence: %.3f LUFS (ok=%v), want -20.0 +- 0.1", got, ok)
	}
}

func TestLUFS_NotOK(t *testing.T) {
	t.Parallel()
	if _, ok := LUFS(dsptest.Dual(48000, dsptest.Silence(48000*2))); ok {
		t.Error("silence has a loudness")
	}
	if _, ok := LUFS(dsptest.Dual(48000, dsptest.Sine(48000, 997, dsptest.DBToLin(-90), 2))); ok {
		t.Error("a -90 dBFS sine is under the absolute gate")
	}
	if _, ok := LUFS(pcm.Buffer{Rate: 48000}); ok {
		t.Error("an empty buffer has a loudness")
	}
}

func TestLUFS_ShortBuffer(t *testing.T) {
	t.Parallel()
	// 200 ms is under one gating block: measured as one zero-padded block, so a short render is not "silent"
	got, ok := LUFS(dsptest.Dual(48000, dsptest.Sine(48000, 997, dsptest.DBToLin(-10), 0.2)))
	if !ok || got > -10 || got < -16 {
		t.Errorf("short buffer: %.2f (ok=%v)", got, ok)
	}
}

func TestLUFS_OtherRates(t *testing.T) {
	t.Parallel()
	// the K-weighting is computed from the rate: the same 997 Hz sine reads alike at 16, 44.1 and 96 kHz
	for _, rate := range []int{16000, 44100, 96000} {
		b := dsptest.Dual(rate, dsptest.Sine(rate, 997, dsptest.DBToLin(-20), 5))
		if got, ok := LUFS(b); !ok || math.Abs(got+20.0) > 0.1 {
			t.Errorf("rate %d: %.3f LUFS", rate, got)
		}
	}
}

// TestLUFS_MatchesFFmpeg compares a noise burst plus sine mix with ffmpeg's ebur128 integrated value within 0.1 LU.
func TestLUFS_MatchesFFmpeg(t *testing.T) {
	testtools.Require(t, "ffmpeg")
	const rate = 48000
	l := dsptest.Concat(dsptest.Sine(rate, 997, 0.1, 3), dsptest.Scale(dsptest.Pink(1, rate), 0.5), dsptest.Sine(rate, 220, 0.3, 2))
	r := dsptest.Concat(dsptest.Sine(rate, 1500, 0.05, 3), dsptest.Scale(dsptest.Pink(2, rate), 0.4), dsptest.Sine(rate, 330, 0.2, 2))
	b := dsptest.Stereo(rate, l, r)
	var raw bytes.Buffer
	if _, err := pcm.WritePCM24(&raw, b); err != nil {
		t.Fatal(err)
	}
	b, err := pcm.ReadPCM24(bytes.NewReader(raw.Bytes()), rate, b.Frames()) // what ffmpeg will see
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(testtools.Tools().FFmpeg, "-nostats", "-hide_banner", "-f", "s24le", "-ar", "48000", "-ac", "2", "-i", "-",
		"-af", "ebur128=metadata=1,ametadata=print:key=lavfi.r128.I:file=-", "-f", "null", "-")
	cmd.Stdin = bytes.NewReader(raw.Bytes())
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ffmpeg: %v", err)
	}
	var ref float64
	var found bool
	for _, line := range strings.Split(string(out), "\n") {
		if _, v, ok := strings.Cut(line, "lavfi.r128.I="); ok {
			ref, _ = strconv.ParseFloat(strings.TrimSpace(v), 64)
			found = true
		}
	}
	if !found {
		t.Fatalf("no lavfi.r128.I in ffmpeg output:\n%s", out)
	}
	got, ok := LUFS(b)
	if !ok || math.Abs(got-ref) > 0.1 {
		t.Errorf("ours %.3f, ffmpeg ebur128 %.3f (ok=%v)", got, ref, ok)
	}
}
