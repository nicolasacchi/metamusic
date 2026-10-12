package analysis

import (
	"encoding/json"
	"errors"
	"math"
)

const detailPixelsPerSecond = 20

// PeaksBuilder accumulates min/max waveform levels from interleaved float32
// frames (C14). Frames beyond the declared total are ignored; missing tail
// pixels stay zero.
type PeaksBuilder struct {
	sr, ch int
	total  uint64
	n      uint64
	lv     [2]*level // overview, detail
}

type level struct {
	spp    uint64
	length int
	min    []float32
	max    []float32
	seen   []bool
}

func newLevel(total, spp uint64) *level {
	length := int((total + spp - 1) / spp) // #nosec G115 -- bounded by the FLAC sample count and spp >= 1
	return &level{spp: spp, length: length, min: make([]float32, length), max: make([]float32, length), seen: make([]bool, length)}
}

// NewPeaksBuilder prepares a builder for a stream of total frames.
func NewPeaksBuilder(sampleRate, channels int, totalSamples uint64) (*PeaksBuilder, error) {
	if sampleRate <= 0 || channels <= 0 {
		return nil, errors.New("analysis: peaks need a positive sample rate and channel count")
	}
	detail := uint64(math.Round(float64(sampleRate) / detailPixelsPerSecond))
	if detail < 1 {
		detail = 1
	}
	ov := (totalSamples + 2047) / 2048
	if ov < 1 {
		ov = 1
	}
	return &PeaksBuilder{sr: sampleRate, ch: channels, total: totalSamples,
		lv: [2]*level{newLevel(totalSamples, ov), newLevel(totalSamples, detail)}}, nil
}

// Write consumes interleaved frames (len must be a multiple of channels; a
// trailing partial frame is ignored).
func (b *PeaksBuilder) Write(samples []float32) {
	for off := 0; off+b.ch <= len(samples); off += b.ch {
		if b.n >= b.total {
			return
		}
		lo, hi := samples[off], samples[off]
		for _, v := range samples[off+1 : off+b.ch] {
			lo = min(lo, v)
			hi = max(hi, v)
		}
		for _, l := range b.lv {
			p := int(b.n / l.spp) // #nosec G115 -- b.n < total, so p < length
			if !l.seen[p] {
				l.seen[p], l.min[p], l.max[p] = true, lo, hi
				continue
			}
			l.min[p] = min(l.min[p], lo)
			l.max[p] = max(l.max[p], hi)
		}
		b.n++
	}
}

func quant(v float32) int8 {
	q := math.Round(float64(v) * 127)
	return int8(math.Max(-127, math.Min(127, q)))
}

// Finish returns the peaks.
func (b *PeaksBuilder) Finish() Peaks {
	mk := func(l *level) Waveform {
		w := Waveform{Version: 2, Channels: 1, SampleRate: b.sr, SamplesPerPixel: int(l.spp), Bits: 8, Length: l.length, Data: make([]int8, 2*l.length)} // #nosec G115 -- spp is derived from an int or fits it
		for i := 0; i < l.length; i++ {
			if l.seen[i] {
				w.Data[2*i], w.Data[2*i+1] = quant(l.min[i]), quant(l.max[i])
			}
		}
		return w
	}
	return Peaks{Overview: mk(b.lv[0]), Detail: mk(b.lv[1]), DurationS: float64(b.total) / float64(b.sr)}
}

// MarshalPeaks renders the peaks file: compact JSON plus a newline.
func MarshalPeaks(p Peaks) ([]byte, error) {
	b, err := json.Marshal(struct {
		Format    string   `json:"format"`
		DurationS float64  `json:"duration_s"`
		Overview  Waveform `json:"overview"`
		Detail    Waveform `json:"detail"`
	}{PeaksFormat, p.DurationS, p.Overview, p.Detail})
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
