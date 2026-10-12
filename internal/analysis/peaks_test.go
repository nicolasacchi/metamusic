package analysis

import (
	"bytes"
	"encoding/json"
	"flag"
	"math"
	"os"
	"testing"
)

var update = flag.Bool("update", false, "update golden files")

// build feeds interleaved float32 frames in odd-sized chunks.
func build(t *testing.T, sr, ch int, total uint64, gen func(i, c int) float32) Peaks {
	t.Helper()
	b, err := NewPeaksBuilder(sr, ch, total)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]float32, 0, 1000*ch)
	for i := uint64(0); i < total; i++ {
		for c := 0; c < ch; c++ {
			buf = append(buf, gen(int(i), c))
		}
		if len(buf) >= 997*ch || i == total-1 {
			b.Write(buf)
			buf = buf[:0]
		}
	}
	return b.Finish()
}

func TestPeaks_SineAmplitude(t *testing.T) {
	p := build(t, 48000, 2, 48000*2, func(i, _ int) float32 {
		return float32(0.5 * math.Sin(2*math.Pi*440*float64(i)/48000))
	})
	for _, w := range []Waveform{p.Detail} { // overview pixels are shorter than a period
		for i := 0; i+1 < len(w.Data); i += 2 {
			if mn, mx := w.Data[i], w.Data[i+1]; mx < 63 || mx > 64 || mn < -64 || mn > -63 {
				t.Fatalf("pixel %d: min %d max %d", i/2, mn, mx)
			}
		}
	}
}

func TestPeaks_SilenceZero(t *testing.T) {
	p := build(t, 48000, 1, 48000, func(int, int) float32 { return 0 })
	for _, d := range append(append([]int8{}, p.Overview.Data...), p.Detail.Data...) {
		if d != 0 {
			t.Fatal("non-zero in silence")
		}
	}
}

func TestPeaks_Lengths(t *testing.T) {
	zero := func(int, int) float32 { return 0 }
	p := build(t, 48000, 1, 480000, zero)
	if p.Detail.SamplesPerPixel != 2400 || p.Detail.Length != 200 {
		t.Fatalf("detail %+v", p.Detail.SamplesPerPixel)
	}
	if p.Overview.SamplesPerPixel != 235 || p.Overview.Length != 2043 {
		t.Fatalf("overview spp %d len %d", p.Overview.SamplesPerPixel, p.Overview.Length)
	}
	if len(p.Detail.Data) != 400 || len(p.Overview.Data) != 2*2043 {
		t.Fatal("data lengths")
	}
	if p.DurationS != 10 {
		t.Fatalf("duration %v", p.DurationS)
	}
	p = build(t, 44100, 1, 44100, zero)
	if p.Detail.SamplesPerPixel != 2205 || p.Detail.Length != 20 {
		t.Fatalf("44.1k detail spp %d len %d", p.Detail.SamplesPerPixel, p.Detail.Length)
	}
	// tiny file: overview spp is at least 1
	p = build(t, 48000, 1, 100, zero)
	if p.Overview.SamplesPerPixel != 1 || p.Overview.Length != 100 {
		t.Fatalf("tiny overview %+v", p.Overview)
	}
}

func TestPeaks_MultiChannelMinMax(t *testing.T) {
	p := build(t, 48000, 2, 48000, func(_, c int) float32 {
		if c == 0 {
			return 0.8
		}
		return -0.4
	})
	for _, w := range []Waveform{p.Overview, p.Detail} {
		for i := 0; i+1 < len(w.Data); i += 2 {
			if w.Data[i] != -51 || w.Data[i+1] != 102 {
				t.Fatalf("pair %d = (%d,%d)", i/2, w.Data[i], w.Data[i+1])
			}
		}
		if w.Channels != 1 {
			t.Fatal("channels must be 1")
		}
	}
}

func TestPeaks_Clamp(t *testing.T) {
	p := build(t, 48000, 1, 4800, func(i, _ int) float32 {
		if i%2 == 0 {
			return 3
		}
		return -3
	})
	if p.Detail.Data[0] != -127 || p.Detail.Data[1] != 127 {
		t.Fatalf("clamp: %v", p.Detail.Data[:2])
	}
}

func TestPeaks_ShortDecodeAndExtraFrames(t *testing.T) {
	b, _ := NewPeaksBuilder(48000, 1, 4800)
	b.Write(make([]float32, 2400)) // half decoded: tail pixels stay zero
	p := b.Finish()
	if p.Detail.Length != 2 || len(p.Detail.Data) != 4 {
		t.Fatalf("%+v", p.Detail)
	}
	b, _ = NewPeaksBuilder(48000, 1, 100)
	ones := make([]float32, 500)
	for i := range ones {
		ones[i] = 1
	}
	b.Write(ones) // more than declared: ignored, no panic
	_ = b.Finish()
}

func TestPeaks_BadParams(t *testing.T) {
	if _, err := NewPeaksBuilder(0, 2, 10); err == nil {
		t.Fatal("sample rate 0")
	}
	if _, err := NewPeaksBuilder(48000, 0, 10); err == nil {
		t.Fatal("channels 0")
	}
}

func TestPeaks_JSONGolden(t *testing.T) {
	const total = 8000
	p := build(t, 8000, 1, total, func(i, _ int) float32 {
		return float32(0.9 * math.Sin(2*math.Pi*5*float64(i)/8000) * float64(i) / total)
	})
	got, err := MarshalPeaks(p)
	if err != nil {
		t.Fatal(err)
	}
	if got[len(got)-1] != '\n' || bytes.Count(got, []byte("\n")) != 1 {
		t.Fatal("must be compact JSON plus one newline")
	}
	var back map[string]any
	if err := json.Unmarshal(got, &back); err != nil || back["format"] != PeaksFormat {
		t.Fatalf("format key: %v %v", back["format"], err)
	}
	const golden = "testdata/peaks.golden.json"
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden mismatch (run with -update)\n got %.200s\nwant %.200s", got, want)
	}
}
