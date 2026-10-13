package ffmpeg_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/synth"
)

func TestArgv_OnlyWorkPaths_CountFrames(t *testing.T) {
	t.Parallel()
	bin, log := fakeFFmpeg(t, "ffmpeg version n9.0.1 Copyright", okFilters, okEncoders, "head -c 40 /dev/zero")
	f, err := ffmpeg.New(context.Background(), mediatool.Tools{FFmpeg: bin})
	if err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(t.TempDir(), "master.mp3")
	n, err := f.CountFrames(context.Background(), in, 44100, 2)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 { // 40 bytes / (4 bytes * 2 channels)
		t.Fatalf("frames = %d, want 5", n)
	}
	b, _ := os.ReadFile(log)
	args := strings.Split(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(string(b)), "--")), "\n")
	want := []string{"-nostdin", "-hide_banner", "-v", "error", "-protocol_whitelist", "file,pipe", "-i", "file:" + in, "-map", "0:a:0",
		"-f", "f32le", "-acodec", "pcm_f32le", "-"}
	if !slices.Equal(args, want) {
		t.Errorf("argv = %q\nwant   %q", args, want)
	}
	for _, bad := range []func() error{
		func() error { _, e := f.CountFrames(context.Background(), "relative.mp3", 44100, 2); return e },
		func() error { _, e := f.CountFrames(context.Background(), in, 0, 2); return e },
		func() error { _, e := f.CountFrames(context.Background(), in, 44100, 0); return e },
	} {
		if bad() == nil {
			t.Error("bad input accepted")
		}
	}
}

func TestCountFrames(t *testing.T) {
	f, tools := newReal(t)
	dir := t.TempDir()
	cases := []struct {
		name   string
		tone   synth.Tone
		o      synth.MP3Options
		want   uint64
		sr, ch int
	}{
		{"stereo2s", synth.Tone{LUFS: -20, Seconds: 2}, synth.MP3Options{}, 88200, 44100, 2},
		{"stereo2s_noid3", synth.Tone{LUFS: -20, Seconds: 2}, synth.MP3Options{NoID3: true}, 88200, 44100, 2},
		{"mono1s22k", synth.Tone{LUFS: -20, Seconds: 1}, synth.MP3Options{SampleRate: 22050, Channels: 1, BitrateK: 64}, 22050, 22050, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			p := filepath.Join(dir, c.name+".mp3")
			if err := synth.MP3(context.Background(), tools, p, c.tone, c.o); err != nil {
				t.Fatal(err)
			}
			got, err := f.CountFrames(context.Background(), p, c.sr, c.ch)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("frames = %d, want %d", got, c.want)
			}
		})
	}
	t.Run("garbage", func(t *testing.T) {
		t.Parallel()
		p := filepath.Join(dir, "garbage.mp3")
		if err := os.WriteFile(p, []byte(strings.Repeat("not audio at all ", 200)), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := f.CountFrames(context.Background(), p, 44100, 2); err == nil {
			t.Fatal("garbage accepted")
		}
	})
}

func TestAnalysis_LossyComparable(t *testing.T) {
	t.Parallel()
	f, tools := newReal(t)
	dir := t.TempDir()
	tone := synth.Tone{LUFS: -20, Seconds: 3}
	mp3, flac := filepath.Join(dir, "a.mp3"), filepath.Join(dir, "a.flac")
	if err := synth.MP3(context.Background(), tools, mp3, tone, synth.MP3Options{}); err != nil {
		t.Fatal(err)
	}
	if err := synth.FLAC(context.Background(), tools, flac, tone, synth.StyleFFmpeg); err != nil {
		t.Fatal(err)
	}
	lm, err := f.Loudness(context.Background(), mp3)
	if err != nil {
		t.Fatal(err)
	}
	lf, err := f.Loudness(context.Background(), flac)
	if err != nil {
		t.Fatal(err)
	}
	if d := lm.IntegratedLUFS - lf.IntegratedLUFS; d < -0.5 || d > 0.5 {
		t.Fatalf("mp3 %.2f LUFS vs flac %.2f LUFS", lm.IntegratedLUFS, lf.IntegratedLUFS)
	}
	n, err := f.CountFrames(context.Background(), mp3, 44100, 2)
	if err != nil {
		t.Fatal(err)
	}
	pk, sp, err := f.Scan(context.Background(), mp3, 44100, 2, n)
	if err != nil {
		t.Fatal(err)
	}
	if sp.BandwidthHz == nil {
		t.Fatal("bandwidth_hz is nil")
	}
	if pk.DurationS < 2.99 || pk.DurationS > 3.01 {
		t.Fatalf("duration = %v", pk.DurationS)
	}
}
