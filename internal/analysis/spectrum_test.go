package analysis

import (
	"math"
	"testing"
)

func feed(t *testing.T, sr, ch, n int, gen func(i int) float64) Spectrum {
	t.Helper()
	b, err := NewSpectrumBuilder(sr, ch)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]float32, 0, 5000*ch)
	for i := 0; i < n; i++ {
		v := float32(gen(i))
		for c := 0; c < ch; c++ {
			buf = append(buf, v)
		}
		if len(buf) >= 4999*ch || i == n-1 {
			b.Write(buf)
			buf = buf[:0]
		}
	}
	return b.Finish()
}

func TestSpectrum_SineBandwidthAndSilence(t *testing.T) {
	s := feed(t, 48000, 2, 48000, func(i int) float64 { return 0.5 * math.Sin(2*math.Pi*5000*float64(i)/48000) })
	if s.BandwidthHz == nil || *s.BandwidthHz < 5000 || *s.BandwidthHz > 5400 {
		t.Fatalf("bandwidth %v", s.BandwidthHz)
	}
	if s.Frames != 5 {
		t.Fatalf("frames %d", s.Frames)
	}
	if math.Abs(s.ThresholdDB-(s.RefDB-70)) > 1e-9 {
		t.Fatal("threshold must be ref-70")
	}
	z := feed(t, 48000, 1, 48000, func(int) float64 { return 0 })
	if z.BandwidthHz != nil || z.Frames != 0 {
		t.Fatalf("silence: %+v", z)
	}
	// below -60 dBFS RMS is skipped
	q := feed(t, 48000, 1, 48000, func(i int) float64 { return 0.0005 * math.Sin(float64(i)) })
	if q.BandwidthHz != nil {
		t.Fatal("quiet frames must be skipped")
	}
	// shorter than one frame
	if sh := feed(t, 48000, 1, 100, func(int) float64 { return 0.5 }); sh.BandwidthHz != nil {
		t.Fatal("short input")
	}
}

func TestSpectrum_BadParams(t *testing.T) {
	if _, err := NewSpectrumBuilder(0, 1); err == nil {
		t.Fatal("rate")
	}
	if _, err := NewSpectrumBuilder(48000, 0); err == nil {
		t.Fatal("channels")
	}
}
