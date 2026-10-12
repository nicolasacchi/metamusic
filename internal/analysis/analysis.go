// Package analysis defines the audio analyzer contract (C14): types, constants
// and the Analyzer interface. The ffmpeg-backed implementation lives in
// internal/analysis/ffmpeg (M1-B6).
package analysis

import (
	"context"
	"errors"
)

// Definition and format identifiers; analyses.version = <definition> + " " + ToolVersion().
const (
	LoudnessDef = "mm-loudness/1"
	SpectrumDef = "mm-spectrum/1"
	PeaksFormat = "mm.peaks/1"
)

// ErrToolMissing is returned when a required external capability is absent;
// the error text names the capability.
var ErrToolMissing = errors.New("analysis: required tool capability missing")

// Loudness is the EBU R128 measurement of one file.
type Loudness struct {
	IntegratedLUFS   float64   `json:"integrated_lufs"`
	LRA              float64   `json:"lra_lu"`
	LRALowLUFS       float64   `json:"lra_low_lufs"`
	LRAHighLUFS      float64   `json:"lra_high_lufs"`
	TruePeakDBTP     float64   `json:"true_peak_dbtp"`
	SamplePeakDBFS   float64   `json:"sample_peak_dbfs"`
	ShortTermMaxLUFS float64   `json:"short_term_max_lufs"`
	ShortTerm1Hz     []float64 `json:"short_term_1hz"` // one value per whole second, floor -70.0, 0.1 LU
}

// Spectrum is the bandwidth measurement (mm-spectrum/1).
type Spectrum struct {
	BandwidthHz *int    `json:"bandwidth_hz"` // nil for silence
	RefDB       float64 `json:"ref_db"`
	ThresholdDB float64 `json:"threshold_db"`
	Frames      int     `json:"frames"`
}

// Waveform is one audiowaveform JSON v2 level.
type Waveform struct {
	Version         int    `json:"version"`
	Channels        int    `json:"channels"`
	SampleRate      int    `json:"sample_rate"`
	SamplesPerPixel int    `json:"samples_per_pixel"`
	Bits            int    `json:"bits"`
	Length          int    `json:"length"`
	Data            []int8 `json:"data"`
}

// Peaks is the peaks file content (format mm.peaks/1).
type Peaks struct {
	Overview  Waveform `json:"overview"`
	Detail    Waveform `json:"detail"`
	DurationS float64  `json:"duration_s"`
}

// Analyzer measures a work-dir FLAC file.
type Analyzer interface {
	// ToolVersion is "ffmpeg <raw token>".
	ToolVersion() string
	Loudness(ctx context.Context, workPath string) (Loudness, error)
	Scan(ctx context.Context, workPath string, sampleRate, channels int, totalSamples uint64) (Peaks, Spectrum, error)
}

// Input limits for the builders: STREAMINFO comes from untrusted files, and the
// peaks buffers are sized from it before any audio is decoded.
const (
	minSampleRate = 8000
	maxSampleRate = 768000
	maxChannels   = 32
	maxDetailPx   = 2 << 20 // ~29 h at the 20 px/s detail level
)

func checkStream(sampleRate, channels int) error {
	if sampleRate < minSampleRate || sampleRate > maxSampleRate {
		return errors.New("analysis: sample rate outside 8000-768000 Hz")
	}
	if channels <= 0 || channels > maxChannels {
		return errors.New("analysis: channel count outside 1-32")
	}
	return nil
}
