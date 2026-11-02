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

// TestFFmpeg_MonoDecodeArgv pins the shared decode of tempo, key and energy (C136.0): the argv of the C94 tempo decode,
// unchanged (TestFFmpeg_TempoDecodeArgv pins the wrapper to the same list).
func TestFFmpeg_MonoDecodeArgv(t *testing.T) {
	t.Parallel()
	bin, log := fakeFFmpeg(t, "ffmpeg version n9.0.1 Copyright", okFilters, okEncoders, "exit 0")
	f, err := ffmpeg.New(context.Background(), mediatool.Tools{FFmpeg: bin})
	if err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(t.TempDir(), "master.flac")
	got, err := f.Mono(context.Background(), in, 240)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rate != 11025 || len(got.Samples) != 0 || got.AnalyzedS != 0 {
		t.Errorf("empty decode = %+v", got)
	}
	b, _ := os.ReadFile(log)
	args := strings.Split(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(string(b)), "--")), "\n")
	want := []string{"-nostdin", "-hide_banner", "-nostats", "-v", "error", "-protocol_whitelist", "file,pipe", "-i", "file:" + in, "-map", "0:a:0",
		"-ac", "1", "-ar", "11025", "-t", "240", "-f", "f32le", "-"}
	if !slices.Equal(args, want) {
		t.Errorf("argv = %q\nwant   %q", args, want)
	}
	if _, err := f.Mono(context.Background(), "relative.flac", 240); err == nil {
		t.Error("relative path must be rejected")
	}
	// a shorter ask is honoured; more than 240 s is capped
	for _, tc := range []struct {
		ask  int
		want string
	}{{30, "30"}, {1000, "240"}, {0, "240"}} {
		if err := os.WriteFile(log, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Mono(context.Background(), in, tc.ask); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(log)
		lines := strings.Split(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(string(b)), "--")), "\n")
		if i := slices.Index(lines, "-t"); i < 0 || lines[i+1] != tc.want {
			t.Errorf("maxS %d: -t %v, want %s", tc.ask, lines, tc.want)
		}
	}
}
