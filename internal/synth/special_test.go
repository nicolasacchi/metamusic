package synth_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
	"github.com/nicolasacchi/metamusic/internal/analysis/npy"
	"github.com/nicolasacchi/metamusic/internal/synth"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

func TestSynthSpecial_ClipBursts(t *testing.T) {
	t.Parallel()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	p := filepath.Join(t.TempDir(), "clip.flac")
	if err := synth.Special(context.Background(), tools, p, synth.SpecialClipBursts, 6); err != nil {
		t.Fatal(err)
	}
	info, _ := readInfo(t, p)
	si := info.StreamInfo
	if si.SampleRate != 48000 || si.Channels != 2 || si.BitsPerSample != 24 || si.TotalSamples != 6*48000 {
		t.Errorf("streaminfo = %+v", si)
	}
	f, err := ffmpeg.New(context.Background(), tools)
	if err != nil {
		t.Fatal(err)
	}
	_, c, err := f.Signal(context.Background(), p, 48000, 6)
	if err != nil {
		t.Fatal(err)
	}
	if c.ClippedFrames < 18 || c.ClippedFrames > 22 || c.FullScaleFrames != c.ClippedFrames {
		t.Errorf("clipping = %+v", c)
	}
}

func TestSynthSpecial_Gap(t *testing.T) {
	t.Parallel()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	p := filepath.Join(t.TempDir(), "gap.flac")
	if err := synth.Special(context.Background(), tools, p, synth.SpecialGap, 10); err != nil {
		t.Fatal(err)
	}
	f, err := ffmpeg.New(context.Background(), tools)
	if err != nil {
		t.Fatal(err)
	}
	s, c, err := f.Signal(context.Background(), p, 48000, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Spans) != 1 || s.InternalS < 2.4 || s.InternalS > 2.6 || c.ClippedFrames != 0 || c.PeakDBFS > -13 || c.PeakDBFS < -15 {
		t.Errorf("silence = %+v clipping = %+v", s, c)
	}
	// atomic publish: no temp file is left behind
	if m, _ := filepath.Glob(p + ".*"); len(m) != 0 {
		t.Errorf("leftovers %v", m)
	}
}

func TestSynthSpecial_Invalid(t *testing.T) {
	t.Parallel()
	tools := testtools.Tools()
	for _, c := range []struct {
		dst  string
		kind synth.SpecialKind
		sec  float64
	}{
		{"relative.flac", synth.SpecialGap, 5},
		{filepath.Join(t.TempDir(), "x.flac"), synth.SpecialKind(99), 5},
		{filepath.Join(t.TempDir(), "x.flac"), synth.SpecialGap, 0},
	} {
		if err := synth.Special(context.Background(), tools, c.dst, c.kind, c.sec); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
}

func readNPY(t *testing.T, p string) npy.Array {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	a, err := npy.Read(bytes.NewReader(b), 1<<24)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestSemanticNPY_ReadBack(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "semantic.npy")
	o := synth.NPYOptions{TokensPerS: 50, Seconds: 12, Vocab: 1024, Seed: 7, LoopAtS: 3, Period: 25, Repeats: 12}
	if err := synth.SemanticNPY(p, o); err != nil {
		t.Fatal(err)
	}
	a := readNPY(t, p)
	if a.DType != "<i8" || len(a.Shape) != 2 || a.Shape[0] != 1 || a.Shape[1] != 600 {
		t.Fatalf("array = %v %v", a.DType, a.Shape)
	}
	for i := 150; i < 450-25; i++ {
		if a.Data[i] != a.Data[i+25] {
			t.Fatalf("loop broken at %d", i)
		}
	}
	for _, v := range a.Data {
		if v < 0 || v >= 1024 {
			t.Fatalf("token %d out of vocab", v)
		}
	}
	// deterministic, and different seeds differ
	q := filepath.Join(dir, "again.npy")
	if err := synth.SemanticNPY(q, o); err != nil {
		t.Fatal(err)
	}
	b1, _ := os.ReadFile(p)
	b2, _ := os.ReadFile(q)
	if !bytes.Equal(b1, b2) {
		t.Error("not deterministic")
	}
	o.Seed = 8
	if err := synth.SemanticNPY(q, o); err != nil {
		t.Fatal(err)
	}
	if b3, _ := os.ReadFile(q); bytes.Equal(b1, b3) {
		t.Error("seed ignored")
	}
	// loop-free: no repeats
	o.Repeats = 0
	if err := synth.SemanticNPY(q, o); err != nil {
		t.Fatal(err)
	}
	if got := readNPY(t, q); len(got.Data) != 600 {
		t.Errorf("len = %d", len(got.Data))
	}
}

func TestSemanticNPY_Invalid(t *testing.T) {
	t.Parallel()
	d := t.TempDir()
	for _, c := range []struct {
		dst string
		o   synth.NPYOptions
	}{
		{"rel.npy", synth.NPYOptions{TokensPerS: 50, Seconds: 1, Vocab: 10}},
		{filepath.Join(d, "a.npy"), synth.NPYOptions{TokensPerS: 0, Seconds: 1, Vocab: 10}},
		{filepath.Join(d, "b.npy"), synth.NPYOptions{TokensPerS: 50, Seconds: 1, Vocab: 0}},
		{filepath.Join(d, "c.npy"), synth.NPYOptions{TokensPerS: 50, Seconds: 1, Vocab: 10, Repeats: 5, Period: 0}},
		{filepath.Join(d, "e.npy"), synth.NPYOptions{TokensPerS: 50, Seconds: 1, Vocab: 10, Repeats: 5, Period: 20}},
	} {
		if err := synth.SemanticNPY(c.dst, c.o); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
}
