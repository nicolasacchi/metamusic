// Package earstest is TEST-ONLY: Tier A rows, a spectrogram rendition and fake
// analyzer/judgment collaborators for the Ears service. Import it only from
// _test.go files and servertest (see TestNoTestHelpersInBinary).
package earstest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/analysis/tokens"
	"github.com/nicolasacchi/metamusic/internal/ladder"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Tool is the version token the fixture rows carry.
const Tool = "ffmpeg n9.0.1"

// Fixture track ids (librarytest's).
const (
	ClipTrack = lt.TrackCitta // clipping 2.0 s + an internal silence span; no semantic.npy; Tier A complete
	LoopTrack = lt.TrackLuna  // token loop (period 25 tokens) + semantic.npy artifact; Tier A complete
)

// Ears lists what Fixture created.
type Ears struct {
	ClipTrack, LoopTrack string
	SpectroSHA           string // sha256 of the shared spectrogram JPEG
	SpectroBytes         int64
	SpectroWidth         int
	SpectroHeight        int
	SemanticSHA256       string // artifact sha of LoopTrack's semantic.npy
}

// Image geometry of the fixture spectrogram (the plot is 1024x384 at x0 = 142, below a 48 px band).
const (
	JPEGWidth, JPEGHeight = 1308, 560
	Quality               = 85
)

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// GradientJPEG returns a w x h gradient JPEG; with the fixture geometry it is well under 100 000 bytes.
func GradientJPEG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x * 255 / w), G: uint8(y * 255 / h), B: 96, A: 255}) //nolint:gosec // bounded by w, h
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: Quality}) // cannot fail on an in-memory image
	return buf.Bytes()
}

func silenceMetrics(spans []analysis.Span, internal float64, dur float64) analysis.Silence {
	if spans == nil {
		spans = []analysis.Span{}
	}
	return analysis.Silence{ThresholdDB: -50, MinDurationS: 1, Spans: spans, InternalS: internal, TotalS: internal, DurationS: dur}
}

func clippingMetrics(peak float64, frames int, spans []analysis.Span) analysis.Clipping {
	if spans == nil {
		spans = []analysis.Span{}
	}
	return analysis.Clipping{FrameS: 0.1, PeakDBFS: peak, FullScaleFrames: frames, ClippedFrames: frames, ClippedS: float64(frames) / 10, Spans: spans}
}

// LoopReport is the token-loop result stored for LoopTrack: a 25-token period from 3.0 s, 13 repeats.
func LoopReport() tokens.Report {
	gmin, gmed := 0.12, 0.35
	return tokens.Report{DType: "<i8", Shape: []int{1, 600}, TimeAxis: 1, Streams: []tokens.Stream{{
		Index: 0, NTokens: 600, DistinctTokens: 140, TokensPerS: 50, Distinct4GramRatio: 0.41,
		LongestRepeat: tokens.Repeat{Tokens: 300, S: 6, FirstAtS: 3, SecondAtS: 9},
		PeriodicRun:   tokens.Periodic{PeriodTokens: 25, PeriodS: 0.5, SpanTokens: 325, SpanS: 6.5, AtS: 3, Repeats: 13},
		GzipRatio10s:  []float64{0.35}, GzipRatioMin: &gmin, GzipRatioMedian: &gmed, Encoding: "u16le"}}}
}

