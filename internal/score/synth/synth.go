// Package synth renders an SMF to audio with fluidsynth (C354, T1 C208.5, D-DESK-8) and keeps the operator's soundfont set.
// It runs the tool only through internal/mediatool: no shell, a literal argument list, absolute app-created paths, bounded
// time and stderr. Neither errors nor logs carry a path.
package synth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

// Defaults and bounds of a Request.
const (
	DefaultRate    = 48000
	DefaultGain    = 0.5
	minRate        = 8000
	maxRate        = 96000
	maxGain        = 10.0
	minTimeout     = 10 * time.Second
	maxVersionLen  = 80
	maxStderrBytes = 8 << 10
)

// Synth renders an SMF file into a WAV file.
type Synth interface {
	Render(ctx context.Context, r Request) (Info, error)
}

// Request describes one render. SMF, Soundfont and Out are absolute paths the application created or configured; Out is
// overwritten. Rate and Gain default to DefaultRate and DefaultGain when zero. Timeout zero means TimeoutFor(0).
type Request struct {
	SMF, Soundfont, Out string
	Rate                int
	Gain                float64
	Timeout             time.Duration
}

// Info identifies the engine that rendered (it enters the transform spec hash).
type Info struct{ Name, Version string }

// Errors returned for a malformed Request (before anything runs).
var (
	ErrBadRequest  = errors.New("synth: invalid request")
	ErrUnavailable = errors.New("synth: fluidsynth is not available")
	ErrNoOutput    = errors.New("synth: no audio produced")
)

// TimeoutFor is the render timeout for a score of the given length: max(10 s, 0.5 x score seconds).
func TimeoutFor(scoreSeconds float64) time.Duration {
	if t := time.Duration(scoreSeconds * 0.5 * float64(time.Second)); t > minTimeout {
		return t
	}
	return minTimeout
}

// Argv is the fluidsynth argument list for r (D-DESK-8; TestFluidsynth_Argv is its golden). -q keeps the banner off stdout,
// -ni is no shell and no MIDI input, one CPU core removes thread scheduling from the synthesis, reverb and chorus are off so
// the render is dry, -O float removes an integer quantisation before ours.
func Argv(r Request) []string {
	rate, gain := r.Rate, r.Gain
	if rate == 0 {
		rate = DefaultRate
	}
	if gain == 0 {
		gain = DefaultGain
	}
	return []string{
		"-q", "-ni", "-o", "synth.cpu-cores=1",
		"-g", strconv.FormatFloat(gain, 'f', -1, 64),
		"-r", strconv.Itoa(rate),
		"-R", "0", "-C", "0",
		"-T", "wav", "-O", "float",
		"-F", r.Out, r.Soundfont, r.SMF,
	}
}

func (r Request) validate() error {
	for _, p := range []string{r.SMF, r.Soundfont, r.Out} {
		if !filepath.IsAbs(p) || strings.HasPrefix(p, "-") || strings.ContainsRune(p, 0) {
			return fmt.Errorf("%w: paths must be absolute", ErrBadRequest)
		}
	}
	if r.Rate != 0 && (r.Rate < minRate || r.Rate > maxRate) {
		return fmt.Errorf("%w: rate", ErrBadRequest)
	}
	if r.Gain < 0 || r.Gain > maxGain || r.Gain != r.Gain {
		return fmt.Errorf("%w: gain", ErrBadRequest)
	}
	if r.Timeout < 0 {
		return fmt.Errorf("%w: timeout", ErrBadRequest)
	}
	return nil
}

// RenderError is a failed fluidsynth run. It carries the exit code and the context error only, never stderr (which names
// paths).
type RenderError struct {
	ExitCode int
	Err      error
}

func (e *RenderError) Error() string {
	return fmt.Sprintf("synth: fluidsynth failed: exit %d", e.ExitCode)
}

// Unwrap exposes the context error so errors.Is(err, context.DeadlineExceeded) works.
func (e *RenderError) Unwrap() error { return e.Err }

// Fluidsynth is the fluidsynth adapter. Build it with NewFluidsynth.
type Fluidsynth struct {
	bin  string
	info Info
}

// NewFluidsynth probes bin once (`--version`, first line, at most 80 bytes). An empty bin or a failing probe returns
// ErrUnavailable.
func NewFluidsynth(ctx context.Context, bin string) (*Fluidsynth, error) {
	if bin == "" {
		return nil, ErrUnavailable
	}
	out, err := mediatool.Run(ctx, bin, []string{"--version"}, mediatool.RunOpts{Timeout: 10 * time.Second, MaxStdout: 4 << 10, MaxStderr: 1 << 10})
	if err != nil {
		return nil, fmt.Errorf("%w", ErrUnavailable)
	}
	return &Fluidsynth{bin: bin, info: Info{Name: "fluidsynth", Version: firstLine(out)}}, nil
}

func firstLine(b []byte) string {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		b = b[:i]
	}
	s := strings.ToValidUTF8(strings.TrimSpace(string(b)), "")
	if len(s) > maxVersionLen {
		s = s[:maxVersionLen]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

// Info is the engine identity read at startup.
func (f *Fluidsynth) Info() Info { return f.info }

// Render runs fluidsynth with its own process group and a minimal environment (LC_ALL, PATH and HOME pointing at Out's
// directory, so no user or system configuration is read) and returns the engine identity. A timeout or cancel kills the
// whole group.
func (f *Fluidsynth) Render(ctx context.Context, r Request) (Info, error) {
	if err := r.validate(); err != nil {
		return Info{}, err
	}
	timeout := r.Timeout
	if timeout == 0 {
		timeout = TimeoutFor(0)
	}
	_ = os.Remove(r.Out)
	_, err := mediatool.Run(ctx, f.bin, Argv(r), mediatool.RunOpts{
		Timeout: timeout, Stdout: io.Discard, MaxStderr: maxStderrBytes, Group: true,
		ExtraEnv: []string{"HOME=" + filepath.Dir(r.Out)},
	})
	if err != nil {
		// fluidsynth names the soundfont, SMF and output paths on stderr; keep only the exit code and the context cause.
		var te *mediatool.ToolError
		if errors.As(err, &te) {
			return Info{}, &RenderError{ExitCode: te.ExitCode, Err: te.Err}
		}
		return Info{}, err
	}
	if st, err := os.Stat(r.Out); err != nil || st.Size() == 0 {
		return Info{}, ErrNoOutput
	}
	return f.info, nil
}

var _ Synth = (*Fluidsynth)(nil)
