package tape

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"testing"
)

// s32le builds a stereo stream from 24-bit values (left aligned in 32 bits, as ffmpeg writes them).
func s32le(vals []int32, shift uint) []byte {
	b := make([]byte, 0, 4*len(vals))
	for _, v := range vals {
		u := uint32(v << shift)
		b = append(b, byte(u), byte(u>>8), byte(u>>16), byte(u>>24))
	}
	return b
}

func s24le(vals []int32) []byte {
	b := make([]byte, 0, 3*len(vals))
	for _, v := range vals {
		b = append(b, byte(v), byte(v>>8), byte(v>>16))
	}
	return b
}

func rampSamples(n int, rng *rand.Rand) []int32 {
	v := make([]int32, n)
	for i := range v {
		v[i] = int32(rng.IntN(1<<24)) - 1<<23
		if v[i] > 8388607 {
			v[i] = 8388607
		}
	}
	return v
}

// runStage feeds in to a new stage in the given piece sizes (cycled) and returns the output, frames and peak.
func runStage(t *testing.T, in []byte, gain int, sizes []int) (out []byte, frames int64, peak int32) {
	t.Helper()
	var buf bytes.Buffer
	g := newGainStage(&buf, gain)
	for i := 0; len(in) > 0; i++ {
		n := min(sizes[i%len(sizes)], len(in))
		if w, err := g.Write(in[:n]); err != nil || w != n {
			t.Fatalf("write %d bytes: %d, %v", n, w, err)
		}
		in = in[n:]
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), g.frames, g.peak
}

func TestPipeline_GainStage_BoundariesAndGapless(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(1, 2))
	samples := rampSamples(2*50_001, rng) // 50 001 frames: more than one internal block, an odd count
	for _, c := range []struct {
		name  string
		shift uint
		gain  int
	}{{"24-bit gain 0", 8, 0}, {"24-bit cut", 8, -450}, {"16-bit left aligned", 16, -120}} {
		t.Run(c.name, func(t *testing.T) {
			src := samples
			if c.shift == 16 { // 16-bit values: only the top 16 bits of the 24 are used
				src = make([]int32, len(samples))
				for i, v := range samples {
					src[i] = v >> 8
				}
			}
			in := s32le(src, c.shift)
			want := make([]int32, len(src))
			for i, v := range src {
				want[i] = v
				if c.shift == 16 {
					want[i] = v << 8
				}
			}
			ApplyGain(want, want, c.gain)
			one, frames, peak := runStage(t, in, c.gain, []int{len(in)})
			if !bytes.Equal(one, s24le(want)) || frames != 50_001 {
				t.Fatalf("one-shot differs from the reference (%d frames)", frames)
			}
			var wantPeak int32
			for _, v := range want {
				wantPeak = max(wantPeak, abs32(v))
			}
			if peak != wantPeak {
				t.Fatalf("peak %d, want %d", peak, wantPeak)
			}
			for _, sizes := range [][]int{{1}, {3}, {7, 1, 5}, {8}, {9}, {4097}, {1 << 16, 3}, {2, 13, 100_003}} {
				got, fr, pk := runStage(t, in, c.gain, sizes)
				if !bytes.Equal(got, one) || fr != frames || pk != peak {
					t.Fatalf("split %v differs from the one-shot conversion", sizes)
				}
			}
		})
	}
}

// Two items back to back through two stages into one writer: the output is the plain concatenation, nothing in between.
func TestPipeline_GainStage_Concatenation(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(3, 4))
	a, b := rampSamples(2*1001, rng), rampSamples(2*777, rng)
	var out bytes.Buffer
	for _, s := range []struct {
		v    []int32
		gain int
	}{{a, -200}, {b, 0}} {
		g := newGainStage(&out, s.gain)
		if _, err := g.Write(s32le(s.v, 8)); err != nil || g.Close() != nil {
			t.Fatal(err)
		}
	}
	wa, wb := append([]int32(nil), a...), append([]int32(nil), b...)
	ApplyGain(wa, wa, -200)
	if !bytes.Equal(out.Bytes(), append(s24le(wa), s24le(wb)...)) {
		t.Fatal("not a plain concatenation")
	}
}

func TestPipeline_GainStage_ClipAndPartialFrame(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	g := newGainStage(&out, 600) // +6 dB on a full-scale sample
	_, err := g.Write(s32le([]int32{8388607, 100}, 8))
	if !errors.Is(err, errClipped) {
		t.Fatalf("err %v", err)
	}
	if _, err := g.Write(s32le([]int32{1, 1}, 8)); !errors.Is(err, errClipped) || g.Close() == nil {
		t.Fatal("the failure must be sticky")
	}
	g = newGainStage(&out, 0)
	if _, err := g.Write(make([]byte, 11)); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); !errors.Is(err, errPartialFrame) {
		t.Fatalf("close: %v", err)
	}
}

func TestPipeline_GainStage_OutputChunks(t *testing.T) {
	t.Parallel()
	var sizes []int
	g := newGainStage(writerFunc(func(p []byte) (int, error) { sizes = append(sizes, len(p)); return len(p), nil }), 0)
	if _, err := g.Write(make([]byte, 8*(Chunk/3))); err != nil { // 2 Chunk-ish worth of 4-byte samples
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	for i, n := range sizes {
		if n < Chunk && i != len(sizes)-1 {
			t.Fatalf("write %d is %d bytes, below Chunk", i, n)
		}
	}
	if len(sizes) < 2 {
		t.Fatalf("expected several writes, got %v", sizes)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
