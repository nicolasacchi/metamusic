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

func TestFFmpeg_TempoDecodeArgv(t *testing.T) {
	t.Parallel()
	bin, log := fakeFFmpeg(t, "ffmpeg version n9.0.1 Copyright", okFilters, okEncoders, "exit 0")
	f, err := ffmpeg.New(context.Background(), mediatool.Tools{FFmpeg: bin})
	if err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(t.TempDir(), "master.flac")
	got, err := f.Tempo(context.Background(), in, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.BPM != nil {
		t.Errorf("empty decode must give no tempo, got %+v", got)
	}
	b, _ := os.ReadFile(log)
	args := strings.Split(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(string(b)), "--")), "\n")
	want := []string{"-nostdin", "-hide_banner", "-nostats", "-v", "error", "-protocol_whitelist", "file,pipe", "-i", "file:" + in, "-map", "0:a:0",
		"-ac", "1", "-ar", "11025", "-t", "240", "-f", "f32le", "-"}
	if !slices.Equal(args, want) {
		t.Errorf("argv = %q\nwant   %q", args, want)
	}
	if _, err := f.Tempo(context.Background(), "relative.flac", 0); err == nil {
		t.Error("relative path must be rejected")
	}
}
