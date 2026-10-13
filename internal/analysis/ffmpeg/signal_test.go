package ffmpeg_test

import (
	"time"

	"context"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/synth"
)

func TestSignal_ClipBursts(t *testing.T) {
	t.Parallel()
	f, tools := newReal(t)
	p := filepath.Join(t.TempDir(), "clip.flac")
	if err := synth.Special(context.Background(), tools, p, synth.SpecialClipBursts, 8); err != nil {
		t.Fatal(err)
	}
	_, c, err := f.Signal(context.Background(), p, 48000, 8)
	if err != nil {
		t.Fatal(err)
	}
	if !near(c.ClippedS, 2.0, 0.2) || len(c.Spans) != 1 || !near(c.Spans[0].StartS, 2, 0.1) || !near(c.Spans[0].EndS, 4, 0.1) {
		t.Errorf("clipping = %+v", c)
	}
}

func TestSignal_Gap(t *testing.T) {
	t.Parallel()
	f, tools := newReal(t)
	p := filepath.Join(t.TempDir(), "gap.flac")
	if err := synth.Special(context.Background(), tools, p, synth.SpecialGap, 10); err != nil {
		t.Fatal(err)
	}
	s, c, err := f.Signal(context.Background(), p, 48000, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Spans) != 1 || !near(s.Spans[0].StartS, 4, 0.05) || !near(s.Spans[0].EndS, 6.5, 0.05) || !near(s.InternalS, 2.5, 0.05) || s.LeadingS != 0 || s.TrailingS != 0 {
		t.Errorf("silence = %+v", s)
	}
	if c.ClippedFrames != 0 {
		t.Errorf("clipping = %+v", c)
	}
}

func TestSignal_44k(t *testing.T) {
	t.Parallel()
	f, tools := newReal(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "gap48.flac")
	if err := synth.Special(context.Background(), tools, src, synth.SpecialGap, 10); err != nil {
		t.Fatal(err)
	}
	// resample to 44.1 kHz through the tool binary used elsewhere in the suite
	dst := filepath.Join(dir, "gap44.flac")
	if _, err := runTool(t, tools.FFmpeg, "-nostdin", "-v", "error", "-y", "-i", src, "-ar", "44100", dst); err != nil {
		t.Fatal(err)
	}
	s, _, err := f.Signal(context.Background(), dst, 44100, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Spans) != 1 || !near(s.Spans[0].StartS, 4, 0.05) || !near(s.Spans[0].EndS, 6.5, 0.05) {
		t.Errorf("silence = %+v", s)
	}
}

func runTool(t *testing.T, bin string, args ...string) ([]byte, error) {
	t.Helper()
	return mediatool.Run(context.Background(), bin, args, mediatool.RunOpts{Timeout: 60 * time.Second, MaxStdout: 1 << 20})
}
