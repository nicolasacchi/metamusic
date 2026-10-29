package librarytest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/synth"
)

// L2AudioSource is one synthetic master of FixtureL2Audio. Rel is relative to L2Audio.Dir (the data directory a tape pipeline
// is configured with).
type L2AudioSource struct {
	Name     string
	Rel      string
	SHA256   string
	Bytes    int64
	Lossy    bool // decoded from MP3
	Rate     int
	Bits     int
	Channels int
	Seconds  float64
	// LUFS is the integrated loudness the tone was calibrated to (within 0.05 LU); Calibrated is false for a derived source
	// whose level is not set (the mono one).
	LUFS       float64
	Calibrated bool
	// Samples is the exact length in samples at Rate for a FLAC source, 0 for the MP3.
	Samples int64
}

// L2Audio is the audio side of the L2 fixture: real files of known level for the tape pipeline (media tier).
type L2Audio struct {
	Dir     string // absolute data directory holding the sources
	Sources []L2AudioSource
}

// Source returns the source called name, failing the test when there is none.
func (a L2Audio) Source(t testing.TB, name string) L2AudioSource {
	t.Helper()
	for _, s := range a.Sources {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no audio source %q", name)
	return L2AudioSource{}
}

// Abs is the absolute path of a source.
func (a L2Audio) Abs(s L2AudioSource) string { return filepath.Join(a.Dir, s.Rel) }

// FixtureL2Audio renders the synthetic sources a tape export is verified on, into a fresh t.TempDir() (C128.6). They are made
// with ffmpeg alone, so tools must hold a working ffmpeg; the caller skips or
// fails the test when it does not (internal/testtools). Sources, all synthetic tones:
//
//	loud   48 kHz 24-bit stereo FLAC, 6 s, -12 LUFS   (needs a cut)
//	quiet  48 kHz 24-bit stereo FLAC, 5 s, -20 LUFS   (needs a boost)
//	mid    48 kHz 24-bit stereo FLAC, 4 s, -16 LUFS
//	lossy  44.1 kHz stereo MP3, 3 s, -15 LUFS         (lossy, resampled)
//	cd     44.1 kHz 16-bit stereo FLAC, 3 s, -14 LUFS (resampled)
//	mono   48 kHz 24-bit mono FLAC, 3 s               (upmixed; level not calibrated)
//
// Every file is mode 0444, like a master.
func FixtureL2Audio(t testing.TB, tools mediatool.Tools) L2Audio {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	dir := t.TempDir()
	a := L2Audio{Dir: dir}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	add := func(s L2AudioSource) {
		t.Helper()
		abs := filepath.Join(dir, s.Rel)
		b, err := os.ReadFile(abs)
		must(err)
		sum := sha256.Sum256(b)
		s.SHA256, s.Bytes = hex.EncodeToString(sum[:]), int64(len(b))
		if !s.Lossy {
			info, err := flacmeta.Read(bytesReaderAt(b), int64(len(b)))
			must(err)
			s.Samples = int64(info.StreamInfo.TotalSamples) //nolint:gosec // 36-bit field
		}
		must(os.Chmod(abs, 0o444))
		a.Sources = append(a.Sources, s)
	}
	rel := func(name, ext string) string {
		p := filepath.Join("masters", "l2", name+ext)
		must(os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755))
		return p
	}
	flacTone := func(name string, lufs, seconds float64, variant int) L2AudioSource {
		p := rel(name, ".flac")
		must(synth.FLAC(ctx, tools, filepath.Join(dir, p), synth.Tone{LUFS: lufs, Seconds: seconds, Variant: variant}, synth.StyleFFmpeg))
		return L2AudioSource{Name: name, Rel: p, Rate: 48000, Bits: 24, Channels: 2, Seconds: seconds, LUFS: lufs, Calibrated: true}
	}
	add(flacTone("loud", -12, 6, 0))
	add(flacTone("quiet", -20, 5, 1))
	add(flacTone("mid", -16, 4, 2))

	mp3 := rel("lossy", ".mp3")
	must(synth.MP3(ctx, tools, filepath.Join(dir, mp3), synth.Tone{LUFS: -15, Seconds: 3, Variant: 3}, synth.MP3Options{SampleRate: 44100}))
	add(L2AudioSource{Name: "lossy", Rel: mp3, Lossy: true, Rate: 44100, Bits: 16, Channels: 2, Seconds: 3, LUFS: -15, Calibrated: true})

	// derived sources start from a calibrated tone kept outside the data directory
	base := filepath.Join(t.TempDir(), "base.flac")
	must(synth.FLAC(ctx, tools, base, synth.Tone{LUFS: -14, Seconds: 3, Variant: 4}, synth.StyleFFmpeg))
	convert := func(name string, extra ...string) string {
		p := rel(name, ".flac")
		args := append([]string{"-nostdin", "-hide_banner", "-v", "error", "-i", "file:" + base, "-map", "0:a:0", "-map_metadata", "-1"}, extra...)
		args = append(args, "-c:a", "flac", "-f", "flac", "file:"+filepath.Join(dir, p))
		_, err := mediatool.Run(ctx, tools.FFmpeg, args, mediatool.RunOpts{Timeout: time.Minute, MaxStdout: 1 << 10})
		must(err)
		return p
	}
	add(L2AudioSource{Name: "cd", Rel: convert("cd", "-ar", "44100", "-sample_fmt", "s16"), Rate: 44100, Bits: 16, Channels: 2, Seconds: 3, LUFS: -14, Calibrated: true})
	add(L2AudioSource{Name: "mono", Rel: convert("mono", "-ac", "1", "-sample_fmt", "s32", "-bits_per_raw_sample", "24"), Rate: 48000, Bits: 24, Channels: 1, Seconds: 3, LUFS: math.NaN()})
	return a
}

// bytesReaderAt adapts a byte slice for flacmeta.Read without importing bytes twice in callers.
type bytesReaderAt []byte

func (b bytesReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(b)) {
		return 0, io.EOF
	}
	n := copy(p, b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
