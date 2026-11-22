// Package desktest is the test double of the Desk's audition pipeline: a Renderer that stands in for the T1b score pipeline (it
// writes the 24-bit PCM file an audition encodes) and a fake MP3 encoder, so tests of the Desk, the operations and the
// acceptance rig run without fluidsynth, a soundfont or ffmpeg. The real pipeline is covered by the go-media tests.
package desktest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/score/synth"
	"github.com/nicolasacchi/metamusic/internal/transform"
)

type nullSynth struct{}

func (nullSynth) Render(context.Context, synth.Request) (synth.Info, error) { return synth.Info{}, nil }

// Renderer implements desk.Renderer. Set Busy, Err, Secs or Block before the call under test.
type Renderer struct {
	Dir     string                  // the data directory the work directories live under
	Support *transform.ScoreSupport // what ScoreSupport answers (replace Fonts or Synth to simulate a missing part)
	Busy    bool                    // Exclusive answers transform.ErrBusy
	Err     error                   // RenderScore fails with it
	Secs    float64                 // the reported length (default 12.5)
	Block   chan struct{}           // RenderScore waits for it (or for ctx) before writing
	Renders atomic.Int64            // RenderScore calls
	slot    chan struct{}
}

// New builds a Renderer with two soundfonts ("gm" and "alt") and synth version 2.5.4. dir is the data directory.
func New(t testing.TB, dir string) *Renderer {
	t.Helper()
	var fonts []synth.Font
	for _, n := range []string{"gm", "alt"} {
		p := filepath.Join(t.TempDir(), n+".sf2")
		if err := os.WriteFile(p, []byte("soundfont "+n), 0o600); err != nil {
			t.Fatal(err)
		}
		fonts = append(fonts, synth.Font{Name: n, Path: p})
	}
	return &Renderer{Dir: dir, slot: make(chan struct{}, 1), Support: &transform.ScoreSupport{Synth: nullSynth{},
		Info: synth.Info{Name: "fluidsynth", Version: "2.5.4"}, Fonts: synth.NewSet(fonts, nil)}}
}

// ScoreSupport implements desk.Renderer.
func (r *Renderer) ScoreSupport() *transform.ScoreSupport { return r.Support }

// Exclusive implements desk.Renderer with a one-slot semaphore.
func (r *Renderer) Exclusive(ctx context.Context, _ time.Duration, fn func(context.Context) error) error {
	if r.Busy {
		return transform.ErrBusy
	}
	r.slot <- struct{}{}
	defer func() { <-r.slot }()
	return fn(ctx)
}

// RenderScore implements desk.Renderer: the PCM bytes depend on the soundfont, chords, tail and gain, so each setting is its own
// audition, as with the real pipeline.
func (r *Renderer) RenderScore(ctx context.Context, j transform.ScoreJob, workRel string, _ transform.ChildProgress) (transform.ScoreOut, error) {
	r.Renders.Add(1)
	if r.Block != nil {
		select {
		case <-r.Block:
		case <-ctx.Done():
			return transform.ScoreOut{}, &transform.ScoreError{Code: transform.ErrInterrupted, Message: "interrupted"}
		}
	}
	if r.Err != nil {
		return transform.ScoreOut{}, r.Err
	}
	abs := filepath.Join(r.Dir, filepath.FromSlash(workRel))
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return transform.ScoreOut{}, err
	}
	key := fmt.Sprintf("%s|%s|%v|%v", j.Soundfont, j.Chords, j.TailS, j.Gain)
	if err := os.WriteFile(filepath.Join(abs, "out.pcm24"), []byte("pcm:"+key), 0o600); err != nil {
		return transform.ScoreOut{}, err
	}
	secs := r.Secs
	if secs == 0 {
		secs = 12.5
	}
	return transform.ScoreOut{PCMSHA256: fmt.Sprintf("%064x", len(key)), Seconds: secs, Report: []byte(`{"voices":1}`), CPU: 40 * time.Millisecond}, nil
}

// Encode is the fake MP3 encoder: the "mp3" is the PCM bytes behind a tag.
func Encode(_ context.Context, pcm, out string) error {
	b, err := os.ReadFile(pcm) // #nosec G304 -- a path the audition under test created
	if err != nil {
		return err
	}
	return os.WriteFile(out, append([]byte("mp3:"), b...), 0o600) // #nosec G703 -- a path the audition under test created
}
