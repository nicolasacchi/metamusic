package pv_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
	"github.com/nicolasacchi/metamusic/internal/dsp/pv"
)

// TestRenderPrototypes is a dev-only helper for the T1-X0 listening gate; it lives in a _test.go file so it can never reach
// the release binary. It renders the prototypes of the phase vocoder as 24-bit stereo WAV files: stretch-x1.25.wav, stretch-x0.80.wav,
// shift+3-plain.wav, shift+3-formant.wav, shift-3-plain.wav and shift-3-formant.wav (formant correction holds the formants
// where they were), next to source.wav, the untouched excerpt, for A/B listening. It does nothing unless MM_PV_PROTO_OUT is set:
//
//	MM_PV_PROTO_OUT=/some/dir go test ./internal/dsp/pv/ -run TestRenderPrototypes -count=1 -v
//	    synthetic input (a few seconds of dsptest signals): exercises the code path only
//	MM_PV_PROTO_OUT=/some/dir MM_PV_PROTO_IN=/path/to/take.flac [MM_PV_PROTO_SECONDS=30] [MM_PV_PROTO_START=0] go test ...
//	    the first MM_PV_PROTO_SECONDS seconds (default 30) of a FLAC (or any file ffmpeg reads) from START seconds
//
// The FLAC is only read, through ffmpeg (bin/ffmpeg when present, else PATH); nothing is written next to it.
func TestRenderPrototypes(t *testing.T) {
	out := os.Getenv("MM_PV_PROTO_OUT")
	if out == "" {
		t.Skip("MM_PV_PROTO_OUT not set: dev-only prototype renderer")
	}
	const sr = 48000
	var src pcm.Buffer
	if in := os.Getenv("MM_PV_PROTO_IN"); in != "" {
		secs := os.Getenv("MM_PV_PROTO_SECONDS")
		if secs == "" {
			secs = "30"
		}
		start := os.Getenv("MM_PV_PROTO_START")
		if start == "" {
			start = "0"
		}
		src = decodeStereo(t, in, start, secs, sr)
	} else {
		src = syntheticSource(sr)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, b pcm.Buffer) {
		path := filepath.Join(out, name+".wav")
		if err := os.WriteFile(path, wav24(b), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %.2f s", path, float64(len(b.Ch[0]))/float64(b.Rate))
	}
	write("source", src)
	p := pv.Params{N: 4096, Lock: true, Rate: sr}
	each := func(f func(x []float64) []float64) pcm.Buffer {
		return pcm.Buffer{Rate: sr, Ch: [2][]float64{f(src.Ch[0]), f(src.Ch[1])}}
	}
	for _, a := range []float64{1.25, 0.8} {
		write(fmt.Sprintf("stretch-x%.2f", a), each(func(x []float64) []float64 {
			y, err := pv.StretchErr(x, func(int) float64 { return a }, p)
			if err != nil {
				t.Fatal(err)
			}
			return y
		}))
	}
	for _, st := range []int{3, -3} {
		rho := dsp.Pow(2, float64(st)/12)
		for _, formant := range []bool{false, true} {
			q := p
			name := fmt.Sprintf("shift%+d-plain", st)
			if formant {
				q.FormantRatio = func(int) float64 { return 1 }
				name = fmt.Sprintf("shift%+d-formant", st)
			}
			write(name, each(func(x []float64) []float64 {
				y, err := pv.ShiftErr(x, func(int) float64 { return rho }, q)
				if err != nil {
					t.Fatal(err)
				}
				return y
			}))
		}
	}
}

// syntheticSource is a short stereo signal with a pitched part, a vowel-like part and clicks, from dsptest only.
func syntheticSource(sr int) pcm.Buffer {
	tone := dsptest.Sine(sr, 220, 0.3, 2)
	for i, v := range dsptest.Sine(sr, 330, 0.2, 2) {
		tone[i] += v
	}
	pink := dsptest.Scale(dsptest.Pink(1, dsptest.Frames(sr, 2)), 0.2)
	clicks := dsptest.Clicks(sr, 120, 0.5, 2)
	l := dsptest.Concat(tone, pink, clicks)
	r := dsptest.Concat(dsptest.Scale(tone, 0.8), dsptest.Scale(pink, 0.8), dsptest.Scale(clicks, 0.8))
	return dsptest.Stereo(sr, l, r)
}

func ffmpegPath() string {
	for _, c := range []string{"bin/ffmpeg", "../../../bin/ffmpeg"} {
		if _, err := os.Stat(c); err == nil {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	return "ffmpeg"
}

func decodeStereo(t *testing.T, path, start, secs string, sr int) pcm.Buffer {
	t.Helper()
	cmd := exec.Command(ffmpegPath(), "-v", "error", "-ss", start, "-t", secs, "-i", path, "-f", "f32le", "-ac", "2", "-ar", fmt.Sprint(sr), "-") //nolint:gosec // dev helper: operator-chosen input
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg: %v: %s", err, stderr.String())
	}
	b, err := pcm.ReadF32LE(&stdout, sr, 2, stdout.Len()/8)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// wav24 is a 24-bit PCM stereo WAV of b (clamped like WritePCM24).
func wav24(b pcm.Buffer) []byte {
	var data bytes.Buffer
	if _, err := pcm.WritePCM24(&data, b); err != nil {
		panic(err)
	}
	var w bytes.Buffer
	le := func(v any) { _ = binary.Write(&w, binary.LittleEndian, v) }
	w.WriteString("RIFF")
	le(uint32(36 + data.Len())) // #nosec G115 -- dev helper
	w.WriteString("WAVEfmt ")
	le(uint32(16))
	le(uint16(1))
	le(uint16(2))
	le(uint32(b.Rate)) // #nosec G115
	le(uint32(b.Rate * 6))
	le(uint16(6))
	le(uint16(24))
	w.WriteString("data")
	le(uint32(data.Len())) // #nosec G115
	w.Write(data.Bytes())
	return w.Bytes()
}
