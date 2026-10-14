package synth_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/synth"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

// TestFLACUncalibrated (heavy): one render pass, the StyleYuE2 shape, distinct M1 audio identity per
// Variant, and no ebur128 call anywhere.
func TestFLACUncalibrated(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tools := testtools.Tools()
	bin, log := recordingFFmpeg(t, tools.FFmpeg)
	rec := tools
	rec.FFmpeg = bin
	d := t.TempDir()
	a, b, a2 := filepath.Join(d, "a.flac"), filepath.Join(d, "b.flac"), filepath.Join(d, "a2.flac")
	ctx := context.Background()
	if err := synth.FLACUncalibrated(ctx, rec, a, synth.Tone{Seconds: 2, BPM: 100}, synth.StyleYuE2); err != nil {
		t.Fatal(err)
	}
	argv, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(argv), "-f\nlavfi"); n != 1 {
		t.Errorf("render passes = %d, want 1\n%s", n, argv)
	}
	if strings.Contains(string(argv), "ebur128") {
		t.Errorf("loudness measurement ran:\n%s", argv)
	}
	ia, _ := readInfo(t, a)
	if ia.Count(1) != 0 || ia.Count(3) != 0 || ia.Count(4) != 1 || ia.Vendor == "" || len(ia.Comments) != 0 {
		t.Errorf("StyleYuE2 shape lost: %+v %q %v", ia.Blocks, ia.Vendor, ia.Comments)
	}
	if ia.StreamInfo.TotalSamples != 2*48000 || ia.StreamInfo.BitsPerSample != 24 || ia.StreamInfo.SampleRate != 48000 || ia.StreamInfo.Channels != 2 {
		t.Errorf("stream info %+v", ia.StreamInfo)
	}
	if err := synth.FLACUncalibrated(ctx, tools, b, synth.Tone{Seconds: 2, BPM: 100, Variant: 1}, synth.StyleYuE2); err != nil {
		t.Fatal(err)
	}
	if err := synth.FLACUncalibrated(ctx, tools, a2, synth.Tone{Seconds: 2, BPM: 100}, synth.StyleYuE2); err != nil {
		t.Fatal(err)
	}
	if framesSHA(t, a) == framesSHA(t, b) {
		t.Error("Variant must change the frames (M1 audio identity)")
	}
	if framesSHA(t, a) != framesSHA(t, a2) {
		t.Error("the same tone must be reproducible")
	}
	for _, bad := range []struct {
		dst  string
		tone synth.Tone
	}{{"relative.flac", synth.Tone{Seconds: 1}}, {filepath.Join(d, "z.flac"), synth.Tone{Seconds: 0}}} {
		if err := synth.FLACUncalibrated(ctx, tools, bad.dst, bad.tone, synth.StyleFFmpeg); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}
