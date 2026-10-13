package ffmpeg_test

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
)

func parseFixture(t *testing.T, name string, dur float64) (analysis.Silence, analysis.Clipping) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s, c, err := ffmpeg.ParseSignal(f, dur)
	if err != nil {
		t.Fatal(err)
	}
	return s, c
}

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestParseSignal_Golden(t *testing.T) {
	t.Parallel()
	t.Run("clipped", func(t *testing.T) {
		t.Parallel()
		_, c := parseFixture(t, "signal_clipped_sine.txt", 10)
		if !near(c.ClippedS, 10, 1e-9) || len(c.Spans) != 1 || c.Spans[0].StartS != 0 || !near(c.Spans[0].EndS, 10, 1e-9) {
			t.Errorf("clipping = %+v", c)
		}
		if c.ClippedFrames != 100 || c.FullScaleFrames != 100 || c.PeakDBFS < -0.01 {
			t.Errorf("clipping = %+v", c)
		}
	})
	t.Run("just below full scale", func(t *testing.T) {
		t.Parallel()
		_, c := parseFixture(t, "signal_sine_0087.txt", 10)
		if c.ClippedFrames != 0 || c.ClippedS != 0 || c.FullScaleFrames != 0 || len(c.Spans) != 0 || !near(c.PeakDBFS, -0.087, 0.001) {
			t.Errorf("clipping = %+v", c)
		}
	})
	t.Run("single full-scale sample", func(t *testing.T) {
		t.Parallel()
		_, c := parseFixture(t, "signal_single_sample.txt", 10)
		if c.ClippedFrames != 0 || c.ClippedS != 0 || c.FullScaleFrames != 1 || len(c.Spans) != 0 {
			t.Errorf("clipping = %+v", c)
		}
	})
	t.Run("gaps", func(t *testing.T) {
		t.Parallel()
		s, _ := parseFixture(t, "signal_gaps.txt", 10)
		want := []analysis.Span{{StartS: 0, EndS: 1.2}, {StartS: 3, EndS: 5.5}, {StartS: 9, EndS: 10}}
		if len(s.Spans) != 3 {
			t.Fatalf("spans = %+v", s.Spans)
		}
		for i, w := range want {
			if !near(s.Spans[i].StartS, w.StartS, 0.01) || !near(s.Spans[i].EndS, w.EndS, 0.01) {
				t.Errorf("span %d = %+v, want %+v", i, s.Spans[i], w)
			}
		}
		if s.Spans[2].EndS != 10 {
			t.Errorf("trailing span not closed at duration: %+v", s.Spans[2])
		}
		if !near(s.InternalS, 2.5, 0.01) || !near(s.LeadingS, 1.2, 0.01) || !near(s.TrailingS, 1, 1e-9) {
			t.Errorf("silence = %+v", s)
		}
		if !near(s.TotalS, s.LeadingS+s.InternalS+s.TrailingS, 1e-9) || s.ThresholdDB != -50 || s.MinDurationS != 1 || s.DurationS != 10 {
			t.Errorf("silence = %+v", s)
		}
	})
}

func TestParseSignal_InfNan(t *testing.T) {
	t.Parallel()
	in := "frame:0    pts:0       pts_time:0\nlavfi.astats.Overall.Peak_level=-inf\nlavfi.astats.Overall.Flat_factor=-inf\n" +
		"frame:1    pts:4800    pts_time:0.1\nlavfi.astats.Overall.Peak_level=nan\nlavfi.astats.Overall.Flat_factor=nan\n" +
		"frame:2    pts:9600    pts_time:0.2\nlavfi.astats.Overall.Peak_level=0.000000\nlavfi.astats.Overall.Flat_factor=inf\n"
	_, c, err := ffmpeg.ParseSignal(strings.NewReader(in), 0.3)
	if err != nil {
		t.Fatal(err)
	}
	if c.PeakDBFS != 0 || c.FullScaleFrames != 1 || c.ClippedFrames != 0 {
		t.Errorf("clipping = %+v", c)
	}
}

func TestParseSignal_OtherFraming(t *testing.T) {
	t.Parallel()
	s1, _ := parseFixture(t, "signal_gaps.txt", 10)
	s2, _ := parseFixture(t, "signal_gaps_50ms.txt", 10)
	if len(s1.Spans) != len(s2.Spans) {
		t.Fatalf("%+v vs %+v", s1.Spans, s2.Spans)
	}
	for i := range s1.Spans {
		if !near(s1.Spans[i].StartS, s2.Spans[i].StartS, 0.05) || !near(s1.Spans[i].EndS, s2.Spans[i].EndS, 0.05) {
			t.Errorf("span %d: %+v vs %+v", i, s1.Spans[i], s2.Spans[i])
		}
	}
}

func TestParseSignal_Truncated(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", "\n", "lavfi.silence_start=1\n"} {
		if _, _, err := ffmpeg.ParseSignal(strings.NewReader(in), 10); err == nil {
			t.Errorf("no error for %q", in)
		}
	}
	if _, _, err := ffmpeg.ParseSignal(strings.NewReader("frame:0 pts:0 pts_time:x\n"), 10); err == nil {
		t.Error("bad pts_time accepted")
	}
}

func TestParseSignal_SpanCap(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString("frame:0    pts:0       pts_time:0\nlavfi.astats.Overall.Peak_level=-20\nlavfi.astats.Overall.Flat_factor=0\n")
	for i := 0; i < 150; i++ {
		fmt.Fprintf(&b, "lavfi.silence_start=%d\nlavfi.silence_end=%d.5\n", 10+4*i, 10+4*i+2)
	}
	s, _, err := ffmpeg.ParseSignal(strings.NewReader(b.String()), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Spans) != 100 || !s.SpansTruncated {
		t.Errorf("spans = %d truncated = %v", len(s.Spans), s.SpansTruncated)
	}
	if !near(s.InternalS, 150*2.5, 1e-6) {
		t.Errorf("internal_s = %v, want 375", s.InternalS)
	}
}
