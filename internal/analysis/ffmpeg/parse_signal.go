package ffmpeg

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/analysis"
)

const (
	maxSpans        = 100
	silenceEdgeS    = 0.05
	clipMergeGapS   = 0.5
	fullScaleDB     = -0.01
	signalFrameS    = 0.1
	silenceThreshDB = -50.0
	silenceMinS     = 1.0
)

type sigFrame struct {
	t        float64
	peak     float64
	flat     float64
	havePeak bool
	haveFlat bool
}

func parseSigFloat(s string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64) // accepts inf, -inf, nan
	if err != nil {
		return 0, err
	}
	return v, nil
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// ParseSignal parses the ametadata stream of the C49.1 invocation. Frame
// durations come from pts_time deltas (the last frame closes at durationS), so
// any framing gives the same spans.
func ParseSignal(r io.Reader, durationS float64) (analysis.Silence, analysis.Clipping, error) {
	var (
		frames []sigFrame
		spans  [][2]float64
		open   = math.NaN()
	)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "frame:"):
			f := strings.Fields(line)
			if len(f) < 3 || !strings.HasPrefix(f[2], "pts_time:") {
				return analysis.Silence{}, analysis.Clipping{}, errors.New("ffmpeg: malformed frame line")
			}
			t, err := parseSigFloat(strings.TrimPrefix(f[2], "pts_time:"))
			if err != nil || math.IsNaN(t) || math.IsInf(t, 0) {
				return analysis.Silence{}, analysis.Clipping{}, errors.New("ffmpeg: malformed pts_time")
			}
			frames = append(frames, sigFrame{t: t})
		case strings.HasPrefix(line, "lavfi.astats.Overall.Peak_level="), strings.HasPrefix(line, "lavfi.astats.Overall.Flat_factor="):
			if len(frames) == 0 {
				return analysis.Silence{}, analysis.Clipping{}, errors.New("ffmpeg: value before first frame")
			}
			k, v, _ := strings.Cut(line, "=")
			x, err := parseSigFloat(v)
			if err != nil {
				return analysis.Silence{}, analysis.Clipping{}, fmt.Errorf("ffmpeg: malformed value for %s", k)
			}
			fr := &frames[len(frames)-1]
			if strings.HasSuffix(k, "Peak_level") {
				fr.peak, fr.havePeak = x, true
			} else {
				fr.flat, fr.haveFlat = x, true
			}
		case strings.HasPrefix(line, "lavfi.silence_start="):
			x, err := parseSigFloat(strings.TrimPrefix(line, "lavfi.silence_start="))
			if err != nil || math.IsNaN(x) || math.IsInf(x, 0) {
				return analysis.Silence{}, analysis.Clipping{}, errors.New("ffmpeg: malformed silence_start")
			}
			if !math.IsNaN(open) { // a start without an end closes at the next start
				spans = append(spans, [2]float64{open, x})
			}
			open = x
		case strings.HasPrefix(line, "lavfi.silence_end="):
			x, err := parseSigFloat(strings.TrimPrefix(line, "lavfi.silence_end="))
			if err != nil || math.IsNaN(x) || math.IsInf(x, 0) {
				return analysis.Silence{}, analysis.Clipping{}, errors.New("ffmpeg: malformed silence_end")
			}
			if math.IsNaN(open) {
				return analysis.Silence{}, analysis.Clipping{}, errors.New("ffmpeg: silence_end without silence_start")
			}
			spans = append(spans, [2]float64{open, x})
			open = math.NaN()
		}
	}
	if err := sc.Err(); err != nil {
		return analysis.Silence{}, analysis.Clipping{}, fmt.Errorf("ffmpeg: reading signal output: %w", err)
	}
	if len(frames) == 0 {
		return analysis.Silence{}, analysis.Clipping{}, errors.New("ffmpeg: signal output has no frames")
	}
	if !math.IsNaN(open) {
		spans = append(spans, [2]float64{open, durationS})
	}
	return buildSilence(spans, durationS), buildClipping(frames, durationS), nil
}

func buildSilence(raw [][2]float64, durationS float64) analysis.Silence {
	s := analysis.Silence{ThresholdDB: silenceThreshDB, MinDurationS: silenceMinS, DurationS: durationS, Spans: []analysis.Span{}}
	for _, p := range raw {
		a, b := math.Max(0, p[0]), math.Min(p[1], durationS)
		if b <= a {
			continue
		}
		d := b - a
		switch {
		case a < silenceEdgeS:
			s.LeadingS += d
		case b > durationS-silenceEdgeS:
			s.TrailingS += d
		default:
			s.InternalS += d
		}
		if len(s.Spans) < maxSpans {
			s.Spans = append(s.Spans, analysis.Span{StartS: round3(a), EndS: round3(b)})
		} else {
			s.SpansTruncated = true
		}
	}
	s.LeadingS, s.InternalS, s.TrailingS = round3(s.LeadingS), round3(s.InternalS), round3(s.TrailingS)
	s.TotalS = round3(s.LeadingS + s.InternalS + s.TrailingS)
	return s
}

func buildClipping(frames []sigFrame, durationS float64) analysis.Clipping {
	c := analysis.Clipping{FrameS: signalFrameS, PeakDBFS: floorDB, Spans: []analysis.Span{}}
	var (
		cur     analysis.Span
		have    bool
		clipped float64
	)
	emit := func() {
		if !have {
			return
		}
		if len(c.Spans) < maxSpans {
			c.Spans = append(c.Spans, analysis.Span{StartS: round3(cur.StartS), EndS: round3(cur.EndS)})
		} else {
			c.SpansTruncated = true
		}
		have = false
	}
	for i, fr := range frames {
		end := durationS
		if i+1 < len(frames) {
			end = frames[i+1].t
		}
		dur := math.Max(0, end-fr.t)
		if fr.havePeak && !math.IsNaN(fr.peak) {
			c.PeakDBFS = math.Max(c.PeakDBFS, math.Max(fr.peak, floorDB))
		}
		if !fr.havePeak || math.IsNaN(fr.peak) || fr.peak < fullScaleDB {
			continue
		}
		c.FullScaleFrames++
		if !fr.haveFlat || math.IsNaN(fr.flat) || math.IsInf(fr.flat, 0) || fr.flat <= 0 {
			continue
		}
		c.ClippedFrames++
		clipped += dur
		switch {
		case have && fr.t-cur.EndS <= clipMergeGapS+1e-9:
			cur.EndS = fr.t + dur
		default:
			emit()
			cur, have = analysis.Span{StartS: fr.t, EndS: fr.t + dur}, true
		}
	}
	emit()
	c.ClippedS = round3(clipped)
	c.PeakDBFS = round3(c.PeakDBFS)
	return c
}
