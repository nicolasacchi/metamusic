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

func TestArgv_OnlyWorkPaths_Window(t *testing.T) {
	t.Parallel()
	capture, _ := filepath.Abs("testdata/window_ebur128_n9.0.1.txt")
	bin, log := fakeFFmpeg(t, "ffmpeg version n9.0.1 Copyright", okFilters, okEncoders, "cat '"+capture+"'")
	f, err := ffmpeg.New(context.Background(), mediatool.Tools{FFmpeg: bin})
	if err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(t.TempDir(), "master.flac")
	if _, err := f.WindowLoudness(context.Background(), in, 48000, 240000); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	args := strings.Split(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(string(b)), "--")), "\n")
	want := []string{"-nostdin", "-hide_banner", "-nostats", "-v", "error", "-protocol_whitelist", "file,pipe", "-i", "file:" + in, "-map", "0:a:0",
		"-af", "atrim=start_sample=48000:end_sample=240000,ebur128=peak=true+sample:framelog=quiet:metadata=1,ametadata=mode=print:file=-", "-f", "null", "-"}
	if !slices.Equal(args, want) {
		t.Errorf("argv = %q\nwant   %q", args, want)
	}
	for _, bad := range []func() error{
		func() error { _, e := f.WindowLoudness(context.Background(), "relative.flac", 0, 10); return e },
		func() error { _, e := f.WindowLoudness(context.Background(), in, 10, 10); return e },
		func() error { _, e := f.WindowLoudness(context.Background(), in, -1, 10); return e },
	} {
		if bad() == nil {
			t.Error("bad input accepted")
		}
	}
}
