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
)

func TestArgv_OnlyWorkPaths_Signal(t *testing.T) {
	t.Parallel()
	body := `printf 'frame:0    pts:0       pts_time:0\nlavfi.astats.Overall.Peak_level=-20.0\nlavfi.astats.Overall.Flat_factor=0.0\n'`
	bin, log := fakeFFmpeg(t, "ffmpeg version n9.0.1 Copyright", okFilters, okEncoders, body)
	f, err := ffmpeg.New(context.Background(), mediatool.Tools{FFmpeg: bin})
	if err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(t.TempDir(), "master.flac")
	if _, _, err := f.Signal(context.Background(), in, 44100, 10); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	args := strings.Split(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(string(b)), "--")), "\n")
	want := []string{"-nostdin", "-hide_banner", "-nostats", "-v", "error", "-protocol_whitelist", "file,pipe", "-i", "file:" + in, "-map", "0:a:0",
		"-af", "asetnsamples=n=4410:p=0,astats=metadata=1:reset=1:measure_perchannel=none:measure_overall=Peak_level+Flat_factor,silencedetect=noise=-50dB:duration=1,ametadata=mode=print:file=-",
		"-f", "null", "-"}
	if !slices.Equal(args, want) {
		t.Errorf("argv = %q\nwant   %q", args, want)
	}
	if _, _, err := f.Signal(context.Background(), "relative.flac", 48000, 10); err == nil {
		t.Error("relative path must be rejected")
	}
	if _, _, err := f.Signal(context.Background(), in, 0, 10); err == nil {
		t.Error("zero sample rate must be rejected")
	}
}
