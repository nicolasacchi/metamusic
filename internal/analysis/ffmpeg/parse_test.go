package ffmpeg

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func near(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/ebur128_n9.0.1.txt")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestParseLoudness_Golden uses a capture of ffmpeg 9.0.1 for a 4 s stereo
// sine (L 0.5, R 0.25) that is silent for the first 1.2 s.
func TestParseLoudness_Golden(t *testing.T) {
	l, err := ParseLoudness(bytes.NewReader(fixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	near(t, "I", l.IntegratedLUFS, -8.299, 1e-9)
	near(t, "LRA", l.LRA, 1.76, 1e-9)
	near(t, "low", l.LRALowLUFS, -10.28, 1e-9)
	near(t, "high", l.LRAHighLUFS, -8.52, 1e-9)
	near(t, "TP", l.TruePeakDBTP, 20*math.Log10(0.5), 1e-9)
	near(t, "SP", l.SamplePeakDBFS, 20*math.Log10(0.5), 1e-9)
	near(t, "STmax", l.ShortTermMaxLUFS, -8.355, 1e-9)
	want := []float64{-70.0, -70.0, -10.0, -8.4} // -120.691 is clamped to -70.0
	if !reflect.DeepEqual(l.ShortTerm1Hz, want) {
		t.Fatalf("curve = %v, want %v", l.ShortTerm1Hz, want)
	}
}

type frame struct {
	t float64
	f map[string]string
}

func frames(t *testing.T, b []byte) []frame {
	t.Helper()
	var out []frame
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "frame:") {
			fs := strings.Fields(line)
			v, err := strconv.ParseFloat(strings.TrimPrefix(fs[2], "pts_time:"), 64)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, frame{t: v, f: map[string]string{}})
		} else if k, v, ok := strings.Cut(line, "="); ok {
			out[len(out)-1].f[k] = v
		}
	}
	return out
}

func render(fr []frame) []byte {
	var sb strings.Builder
	for i, f := range fr {
		fmt.Fprintf(&sb, "frame:%-4d pts:%-7d pts_time:%s\n", i, int(f.t*48000), strconv.FormatFloat(f.t, 'g', -1, 64))
		for _, k := range []string{"lavfi.r128.M", "lavfi.r128.S", "lavfi.r128.I", "lavfi.r128.LRA", "lavfi.r128.LRA.low", "lavfi.r128.LRA.high",
			"lavfi.r128.sample_peak", "lavfi.r128.true_peak"} {
			fmt.Fprintf(&sb, "%s=%s\n", k, f.f[k])
		}
	}
	return []byte(sb.String())
}

func TestParseLoudness_OtherFraming(t *testing.T) {
	base, err := ParseLoudness(bytes.NewReader(fixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	src := frames(t, fixture(t))
	var fine, irregular []frame
	for i, f := range src {
		fine = append(fine, f)
		next := frame{t: f.t + 0.05, f: f.f}
		if i+1 < len(src) {
			fine = append(fine, next) // 50 ms framing: the half-step frame repeats the values
		}
		// irregular: whole-second frames keep their time; others are nudged by an irregular amount
		g := frame{t: f.t, f: f.f}
		if math.Abs(f.t-math.Round(f.t)) > 1e-9 && i != len(src)-1 {
			g.t = f.t + 0.01*float64(i%3)
		}
		irregular = append(irregular, g)
	}
	// a 50 ms capture ends one 50 ms step later
	fine = append(fine, frame{t: src[len(src)-1].t + 0.05, f: src[len(src)-1].f})
	for name, fr := range map[string][]frame{"50ms": fine, "irregular": irregular} {
		l, err := ParseLoudness(bytes.NewReader(render(fr)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(l.ShortTerm1Hz, base.ShortTerm1Hz) {
			t.Errorf("%s curve = %v, want %v", name, l.ShortTerm1Hz, base.ShortTerm1Hz)
		}
		if l.IntegratedLUFS != base.IntegratedLUFS || l.ShortTermMaxLUFS != base.ShortTermMaxLUFS {
			t.Errorf("%s scalars differ", name)
		}
	}
}

func TestParseLoudness_TruncatedOutput(t *testing.T) {
	for name, in := range map[string]string{
		"empty":      "",
		"no frames":  "lavfi.r128.I=-23.0\n",
		"no I":       "frame:0    pts:0       pts_time:0\nlavfi.r128.S=-30.0\n",
		"bad number": "frame:0    pts:0       pts_time:0\nlavfi.r128.I=abc\n",
		"bad pts":    "frame:0    pts:0       pts_time:x\nlavfi.r128.I=-23.0\n",
	} {
		if _, err := ParseLoudness(strings.NewReader(in)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestParseLoudness_PeakZeroAndNaN(t *testing.T) {
	in := "frame:0    pts:0       pts_time:0\nlavfi.r128.S=-120.691\nlavfi.r128.I=-70.000\nlavfi.r128.LRA=0.000\n" +
		"lavfi.r128.LRA.low=0.000\nlavfi.r128.LRA.high=0.000\nlavfi.r128.sample_peak=0.000\nlavfi.r128.true_peak=0.000\n"
	l, err := ParseLoudness(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if l.TruePeakDBTP != -120 || l.SamplePeakDBFS != -120 {
		t.Fatalf("zero peak must be -120: %+v", l)
	}
	if l.IntegratedLUFS != -70 {
		t.Fatal("I")
	}
}

func TestVersionToken(t *testing.T) {
	for in, want := range map[string]string{
		"ffmpeg version n9.0.1 Copyright (c) 2000-2026 the FFmpeg developers\nbuilt with gcc": "n9.0.1",
		"ffmpeg version 7.1.2-0ubuntu1 Copyright (c) 2000-2025":                               "7.1.2-0ubuntu1",
		"ffmpeg version N-118123-gabcdef Copyright (c)":                                       "N-118123-gabcdef",
	} {
		got, err := parseVersion([]byte(in))
		if err != nil || got != want {
			t.Errorf("parseVersion(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "something else\n", "ffmpeg version \n"} {
		if _, err := parseVersion([]byte(bad)); err == nil {
			t.Errorf("parseVersion(%q) should fail", bad)
		}
	}
}