// Fixture inserts Tier A analyses rows for two tracks, a semantic.npy artifact for LoopTrack and a
// spectrogram rendition (a Go-generated JPEG written under derived/) for both. It expects
// librarytest.Fixture to have run.
func Fixture(tb testing.TB, st *store.Store, data *os.Root, fx lt.Fixtures, now time.Time) Ears {
	tb.Helper()
	ctx := context.Background()
	exec := func(q string, a ...any) {
		tb.Helper()
		if _, err := st.W.ExecContext(ctx, q, a...); err != nil {
			tb.Fatal(err)
		}
	}
	analyses := func(track, analyzer, def string, v any) {
		tb.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			tb.Fatal(err)
		}
		exec(`INSERT INTO analyses(track_id, analyzer, version, tier, metrics_json, created_at) VALUES (?,?,?, 'A', ?, ?)`,
			track, analyzer, def+" "+Tool, string(b), rfc(now.Add(-time.Hour)))
	}
	// ClipTrack: a 2 s clipped burst and an internal silence.
	analyses(ClipTrack, "silence", analysis.SilenceDef, silenceMetrics([]analysis.Span{{StartS: 4, EndS: 6.5}}, 2.5, 6))
	analyses(ClipTrack, "clipping", analysis.ClippingDef, clippingMetrics(0, 20, []analysis.Span{{StartS: 2, EndS: 4}}))
	// LoopTrack: clean signal, a suspected token loop, its semantic.npy artifact.
	analyses(LoopTrack, "silence", analysis.SilenceDef, silenceMetrics(nil, 0, 12))
	analyses(LoopTrack, "clipping", analysis.ClippingDef, clippingMetrics(-1.5, 0, nil))
	analyses(LoopTrack, "token_loops", analysis.TokenLoopDef, LoopReport())

	npy := []byte("earstest-semantic-npy")
	npyPath := "takes/" + LoopTrack + "/semantic.npy"
	if err := data.MkdirAll("takes/"+LoopTrack, 0o750); err != nil {
		tb.Fatal(err)
	}
	if err := data.WriteFile(npyPath, npy, 0o640); err != nil {
		tb.Fatal(err)
	}
	semSHA := library.SHA256Hex(npy)
	exec(`INSERT INTO artifacts(track_id, name, sha256, bytes, path, created_at) VALUES (?, 'semantic.npy', ?, ?, ?, ?)`,
		LoopTrack, semSHA, len(npy), npyPath, rfc(now.Add(-2*time.Hour)))

	jpg := GradientJPEG(JPEGWidth, JPEGHeight)
	sha := library.SHA256Hex(jpg)
	rel := "derived/" + sha[:2] + "/" + sha + ".spectro.jpg"
	if err := data.MkdirAll("derived/"+sha[:2], 0o750); err != nil {
		tb.Fatal(err)
	}
	if err := data.WriteFile(rel, jpg, 0o444); err != nil {
		tb.Fatal(err)
	}
	params, _ := json.Marshal(map[string]any{"def": analysis.SpectroDef, "ffmpeg": "n9.0.1", "plot": "1024x384", "quality": Quality,
		"width": JPEGWidth, "height": JPEGHeight, "geometry": map[string]any{"x0": 142, "y0": 112, "w": 1024, "h": 384, "duration_s": 6},
		"overlay_sha256": lt.Sha("earstest-overlay"), "freq_scale": "log"})
	for _, id := range []string{ClipTrack, LoopTrack} {
		exec(`INSERT INTO renditions(track_id, kind, sha256, bytes, path, params_json, created_at) VALUES (?, 'spectrogram', ?, ?, ?, ?, ?)`,
			id, sha, len(jpg), rel, string(params), rfc(now.Add(-time.Hour)))
	}
	return Ears{ClipTrack: ClipTrack, LoopTrack: LoopTrack, SpectroSHA: sha, SpectroBytes: int64(len(jpg)),
		SpectroWidth: JPEGWidth, SpectroHeight: JPEGHeight, SemanticSHA256: semSHA}
}

// TierA is a fake ears.TierA: Signal returns Silence/Clipping (a clean signal by default) and
// SpectrumPicture writes a Go-generated PNG of the exact C50.2 geometry for 48 kHz. Calls are counted.
type TierA struct {
	mu                sync.Mutex
	Silence           analysis.Silence
	Clipping          analysis.Clipping
	SignalErr, PicErr error
	signals, pictures int
}

// NewTierA returns a TierA reporting no silence and no clipping.
func NewTierA() *TierA {
	return &TierA{Silence: silenceMetrics(nil, 0, 0), Clipping: clippingMetrics(-3, 0, nil)}
}

// ToolVersion implements ears.TierA.
func (a *TierA) ToolVersion() string { return "ffmpeg fake" }

// Signal implements ears.TierA.
func (a *TierA) Signal(_ context.Context, _ string, _ int, durationS float64) (analysis.Silence, analysis.Clipping, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.signals++
	if a.SignalErr != nil {
		return analysis.Silence{}, analysis.Clipping{}, a.SignalErr
	}
	s := a.Silence
	s.DurationS = durationS
	return s, a.Clipping, nil
}

// PlotX0 is ffmpeg's plot origin for a sample rate (C50.2).
func PlotX0(sampleRate int) int { return int((math.Log10(float64(sampleRate)) + 1) * 25) }

// SpectrumPicture implements ears.TierA: a (w+2*x0) x (h+128) gradient PNG at 48 kHz.
func (a *TierA) SpectrumPicture(_ context.Context, _, outPNG string, w, h int) error {
	a.mu.Lock()
	a.pictures++
	err := a.PicErr
	a.mu.Unlock()
	if err != nil {
		return err
	}
	x0 := PlotX0(48000)
	img := image.NewGray(image.Rect(0, 0, w+2*x0, h+128))
	for y := range img.Rect.Dy() {
		for x := range img.Rect.Dx() {
			img.SetGray(x, y, color.Gray{Y: uint8((x + y) % 256)})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return err
	}
	return os.WriteFile(outPNG, buf.Bytes(), 0o600)
}

// Calls reports how many Signal and SpectrumPicture runs happened.
func (a *TierA) Calls() (signal, picture int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.signals, a.pictures
}

// Judgments is a fake ears.Judgments answering from maps.
type Judgments struct {
	Pairs    map[[2]string]ladder.PairStats // key: {a, b} as requested
	Strength map[string]ladder.Strength
	Nu       float64
}

// PairStats implements ears.Judgments.
func (j *Judgments) PairStats(_ context.Context, a, b string) (ladder.PairStats, error) {
	return j.Pairs[[2]string{a, b}], nil
}

// Strengths implements ears.Judgments: the listed ids that have a strength, and nu.
func (j *Judgments) Strengths(_ context.Context, ids []string) (map[string]ladder.Strength, float64, error) {
	out := map[string]ladder.Strength{}
	for _, id := range ids {
		if s, ok := j.Strength[id]; ok {
			out[id] = s
		}
	}
	return out, j.Nu, nil
}

// String names the fixture for failure messages.
func (e Ears) String() string { return fmt.Sprintf("ears fixture (%s, %s)", e.ClipTrack, e.LoopTrack) }
