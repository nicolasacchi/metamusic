package analysis

import (
	"math"
)

const (
	specN        = 8192
	bandHz       = 100
	silenceRMS   = 0.001 // -60 dBFS
	specRangeDB  = 70
	refMinBandHz = 50
)

// SpectrumBuilder implements mm-spectrum/1: Hann-windowed 8192-point frames of
// the mono mix, power averaged over voiced frames, grouped in 100 Hz bands.
// The reference level is the loudest band whose lower edge is >= 50 Hz
// (band 0 with DC and rumble is excluded); bandwidth is the upper edge of the
// highest band within 70 dB of it.
type SpectrumBuilder struct {
	sr, ch int
	win    []float64
	buf    []float64
	re, im []float64
	acc    []float64
	voiced int
}

// NewSpectrumBuilder prepares a builder for interleaved float32 input.
func NewSpectrumBuilder(sampleRate, channels int) (*SpectrumBuilder, error) {
	if err := checkStream(sampleRate, channels); err != nil {
		return nil, err
	}
	return &SpectrumBuilder{sr: sampleRate, ch: channels, win: Hann(specN), buf: make([]float64, 0, specN),
		re: make([]float64, specN), im: make([]float64, specN), acc: make([]float64, specN/2+1)}, nil
}

// Write consumes interleaved frames.
func (s *SpectrumBuilder) Write(samples []float32) {
	for off := 0; off+s.ch <= len(samples); off += s.ch {
		var m float64
		for _, v := range samples[off : off+s.ch] {
			m += float64(v)
		}
		s.buf = append(s.buf, m/float64(s.ch))
		if len(s.buf) == specN {
			s.frame()
			s.buf = s.buf[:0]
		}
	}
}

func (s *SpectrumBuilder) frame() {
	var sq float64
	for _, v := range s.buf {
		sq += v * v
	}
	if math.Sqrt(sq/specN) < silenceRMS {
		return
	}
	for i, v := range s.buf {
		s.re[i], s.im[i] = v*s.win[i], 0
	}
	FFT(s.re, s.im)
	for k := range s.acc {
		s.acc[k] += s.re[k]*s.re[k] + s.im[k]*s.im[k]
	}
	s.voiced++
}

// Finish returns the measurement; BandwidthHz is nil when no frame was voiced.
func (s *SpectrumBuilder) Finish() Spectrum {
	if s.voiced == 0 {
		return Spectrum{Frames: 0}
	}
	nb := int(float64(s.sr)/2/bandHz) + 1
	sum := make([]float64, nb)
	cnt := make([]int, nb)
	// The Nyquist bin (k == specN/2) is left out: it would form a band above sr/2
	// and report a bandwidth beyond Nyquist for full-band content.
	for k, p := range s.acc[:specN/2] {
		b := int(float64(k) * float64(s.sr) / specN / bandHz)
		if b >= nb {
			b = nb - 1
		}
		sum[b] += p
		cnt[b]++
	}
	level := make([]float64, nb)
	ref := math.Inf(-1)
	for b := range level {
		level[b] = math.Inf(-1)
		if cnt[b] == 0 {
			continue
		}
		if mean := sum[b] / float64(cnt[b]) / float64(s.voiced); mean > 0 {
			level[b] = 10 * math.Log10(mean)
		}
		if b*bandHz >= refMinBandHz {
			ref = math.Max(ref, level[b])
		}
	}
	if math.IsInf(ref, -1) {
		return Spectrum{Frames: s.voiced}
	}
	thr := ref - specRangeDB
	hi := -1
	for b := range level {
		if cnt[b] > 0 && level[b] >= thr {
			hi = b
		}
	}
	bw := (hi + 1) * bandHz
	return Spectrum{BandwidthHz: &bw, RefDB: ref, ThresholdDB: thr, Frames: s.voiced}
}
