package clip_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/synth"
)

// argvLoggingFFmpeg wraps the real ffmpeg in a script that appends each argv to a log.
func argvLoggingFFmpeg(t testing.TB, realBin string) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "argv.log")
	bin = filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> '" + log + "'\nexec '" + realBin + "' \"$@\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func TestClip_LossyMaster(t *testing.T) {
	t.Parallel()
	rt, _ := realTools(t)
	e := newEnv(t)
	rel := "audio/masters/lossy.mp3"
	if err := os.MkdirAll(filepath.Dir(e.abs(rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := synth.MP3(context.Background(), rt, e.abs(rel), synth.Tone{LUFS: -20, Seconds: 40}, synth.MP3Options{}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(e.abs(rel))
	if err != nil {
		t.Fatal(err)
	}
	sp := e.putMasterBytes(rel, b, 44100, 2)
	bin, log := argvLoggingFFmpeg(t, rt.FFmpeg)
	tools := mediatool.Tools{FFmpeg: bin}
	s := clip.New(e.root, e.dir, tools, mustAnalyzer(t, tools), nil, nil, clip.Options{})

	if _, err := s.Measure(context.Background(), sp); err != nil {
		t.Fatal(err)
	}
	o, err := s.Ensure(context.Background(), sp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(o.RelPath, ".flac") {
		t.Fatalf("clip path %q is not a FLAC", o.RelPath)
	}
	si := streamInfo(t, e.abs(o.RelPath))
	want := uint64(30 * 48000) // clips are always 48 kHz
	if si.BitsPerSample != 24 || si.SampleRate != 48000 || si.TotalSamples+1 < want || si.TotalSamples > want+1 {
		t.Fatalf("clip stream info = %+v, want 24-bit 48000 Hz %d samples +-1", si, want)
	}
	lg, _ := os.ReadFile(log)
	if !strings.Contains(string(lg), "/master.mp3\n") || strings.Contains(string(lg), "master.flac") {
		t.Fatalf("work dir did not hold master.mp3:\n%s", lg)
	}
	if left := e.workDirs(t); len(left) != 0 {
		t.Fatalf("work dirs left: %v", left)
	}
}
