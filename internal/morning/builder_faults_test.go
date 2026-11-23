package morning

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

type errAnalyzer struct{ err error }

func (errAnalyzer) ToolVersion() string { return "fake" }
func (e errAnalyzer) WindowLoudness(context.Context, string, int64, int64) (clip.Measure, error) {
	return clip.Measure{}, e.err
}

func buildCode(t *testing.T, err error) string {
	t.Helper()
	var be *BuildError
	if !errors.As(err, &be) {
		t.Fatalf("err = %v, want a BuildError", err)
	}
	return be.Code
}

func TestBuilder_VerifyFLACRefusesNonFLAC(t *testing.T) {
	t.Parallel()
	root, dir := newRoot(t)
	b := NewBuilder(root, dir, mediatool.Tools{}, fixedAnalyzer{})
	s := Side{Samples: 1000}
	if _, err := b.verifyFLAC("absent.flac", s); buildCode(t, err) != CodeToolFailed {
		t.Errorf("absent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "junk.flac"), []byte("not a flac file at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.verifyFLAC("junk.flac", s); buildCode(t, err) != CodeToolFailed {
		t.Errorf("junk: %v", err)
	}
	if _, err := b.sha("absent.flac"); err == nil {
		t.Error("sha of a missing file")
	}
	if err := os.WriteFile(filepath.Join(dir, "x.bin"), []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := b.sha("x.bin"); err != nil || got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("sha = %q %v", got, err)
	}
}

func TestBuilder_CheckLoudnessFaults(t *testing.T) {
	t.Parallel()
	root, dir := newRoot(t)
	side := Side{Chapters: []Chapter{{N: 1, AudioStart: 10, End: 20}, {N: 2, AudioStart: 30, End: 40, Limited: true}}}
	ctx := context.Background()
	ok := clip.Measure{IntegratedLUFS: TargetLUFS, TruePeakDBTP: CeilingTP - 1}
	if got, err := NewBuilder(root, dir, mediatool.Tools{}, fixedAnalyzer{m: ok}).checkLoudness(ctx, "x.flac", side); err != nil || len(got) != 2 {
		t.Fatalf("on target: %v %v", got, err)
	}
	for name, m := range map[string]clip.Measure{
		"nan lufs":     {IntegratedLUFS: math.NaN(), TruePeakDBTP: -3},
		"nan peak":     {IntegratedLUFS: TargetLUFS, TruePeakDBTP: math.NaN()},
		"too loud":     {IntegratedLUFS: TargetLUFS + 2*LoudnessTolerance, TruePeakDBTP: -3},
		"peak too hot": {IntegratedLUFS: TargetLUFS, TruePeakDBTP: CeilingTP + 2*LoudnessTolerance},
	} {
		_, err := NewBuilder(root, dir, mediatool.Tools{}, fixedAnalyzer{m: m}).checkLoudness(ctx, "x.flac", side)
		if buildCode(t, err) != CodeLoudness {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, err := NewBuilder(root, dir, mediatool.Tools{}, errAnalyzer{err: errors.New("boom")}).checkLoudness(ctx, "x.flac", side)
	if buildCode(t, err) != CodeToolFailed {
		t.Errorf("analyzer error: %v", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = NewBuilder(root, dir, mediatool.Tools{}, errAnalyzer{err: errors.New("boom")}).checkLoudness(cctx, "x.flac", side)
	if buildCode(t, err) != CodeInterrupted {
		t.Errorf("cancelled analyzer: %v", err)
	}
}
