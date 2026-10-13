// Package cliptest is TEST-ONLY: fakes for the clip service's collaborators so
// duels, the API and the rigs run without an ffmpeg subprocess. Import it only
// from _test.go files and servertest (see TestNoTestHelpersInBinary).
package cliptest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"

	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta/flactest"
)

// Tool is the ToolVersion every fake reports.
const Tool = "ffmpeg fake"

// DefaultMeasure is what an unlisted window measures.
var DefaultMeasure = clip.Measure{IntegratedLUFS: -20, TruePeakDBTP: -1}

// Renderer is a duels.ClipRenderer: Measure answers from a map keyed by the
// master's sha256, Ensure returns the clip's key without touching a file, and
// both count their calls. It is safe for concurrent use.
type Renderer struct {
	mu       sync.Mutex
	measures map[string]clip.Measure
	nMeasure int
	nEnsure  int
	// EnsureErr, when set, makes Ensure fail (a render failure leaves no duel).
	EnsureErr error
}

// NewRenderer returns a Renderer with no listed windows.
func NewRenderer() *Renderer { return &Renderer{measures: map[string]clip.Measure{}} }

// SetMeasure lists the window loudness of the master with this sha256.
func (r *Renderer) SetMeasure(masterSHA256 string, m clip.Measure) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.measures[masterSHA256] = m
}

// ToolVersion implements duels.ClipRenderer.
func (r *Renderer) ToolVersion() string { return Tool }

// Measure implements duels.ClipRenderer.
func (r *Renderer) Measure(_ context.Context, sp clip.Spec) (clip.Measure, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nMeasure++
	if m, ok := r.measures[sp.MasterSHA256]; ok {
		return m, nil
	}
	return DefaultMeasure, nil
}

// Ensure implements duels.ClipRenderer: the object a render would produce, with a plausible size.
func (r *Renderer) Ensure(_ context.Context, sp clip.Spec) (clip.Object, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nEnsure++
	if r.EnsureErr != nil {
		return clip.Object{}, r.EnsureErr
	}
	key := clip.Key(sp, Tool)
	s, e := sp.Samples()
	return clip.Object{Key: key, RelPath: "derived/clips/" + key[:2] + "/" + key + ".flac",
		Bytes: (e - s) / int64(sp.SampleRate) * 90000, DurationS: sp.W.DurationS(), Channels: sp.Channels}, nil
}

// Calls reports how many Measure and Ensure calls were made.
func (r *Renderer) Calls() (measure, ensure int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.nMeasure, r.nEnsure
}

// Analyzer is a clip.WindowAnalyzer: WindowLoudness answers from a map keyed by
// the sha256 of the file at workPath (the clip service hard-links the master
// there, so the key is the master's sha256).
type Analyzer struct {
	mu       sync.Mutex
	measures map[string]clip.Measure
}

// NewAnalyzer returns an Analyzer with no listed masters.
func NewAnalyzer() *Analyzer { return &Analyzer{measures: map[string]clip.Measure{}} }

// SetMeasure lists the window loudness of the master with this sha256.
func (a *Analyzer) SetMeasure(masterSHA256 string, m clip.Measure) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.measures[masterSHA256] = m
}

// ToolVersion implements clip.WindowAnalyzer.
func (a *Analyzer) ToolVersion() string { return Tool }

// WindowLoudness implements clip.WindowAnalyzer.
func (a *Analyzer) WindowLoudness(_ context.Context, workPath string, _, _ int64) (clip.Measure, error) {
	b, err := os.ReadFile(workPath) //nolint:gosec // test helper reading the work-dir master link
	if err != nil {
		return clip.Measure{}, err
	}
	sum := sha256.Sum256(b)
	a.mu.Lock()
	defer a.mu.Unlock()
	if m, ok := a.measures[hex.EncodeToString(sum[:])]; ok {
		return m, nil
	}
	return DefaultMeasure, nil
}

// Render is a clip.RenderFunc that writes a header-valid FLAC with the STREAMINFO
// a real render would produce (24 bit, 48 kHz, the source's channel count and
// the window's sample count), so the real clip.Service runs without ffmpeg.
func Render(_ context.Context, _, outPath string, sp clip.Spec) error {
	total := uint64(sp.W.EndMS-sp.W.StartMS) * clip.OutRate / 1000                                                                    //nolint:gosec // window is positive
	b := flactest.Build(flactest.Spec{SampleRate: clip.OutRate, Channels: uint8(sp.Channels), BitsPerSample: 24, TotalSamples: total, //nolint:gosec // 1 or 2
		Frames: []byte("cliptest-frames")})
	if err := os.MkdirAll(filepath.Dir(outPath), 0o750); err != nil {
		return err
	}
	return os.WriteFile(outPath, b, 0o600)
}
