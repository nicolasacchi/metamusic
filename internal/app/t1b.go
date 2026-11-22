package app

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/score/synth"
	"github.com/nicolasacchi/metamusic/internal/transform"
)

// wireScore adds the T1b score renderer to the transform options (C355.8): the fluidsynth tool and its version (read once), the
// soundfont set (hashed once, a bad file disables only its name) and the reader of a take's score text. Without fluidsynth or a
// soundfont score_render stays unavailable: its estimate and create answer 503 UNAVAILABLE, never a failure after a queue slot is
// spent. The estimate itself stays database-only (D-T1-6): only create and run read a score file.
func wireScore(o *transform.Options, cfg config.Config, lib *library.Service, log *slog.Logger) {
	fonts := make([]synth.Font, 0, len(cfg.Soundfonts))
	for _, sf := range cfg.Soundfonts {
		fonts = append(fonts, synth.Font(sf))
	}
	set := synth.NewSet(fonts, log)
	sup := &transform.ScoreSupport{Fonts: set, ScoreABC: lib.ScoreABC}

	bin := cfg.FluidsynthPath
	if bin == "" {
		bin, _ = exec.LookPath("fluidsynth")
	}
	var version string
	if bin != "" {
		if f, err := synth.NewFluidsynth(context.Background(), bin); err == nil {
			sup.Synth, sup.Info, version = f, f.Info(), f.Info().Version
			o.Tools = mediatool.Tools{FFmpeg: o.Tools.FFmpeg, FLAC: o.Tools.FLAC, Metaflac: o.Tools.Metaflac, Fluidsynth: bin}
		} else if log != nil {
			log.Warn("fluidsynth unavailable", "reason", "the version probe failed")
		}
	}
	o.Score = sup
	prev := o.ToolStatus
	o.ToolStatus = func() transform.ToolStatus {
		st := transform.ToolStatus{FFmpeg: true, FLAC: true}
		if prev != nil {
			st = prev()
		}
		st.Fluidsynth, st.FluidsynthVersion = sup.Synth != nil, version
		return st
	}
	o.SoundfontSHA12 = func(sf config.Soundfont) string {
		if _, sha, _, ok := set.Lookup(sf.Name); ok && len(sha) >= 12 {
			return sha[:12]
		}
		return ""
	}
}

// scoreText adapts the library's score reader to trackctx: a take without a score, or a file that cannot be read, answers
// "no text" (abc null) rather than failing the whole context.
func scoreText(lib *library.Service) func(ctx context.Context, trackID string) (string, error) {
	return func(ctx context.Context, trackID string) (string, error) {
		b, _, ok, err := lib.ScoreABC(ctx, trackID)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", errNoScore
		}
		return string(b), nil
	}
}

var errNoScore = errors.New("app: the take has no score")
