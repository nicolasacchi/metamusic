package ears

import (
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/analysis/tokens"
	"github.com/nicolasacchi/metamusic/internal/library"
)

func lufs(tp float64) *library.LoudnessOut {
	return &library.LoudnessOut{Loudness: analysis.Loudness{IntegratedLUFS: -14, TruePeakDBTP: tp}}
}

func codes(fs []Flag) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.Code)
	}
	return out
}

func TestFlags_Table(t *testing.T) {
	t.Parallel()
	sil := func(leading, trailing float64, spans ...analysis.Span) *SilenceBlock {
		return &SilenceBlock{Silence: analysis.Silence{Spans: spans, LeadingS: leading, TrailingS: trailing, DurationS: 60}}
	}
	clip := func(frames int, spans ...analysis.Span) *ClippingBlock {
		return &ClippingBlock{Clipping: analysis.Clipping{ClippedFrames: frames, ClippedS: float64(frames) / 10, Spans: spans}}
	}
	loop := func(s tokens.Stream) *TokenLoopsBlock { return &TokenLoopsBlock{Streams: []tokens.Stream{s}} }
	gz := func(lo, med float64, n int) tokens.Stream {
		return tokens.Stream{GzipRatio10s: make([]float64, n), GzipRatioMin: &lo, GzipRatioMedian: &med}
	}
	periodic := func(span, repeats float64) tokens.Stream {
		return tokens.Stream{PeriodicRun: tokens.Periodic{SpanS: span, Repeats: repeats, PeriodS: span / repeats, AtS: 3}}
	}
	cases := []struct {
		name string
		in   flagInput
		want []string
	}{
		{"nothing", flagInput{Loudness: lufs(-3), BandwidthHz: ptr(18000)}, []string{}},
		{"truncated abc", flagInput{TruncatedABC: ptr(true)}, []string{"truncated"}},
		{"truncated semantic", flagInput{TruncatedSemantic: ptr(true)}, []string{"truncated"}},
		{"truncated false", flagInput{TruncatedABC: ptr(false), TruncatedSemantic: ptr(false)}, []string{}},
		{"clipping one frame", flagInput{Clipping: clip(1, analysis.Span{StartS: 1, EndS: 1.1})}, []string{"clipping"}},
		{"clipping none", flagInput{Clipping: clip(0)}, []string{}},
		{"true peak -1.0 is not near clipping", flagInput{Loudness: lufs(-1.0)}, []string{}},
		{"true peak -0.99 is near clipping", flagInput{Loudness: lufs(-0.99)}, []string{"near_clipping"}},
		{"near clipping yields to clipping", flagInput{Loudness: lufs(-0.1), Clipping: clip(2)}, []string{"clipping"}},
		{"near clipping with a clean clipping run", flagInput{Loudness: lufs(-0.5), Clipping: clip(0)}, []string{"near_clipping"}},
		{"internal span 1.99 s", flagInput{Silence: sil(0, 0, analysis.Span{StartS: 10, EndS: 11.99})}, []string{}},
		{"internal span 2.0 s", flagInput{Silence: sil(0, 0, analysis.Span{StartS: 10, EndS: 12})}, []string{"internal_silence"}},
		{"long span at the start is leading, not internal", flagInput{Silence: sil(4, 0, analysis.Span{StartS: 0, EndS: 4})}, []string{"long_leading_silence"}},
		{"long span at the end is trailing, not internal", flagInput{Silence: sil(0, 6, analysis.Span{StartS: 54, EndS: 60})}, []string{"long_trailing_silence"}},
		{"leading 2.99 s", flagInput{Silence: sil(2.99, 0, analysis.Span{StartS: 0, EndS: 2.99})}, []string{}},
		{"leading 3.0 s", flagInput{Silence: sil(3.0, 0, analysis.Span{StartS: 0, EndS: 3})}, []string{"long_leading_silence"}},
		{"trailing 4.99 s", flagInput{Silence: sil(0, 4.99, analysis.Span{StartS: 55.01, EndS: 60})}, []string{}},
		{"trailing 5.0 s", flagInput{Silence: sil(0, 5.0, analysis.Span{StartS: 55, EndS: 60})}, []string{"long_trailing_silence"}},
		{"bandwidth 15999", flagInput{BandwidthHz: ptr(15999)}, []string{"band_limited"}},
		{"bandwidth 16000", flagInput{BandwidthHz: ptr(16000)}, []string{}},
		{"periodic span 5.99 s", flagInput{Tokens: loop(periodic(5.99, 5))}, []string{}},
		{"periodic 6 s x 2.9", flagInput{Tokens: loop(periodic(6, 2.9))}, []string{}},
		{"periodic 6 s x 3", flagInput{Tokens: loop(periodic(6, 3))}, []string{"token_loop_suspected"}},
		{"gzip 3 windows, min = half the median", flagInput{Tokens: loop(gz(0.2, 0.4, 3))}, []string{"token_loop_suspected"}},
		{"gzip 3 windows, just above half", flagInput{Tokens: loop(gz(0.2001, 0.4, 3))}, []string{}},
		{"gzip 2 windows", flagInput{Tokens: loop(gz(0.1, 0.4, 2))}, []string{}},
		{"duration ratio 1.15", flagInput{Timed: true, DurationS: 115, ExpectedS: ptr(100.0)}, []string{}},
		{"duration ratio 1.151", flagInput{Timed: true, DurationS: 115.1, ExpectedS: ptr(100.0)}, []string{"duration_mismatch"}},
		{"duration ratio 0.85", flagInput{Timed: true, DurationS: 85, ExpectedS: ptr(100.0)}, []string{}},
		{"duration ratio 0.849", flagInput{Timed: true, DurationS: 84.9, ExpectedS: ptr(100.0)}, []string{"duration_mismatch"}},
		{"duration mismatch needs timed sections", flagInput{Timed: false, DurationS: 10, ExpectedS: ptr(100.0)}, []string{}},
		{"everything, in table order", flagInput{
			TruncatedSemantic: ptr(true), Clipping: clip(3, analysis.Span{StartS: 1, EndS: 1.3}),
			Silence:  sil(3, 5, analysis.Span{StartS: 0, EndS: 3}, analysis.Span{StartS: 10, EndS: 13}, analysis.Span{StartS: 55, EndS: 60}),
			Loudness: lufs(0), BandwidthHz: ptr(9000), Tokens: loop(periodic(7, 4)), Timed: true, DurationS: 60, ExpectedS: ptr(30.0)},
			[]string{"truncated", "clipping", "internal_silence", "long_leading_silence", "long_trailing_silence", "band_limited", "token_loop_suspected", "duration_mismatch"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := codes(computeFlags(c.in))
			if !slices.Equal(got, c.want) {
				t.Errorf("flags = %v, want %v", got, c.want)
			}
			for _, f := range computeFlags(c.in) {
				if f.Evidence == nil || (f.Severity != "info" && f.Severity != "warn") {
					t.Errorf("flag %+v needs severity and evidence", f)
				}
			}
		})
	}
}

