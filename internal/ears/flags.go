package ears

import (
	"math"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/library"
)

// flagInput is what the C51.2 rules read.
type flagInput struct {
	TruncatedABC, TruncatedSemantic *bool
	Loudness                        *library.LoudnessOut
	BandwidthHz                     *int
	Silence                         *SilenceBlock
	Clipping                        *ClippingBlock
	Tokens                          *TokenLoopsBlock
	Timed                           bool
	DurationS                       float64
	ExpectedS                       *float64
}

// Flag thresholds (C51.2). Changing one bumps the owning analyzer's definition version.
const (
	nearClippingDBTP     = -1.0
	internalSilenceMinS  = 2.0
	leadingSilenceMinS   = 3.0
	trailingSilenceMinS  = 5.0
	bandLimitedHz        = 16000
	loopSpanMinS         = 6.0
	loopRepeatsMin       = 3.0
	gzipWindowsMin       = 3
	gzipDropFactor       = 0.5
	durationMismatchFrac = 0.15
	edgeSlackS           = 0.05
	evidenceSpans        = 5
)

func truthy(b *bool) bool { return b != nil && *b }

func firstSpans(sp []analysis.Span, n int) []analysis.Span {
	out := make([]analysis.Span, 0, min(len(sp), n))
	return append(out, sp[:min(len(sp), n)]...)
}

// internalSpans are the silence spans that are neither the leading nor the trailing one. The stored
// leading_s / trailing_s say whether such an edge span exists.
func internalSpans(s *SilenceBlock) []analysis.Span {
	var out []analysis.Span
	for _, sp := range s.Spans {
		if s.LeadingS > 0 && sp.StartS < edgeSlackS {
			continue
		}
		if s.TrailingS > 0 && sp.EndS > s.DurationS-edgeSlackS {
			continue
		}
		out = append(out, sp)
	}
	return out
}

// computeFlags applies the C51.2 rules, in table order.
func computeFlags(in flagInput) []Flag {
	out := []Flag{}
	add := func(code, severity string, ev map[string]any) {
		out = append(out, Flag{Code: code, Severity: severity, Evidence: ev})
	}
	if truthy(in.TruncatedABC) || truthy(in.TruncatedSemantic) {
		add("truncated", "warn", map[string]any{"abc": truthy(in.TruncatedABC), "semantic": truthy(in.TruncatedSemantic)})
	}
	clipped := in.Clipping != nil && in.Clipping.ClippedFrames >= 1
	if clipped {
		add("clipping", "warn", map[string]any{"clipped_s": in.Clipping.ClippedS, "spans": firstSpans(in.Clipping.Spans, evidenceSpans)})
	}
	if !clipped && in.Loudness != nil && in.Loudness.TruePeakDBTP > nearClippingDBTP {
		add("near_clipping", "info", map[string]any{"true_peak_dbtp": in.Loudness.TruePeakDBTP})
	}
	if in.Silence != nil {
		var long []analysis.Span
		for _, sp := range internalSpans(in.Silence) {
			if sp.EndS-sp.StartS >= internalSilenceMinS {
				long = append(long, sp)
			}
		}
		if len(long) > 0 {
			add("internal_silence", "warn", map[string]any{"spans": firstSpans(long, evidenceSpans)})
		}
		if in.Silence.LeadingS >= leadingSilenceMinS {
			add("long_leading_silence", "info", map[string]any{"leading_s": in.Silence.LeadingS})
		}
		if in.Silence.TrailingS >= trailingSilenceMinS {
			add("long_trailing_silence", "info", map[string]any{"trailing_s": in.Silence.TrailingS})
		}
	}
	if in.BandwidthHz != nil && *in.BandwidthHz < bandLimitedHz {
		add("band_limited", "info", map[string]any{"bandwidth_hz": *in.BandwidthHz})
	}
	if in.Tokens != nil {
		if ev := loopEvidence(in.Tokens); ev != nil {
			add("token_loop_suspected", "warn", ev)
		}
	}
	if in.Timed && in.ExpectedS != nil && *in.ExpectedS > 0 && math.Abs(in.DurationS/(*in.ExpectedS)-1) > durationMismatchFrac+1e-9 {
		add("duration_mismatch", "info", map[string]any{"duration_s": in.DurationS, "expected_s": *in.ExpectedS})
	}
	return out
}

// loopEvidence returns the evidence of the first stream that looks like a loop, or nil.
func loopEvidence(t *TokenLoopsBlock) map[string]any {
	for _, st := range t.Streams {
		if p := st.PeriodicRun; p.SpanS >= loopSpanMinS && p.Repeats >= loopRepeatsMin {
			return map[string]any{"stream": st.Index, "at_s": p.AtS, "span_s": p.SpanS, "period_s": p.PeriodS}
		}
		if len(st.GzipRatio10s) >= gzipWindowsMin && st.GzipRatioMin != nil && st.GzipRatioMedian != nil &&
			*st.GzipRatioMin <= gzipDropFactor*(*st.GzipRatioMedian) {
			at := 0
			for i, v := range st.GzipRatio10s {
				if v == *st.GzipRatioMin {
					at = i
					break
				}
			}
			return map[string]any{"stream": st.Index, "window_at_s": float64(at) * 10, "ratio": *st.GzipRatioMin, "median": *st.GzipRatioMedian}
		}
	}
	return nil
}
