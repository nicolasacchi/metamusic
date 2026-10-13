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

func TestArgv_OnlyWorkPaths_Spectrum(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out := filepath.Join(dir, "spectro-1024x384.png")
	body := `for last; do :; done; printf x > "${last#file:}"`
	bin, log := fakeFFmpeg(t, "ffmpeg version n9.0.1 Copyright", okFilters, okEncoders, body)
	f, err := ffmpeg.New(context.Background(), mediatool.Tools{FFmpeg: bin})
	if err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(dir, "master.flac")
	if err := f.SpectrumPicture(context.Background(), in, out, 1024, 384); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	args := strings.Split(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(string(b)), "--")), "\n")
	want := []string{"-nostdin", "-hide_banner", "-nostats", "-v", "error", "-protocol_whitelist", "file,pipe", "-i", "file:" + in,
		"-filter_complex", "[0:a:0]aformat=channel_layouts=mono,showspectrumpic=s=1024x384:legend=1:fscale=log:scale=log:color=magma[v]",
		"-map", "[v]", "-frames:v", "1", "-update", "1", "-c:v", "png", "-f", "image2", "file:" + out}
	if !slices.Equal(args, want) {
		t.Errorf("argv = %q\nwant   %q", args, want)
	}
	for _, c := range []struct {
		in, out string
		w, h    int
	}{
		{"relative.flac", out, 1024, 384},
		{in, "relative.png", 1024, 384},
		{in, out, 0, 384},
		{in, out, 1024, 5000},
	} {
		if err := f.SpectrumPicture(context.Background(), c.in, c.out, c.w, c.h); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
}
