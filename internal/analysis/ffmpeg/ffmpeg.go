// Package ffmpeg is the ffmpeg-backed implementation of analysis.Analyzer
// (C14): EBU R128 loudness from ffmpeg's ebur128 filter, and one decode pass
// that feeds peaks and the bandwidth measurement in Go.
package ffmpeg

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

const (
	probeTimeout    = 30 * time.Second
	loudnessTimeout = 300 * time.Second
	decodeTimeout   = 300 * time.Second
	maxLoudnessOut  = 32 << 20
	maxProbeOut     = 4 << 20
	// blockTolerance is the largest accepted difference between the decoded
	// frame count and STREAMINFO (one FLAC block).
	blockTolerance = 4608
	chunkFrames    = 4096
)

var (
	_            analysis.Analyzer = (*FFmpeg)(nil)
	reEbur128                      = regexp.MustCompile(`(?m)\sebur128\s`)
	reLibMP3Lame                   = regexp.MustCompile(`(?m)\slibmp3lame\s`)
)

// FFmpeg implements analysis.Analyzer.
type FFmpeg struct {
	bin     string
	version string // raw token
	// The -filters and -encoders listings New already reads; RequireClips and
	// RequireTierA (capabilities.go) match against them.
	filters, encoders []byte
}

// New probes the binary: version token (stored verbatim) and the ebur128 and
// libmp3lame capabilities.
func New(ctx context.Context, tools mediatool.Tools) (*FFmpeg, error) {
	run := func(arg string) ([]byte, error) {
		return mediatool.Run(ctx, tools.FFmpeg, []string{"-hide_banner", arg}, mediatool.RunOpts{Timeout: probeTimeout, MaxStdout: maxProbeOut})
	}
	out, err := run("-version")
	if err != nil {
		var te *mediatool.ToolError
		if errors.As(err, &te) && te.ExitCode == -1 && te.Err == nil {
			return nil, fmt.Errorf("%w: ffmpeg", analysis.ErrToolMissing)
		}
		return nil, fmt.Errorf("ffmpeg: version probe: %w", err)
	}
	tok, err := parseVersion(out)
	if err != nil {
		return nil, err
	}
	f := &FFmpeg{bin: tools.FFmpeg, version: tok}
	for _, c := range []struct {
		arg, name string
		re        *regexp.Regexp
		keep      *[]byte
	}{
		{"-filters", "ebur128", reEbur128, &f.filters},
		{"-encoders", "libmp3lame", reLibMP3Lame, &f.encoders},
	} {
		out, err := run(c.arg)
		if err != nil {
			return nil, fmt.Errorf("ffmpeg: %s probe: %w", c.arg, err)
		}
		if !c.re.Match(out) {
			return nil, fmt.Errorf("%w: %s", analysis.ErrToolMissing, c.name)
		}
		*c.keep = out
	}
	return f, nil
}

// ToolVersion is "ffmpeg <raw token>".
func (f *FFmpeg) ToolVersion() string { return "ffmpeg " + f.version }

func input(workPath string) ([]string, error) {
	if !filepath.IsAbs(workPath) {
		return nil, errors.New("ffmpeg: work path must be absolute")
	}
	return []string{"-protocol_whitelist", "file,pipe", "-i", "file:" + workPath, "-map", "0:a:0"}, nil
}

// Loudness runs the C14 ebur128 invocation and parses its metadata stream.
func (f *FFmpeg) Loudness(ctx context.Context, workPath string) (analysis.Loudness, error) {
	in, err := input(workPath)
	if err != nil {
		return analysis.Loudness{}, err
	}
	args := append([]string{"-nostdin", "-hide_banner", "-nostats", "-v", "error"}, in...)
	args = append(args, "-af", "ebur128=peak=true+sample:framelog=quiet:metadata=1,ametadata=mode=print:file=-", "-f", "null", "-")
	out, err := mediatool.Run(ctx, f.bin, args, mediatool.RunOpts{Timeout: loudnessTimeout, MaxStdout: maxLoudnessOut})
	if err != nil {
		return analysis.Loudness{}, err
	}
	return ParseLoudness(bytes.NewReader(out))
}

// sink decodes the f32le stream into the peaks and spectrum builders.
type sink struct {
	ch     int
	total  uint64
	n      uint64
	rem    []byte
	buf    []float32
	pk     *analysis.PeaksBuilder
	sp     *analysis.SpectrumBuilder
	cancel context.CancelFunc
	err    error
}

func (s *sink) Write(p []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	s.rem = append(s.rem, p...)
	fb := s.ch * 4
	whole := len(s.rem) / fb * fb
	for off := 0; off < whole; {
		end := min(whole, off+chunkFrames*fb)
		s.buf = s.buf[:0]
		for i := off; i < end; i += 4 {
			s.buf = append(s.buf, math.Float32frombits(binary.LittleEndian.Uint32(s.rem[i:])))
		}
		s.n += uint64((end - off) / fb) // #nosec G115 -- non-negative frame count
		if s.n > s.total+blockTolerance {
			s.err = errors.New("ffmpeg: decode produced more frames than STREAMINFO declares")
			s.cancel()
			return 0, s.err
		}
		s.pk.Write(s.buf)
		s.sp.Write(s.buf)
		off = end
	}
	s.rem = append(s.rem[:0], s.rem[whole:]...)
	return len(p), nil
}

// Scan decodes the file once and returns peaks and the bandwidth measurement.
func (f *FFmpeg) Scan(ctx context.Context, workPath string, sampleRate, channels int, totalSamples uint64) (analysis.Peaks, analysis.Spectrum, error) {
	in, err := input(workPath)
	if err != nil {
		return analysis.Peaks{}, analysis.Spectrum{}, err
	}
	pk, err := analysis.NewPeaksBuilder(sampleRate, channels, totalSamples)
	if err != nil {
		return analysis.Peaks{}, analysis.Spectrum{}, err
	}
	sp, err := analysis.NewSpectrumBuilder(sampleRate, channels)
	if err != nil {
		return analysis.Peaks{}, analysis.Spectrum{}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &sink{ch: channels, total: totalSamples, pk: pk, sp: sp, cancel: cancel}
	args := append([]string{"-nostdin", "-hide_banner", "-v", "error"}, in...)
	args = append(args, "-f", "f32le", "-acodec", "pcm_f32le", "-")
	_, runErr := mediatool.Run(ctx, f.bin, args, mediatool.RunOpts{Timeout: decodeTimeout, Stdout: s})
	if s.err != nil {
		return analysis.Peaks{}, analysis.Spectrum{}, s.err
	}
	if runErr != nil {
		return analysis.Peaks{}, analysis.Spectrum{}, runErr
	}
	if s.n+blockTolerance < totalSamples {
		return analysis.Peaks{}, analysis.Spectrum{}, fmt.Errorf("ffmpeg: decoded frame count %d is short of STREAMINFO %d", s.n, totalSamples)
	}
	return pk.Finish(), sp.Finish(), nil
}
