package ffmpeg_test

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

// writeClickWAV writes a 16-bit mono 44.1 kHz WAV: a decaying 60 Hz kick on every beat, a noise tick on the offbeats.
func writeClickWAV(t *testing.T, path string, bpm, seconds float64) {
	t.Helper()
	const sr = 44100
	n := int(seconds * sr)
	pcm := make([]int16, n)
	seed := uint64(7)
	period := 60 / bpm
	for k := 0; float64(k)*period < seconds-0.3; k++ {
		for half := range 2 {
			i0 := int((float64(k) + float64(half)/2) * period * sr)
			for i := 0; i < int(0.12*sr) && i0+i < n; i++ {
				tt := float64(i) / sr
				v := 0.8 * math.Exp(-tt*35) * math.Sin(2*math.Pi*60*tt)
				if half == 1 {
					seed = seed*6364136223846793005 + 1442695040888963407
					v = 0.2 * (float64(seed>>40)/float64(1<<24) - 0.5) * math.Exp(-tt*150)
				}
				pcm[i0+i] += int16(v * 20000)
			}
		}
	}
	hdr := make([]byte, 44)
	copy(hdr, "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], uint32(36+2*n)) //nolint:gosec // test sizes are small
	copy(hdr[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)
	binary.LittleEndian.PutUint16(hdr[20:], 1)
	binary.LittleEndian.PutUint16(hdr[22:], 1)
	binary.LittleEndian.PutUint32(hdr[24:], sr)
	binary.LittleEndian.PutUint32(hdr[28:], sr*2)
	binary.LittleEndian.PutUint16(hdr[32:], 2)
	binary.LittleEndian.PutUint16(hdr[34:], 16)
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], uint32(2*n)) //nolint:gosec // test sizes are small
	body := make([]byte, 2*n)
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(body[2*i:], uint16(v)) //nolint:gosec // two's complement sample
	}
	if err := os.WriteFile(path, append(hdr, body...), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFFmpeg_Tempo(t *testing.T) {
	t.Parallel()
	f, tools := newReal(t)
	dir := t.TempDir()
	wav, flac := filepath.Join(dir, "in.wav"), filepath.Join(dir, "clicks.flac")
	writeClickWAV(t, wav, 120, 45)
	if _, err := mediatool.Run(context.Background(), tools.FFmpeg, []string{"-nostdin", "-v", "error", "-i", wav, "-c:a", "flac", flac}, mediatool.RunOpts{Timeout: 60e9, MaxStdout: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	got, err := f.Tempo(context.Background(), flac, 45)
	if err != nil {
		t.Fatal(err)
	}
	if got.BPM == nil {
		t.Fatalf("no tempo: %+v", got)
	}
	if !near(*got.BPM, 120, 1.5) && !near(*got.BPM, 240, 3) && !near(*got.BPM, 60, 1) {
		t.Errorf("bpm = %.2f, want 120 (or an octave)", *got.BPM)
	}
	if !near(got.AnalyzedSeconds, 45, 0.5) {
		t.Errorf("analyzed %.2f s", got.AnalyzedSeconds)
	}
}
