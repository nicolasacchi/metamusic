package transform

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
)

// rawMedia is a Media whose Decode writes a fixed float32 stream.
type rawMedia struct{ samples []float32 }

func (m rawMedia) Decode(_ context.Context, _, dst string) (time.Duration, error) {
	b := make([]byte, 4*len(m.samples))
	for i, v := range m.samples {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(v))
	}
	return 0, os.WriteFile(dst, b, 0o600)
}
func (rawMedia) Encode(context.Context, string, string, int) (time.Duration, error) { return 0, nil }
func (rawMedia) Versions(context.Context) ToolVersions                              { return ToolVersions{} }

func internalSvc(t *testing.T, m Media, rs Resampler) (*Service, *staged) {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	st := &staged{rel: "incoming/transform-x", abs: filepath.Join(dir, "incoming", "transform-x"), rate: 48000}
	if err := root.MkdirAll(st.rel, 0o700); err != nil {
		t.Fatal(err)
	}
	return New(Options{Data: root, DataDir: dir, Media: m, Resample: rs}), st
}

func readF32(t *testing.T, s *Service, rel string) []float32 {
	t.Helper()
	b, err := s.o.Data.ReadFile(rel)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out
}

// TestDecode_MonoDuplicated (D-T1-19): a mono master is decoded at its native channel count and duplicated to both channels in
// Go, never up-mixed by ffmpeg.
func TestDecode_MonoDuplicated(t *testing.T) {
	s, st := internalSvc(t, rawMedia{samples: []float32{0.1, -0.2, 0.3}}, nil)
	in := &inputRow{channels: 1, rate: 48000, totalSamples: 3, masterPath: "audio/masters/m.flac"}
	if f := s.decodeInput(context.Background(), st, in, 0, &cpuAcct{}); f != nil {
		t.Fatal(f)
	}
	got := readF32(t, s, st.rel+"/in-0.f32")
	want := []float32{0.1, 0.1, -0.2, -0.2, 0.3, 0.3}
	if !slices.Equal(got, want) || in.frames != 3 || in.resampledFrom != 0 {
		t.Errorf("got %v frames %d resampledFrom %d", got, in.frames, in.resampledFrom)
	}
	if _, err := s.o.Data.Stat(st.rel + "/in-0.raw"); err == nil {
		t.Error("the raw decode output was left behind")
	}
}

// TestDecode_StereoPassThrough: a stereo master at the working rate is used as decoded.
func TestDecode_StereoPassThrough(t *testing.T) {
	s, st := internalSvc(t, rawMedia{samples: []float32{1, 2, 3, 4}}, nil)
	in := &inputRow{channels: 2, rate: 48000, totalSamples: 2}
	if f := s.decodeInput(context.Background(), st, in, 0, &cpuAcct{}); f != nil {
		t.Fatal(f)
	}
	if got := readF32(t, s, st.rel+"/in-0.f32"); !slices.Equal(got, []float32{1, 2, 3, 4}) {
		t.Errorf("got %v", got)
	}
}

// TestDecode_ResamplesASecondRate (D-T1-19): an input at another rate than the working rate goes through the Resampler hook and
// is recorded as resampled_from; without a resampler it is refused, never decoded through ffmpeg's resampler.
func TestDecode_ResamplesASecondRate(t *testing.T) {
	double := func(b pcm.Buffer, to int) (pcm.Buffer, error) {
		out := pcm.Buffer{Rate: to}
		for c := 0; c < 2; c++ {
			for _, v := range b.Ch[c] {
				out.Ch[c] = append(out.Ch[c], v, v)
			}
		}
		return out, nil
	}
	s, st := internalSvc(t, rawMedia{samples: []float32{1, 2, 3, 4}}, double)
	in := &inputRow{channels: 2, rate: 24000, totalSamples: 2}
	if f := s.decodeInput(context.Background(), st, in, 0, &cpuAcct{}); f != nil {
		t.Fatal(f)
	}
	if got := readF32(t, s, st.rel+"/in-0.f32"); !slices.Equal(got, []float32{1, 2, 1, 2, 3, 4, 3, 4}) || in.frames != 4 || in.resampledFrom != 24000 {
		t.Errorf("got %v frames %d resampledFrom %d", got, in.frames, in.resampledFrom)
	}
	s2, st2 := internalSvc(t, rawMedia{samples: []float32{1, 2}}, nil)
	in2 := &inputRow{channels: 2, rate: 24000, totalSamples: 1}
	if f := s2.decodeInput(context.Background(), st2, in2, 0, &cpuAcct{}); f == nil || f.code != ErrTransformFailed {
		t.Errorf("no resampler: %v", f)
	}
}

// TestDecode_RefusesMoreThanStereo: transforms take mono or stereo.
func TestDecode_RefusesMoreThanStereo(t *testing.T) {
	s, st := internalSvc(t, rawMedia{samples: make([]float32, 6)}, nil)
	if f := s.decodeInput(context.Background(), st, &inputRow{channels: 6, rate: 48000}, 0, &cpuAcct{}); f == nil || f.code != ErrInvalidTransform {
		t.Errorf("6 channels: %v", f)
	}
}

// TestEncode_Argv: the encode argv is the literal 24-bit command of C210.6(4) (verified at T1-X0: metaflac --show-bps = 24),
// and the decode argv has no -ar and no -ac: decode is native-format and exact (D-T1-19).
func TestEncode_Argv(t *testing.T) {
	enc := strings.Join(encodeArgs("/w/out.pcm24", "/w/out.flac", 44100), " ")
	want := "-f s24le -ar 44100 -ac 2 -i file:/w/out.pcm24 -c:a flac -compression_level 8 -sample_fmt s32 -bits_per_raw_sample 24 file:/w/out.flac"
	if !strings.HasSuffix(enc, want) {
		t.Errorf("encode argv = %q, want suffix %q", enc, want)
	}
	dec := decodeArgs("/m/in.flac", "/w/in-0.raw")
	for _, a := range dec {
		if a == "-ar" || a == "-ac" || a == "-af" || a == "-sample_fmt" {
			t.Errorf("decode argv has %s: %v", a, dec)
		}
	}
	if !slices.Contains(dec, "f32le") || dec[len(dec)-1] != "file:/w/in-0.raw" {
		t.Errorf("decode argv = %v", dec)
	}
}

// TestUnitsBudget: the work meter's budget is the CPU budget less the tool shares, never below one.
func TestUnitsBudget(t *testing.T) {
	if got := unitsBudget(60_000, 20, 20); got <= 0 || got >= int64(60_000*1e6/nsPerUnit) {
		t.Errorf("budget %d", got)
	}
	if unitsBudget(10, 600, 900) != 1 {
		t.Error("a budget eaten by the tool shares must be 1, not unlimited or negative")
	}
	if wallDeadline(1000) != 60*time.Second || wallDeadline(100_000) != 300*time.Second {
		t.Errorf("deadlines %v %v", wallDeadline(1000), wallDeadline(100_000))
	}
}

// TestMajorMinor: a synth version token reduces to its major.minor.
func TestMajorMinor(t *testing.T) {
	for in, want := range map[string]string{"FluidSynth 2.3.4": "2.3", "2.4": "2.4", "x": "x", "fluidsynth 3.0.0-rc1": "3.0"} {
		if got := majorMinor(in); got != want {
			t.Errorf("majorMinor(%q) = %q, want %q", in, got, want)
		}
	}
}