func TestFlags_SeverityAndEvidence(t *testing.T) {
	t.Parallel()
	spans := []analysis.Span{{StartS: 1, EndS: 1.1}, {StartS: 2, EndS: 2.1}, {StartS: 3, EndS: 3.1}, {StartS: 4, EndS: 4.1}, {StartS: 5, EndS: 5.1}, {StartS: 6, EndS: 6.1}, {StartS: 7, EndS: 7.1}}
	got := computeFlags(flagInput{
		TruncatedABC: ptr(true), Clipping: &ClippingBlock{Clipping: analysis.Clipping{ClippedFrames: 7, ClippedS: 0.7, Spans: spans}},
		Tokens: &TokenLoopsBlock{Streams: []tokens.Stream{{Index: 1, PeriodicRun: tokens.Periodic{SpanS: 7, Repeats: 4, PeriodS: 1.75, AtS: 3}}}},
	})
	by := map[string]Flag{}
	for _, f := range got {
		by[f.Code] = f
	}
	if f := by["truncated"]; f.Severity != "warn" || f.Evidence["abc"] != true || f.Evidence["semantic"] != false {
		t.Errorf("truncated = %+v", f)
	}
	if f := by["clipping"]; f.Severity != "warn" || f.Evidence["clipped_s"] != 0.7 || len(f.Evidence["spans"].([]analysis.Span)) != 5 {
		t.Errorf("clipping = %+v (evidence keeps at most 5 spans)", f)
	}
	f := by["token_loop_suspected"]
	if f.Severity != "warn" || f.Evidence["stream"] != 1 || f.Evidence["at_s"] != 3.0 || f.Evidence["span_s"] != 7.0 || f.Evidence["period_s"] != 1.75 {
		t.Errorf("token_loop_suspected = %+v", f)
	}
}
