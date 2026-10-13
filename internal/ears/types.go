// Package ears runs Tier A signal analysis per track and serves the dossier,
// the spectrogram view, compare_tracks and the /context perception block
// (C51). This file is the contract; the behaviour lands in M2b-E3.
package ears

import (
	"context"
	"os"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/analysis/tokens"
	"github.com/nicolasacchi/metamusic/internal/ladder"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TierA is the analyzer side of ears; *ffmpeg.FFmpeg implements it (earstest.TierA in tests).
type TierA interface {
	ToolVersion() string
	Signal(ctx context.Context, workPath string, sampleRate int, durationS float64) (analysis.Silence, analysis.Clipping, error)
	SpectrumPicture(ctx context.Context, workPath, outPNG string, w, h int) error
}

// Judgments is what ears reads from duels; *duels.Service implements it (earstest.Judgments in tests).
type Judgments interface {
	PairStats(ctx context.Context, a, b string) (ladder.PairStats, error)
	Strengths(ctx context.Context, ids []string) (map[string]ladder.Strength, float64, error)
}

// Options builds a Service.
type Options struct {
	Store     *store.Store
	Library   *library.Service
	Data      *os.Root
	DataDir   string
	Analyzer  TierA
	Missing   error // non-nil (from ffmpeg.RequireTierA) => AnalyzeTrack/Backfill run nothing and report TOOL_MISSING once
	Signer    *serve.Signer
	Judgments Judgments
	Now       func() time.Time
}

// Service is the Ears service. An unwired Service (Options{}) answers errors.ErrUnsupported.
type Service struct{ o Options }

// AnalyzeOptions tunes AnalyzeTrack.
type AnalyzeOptions struct{ Force bool }

// Outcome reports one track's analysis.
type Outcome struct {
	TrackID string
	Ran     []string
	Skipped []string
	Failed  map[string]string // analyzer => error code
}

// BackfillOptions tunes Backfill.
type BackfillOptions struct {
	TrackIDs []string
	Force    bool
	DryRun   bool
	Jobs     int // default 1
}

// BackfillReport sums a Backfill run.
type BackfillReport struct {
	Tracks, Analysed, Failed int
	Ran                      map[string]int
	Failures                 []Outcome
	ToolMissing              string // names the missing capability
}

// Flag is one computed finding (C51.2).
type Flag struct {
	Code     string         `json:"code"`
	Severity string         `json:"severity" enum:"info,warn"`
	Evidence map[string]any `json:"evidence" nullable:"false"`
}

// SilenceBlock is the stored silence analysis.
type SilenceBlock struct {
	analysis.Silence
	Version string `json:"version"`
}

// ClippingBlock is the stored clipping analysis.
type ClippingBlock struct {
	analysis.Clipping
	Version string `json:"version"`
}

// TokenLoopsBlock is the stored token-loop analysis of semantic.npy.
type TokenLoopsBlock struct {
	ArtifactSHA256 string          `json:"artifact_sha256"`
	DType          string          `json:"dtype"`
	Shape          []int           `json:"shape" nullable:"false"`
	TimeAxis       int             `json:"time_axis"`
	Streams        []tokens.Stream `json:"streams" nullable:"false"`
	Version        string          `json:"version"`
}

// StructureSection is one section of a track's structure.
type StructureSection struct {
	Idx    int      `json:"idx"`
	Label  string   `json:"label"`
	Kind   string   `json:"kind"`
	StartS *float64 `json:"start_s"`
	EndS   *float64 `json:"end_s"`
}

// Structure is the section view with the score-plan duration check.
type Structure struct {
	DurationS         float64            `json:"duration_s"`
	Timed             bool               `json:"timed"`
	Source            *string            `json:"source" doc:"plan | measured | manual; null when untimed"`
	Confidence        *float64           `json:"confidence"`
	ExpectedDurationS *float64           `json:"expected_duration_s"`
	Ratio             *float64           `json:"ratio" doc:"duration_s / expected_duration_s"`
	Sections          []StructureSection `json:"sections" nullable:"false"`
}

// SpectrogramInfo says whether a spectrogram exists for the dossier's track.
type SpectrogramInfo struct {
	Available bool   `json:"available"`
	Bytes     *int64 `json:"bytes"`
	Width     *int   `json:"width"`
	Height    *int   `json:"height"`
	Quality   *int   `json:"quality"`
}

// Provenance names the definition and tool of every analyzer behind a dossier.
type Provenance struct {
	Analyzers map[string]string `json:"analyzers" nullable:"false"`
}

// Dossier is the get_track_analysis answer (C51.4).
type Dossier struct {
	TrackID     string                 `json:"track_id"`
	Tier        string                 `json:"tier" enum:"0,A"`
	Complete    bool                   `json:"complete"`
	Missing     []string               `json:"missing" nullable:"false" doc:"Applicable analyzers that have not run"`
	Summary     string                 `json:"summary" maxLength:"300"`
	Loudness    *library.LoudnessOut   `json:"loudness"`
	Spectrum    *library.SpectrumOut   `json:"spectrum"`
	Silence     *SilenceBlockOrNull    `json:"silence"`
	Clipping    *ClippingBlockOrNull   `json:"clipping"`
	TokenLoops  *TokenLoopsBlockOrNull `json:"token_loops"`
	Structure   Structure              `json:"structure"`
	Flags       []Flag                 `json:"flags" nullable:"false"`
	BlindSpots  []string               `json:"blind_spots" nullable:"false" doc:"What the machine cannot judge; read before trusting the numbers"`
	Spectrogram SpectrogramInfo        `json:"spectrogram"`
	Provenance  Provenance             `json:"provenance"`
}

// ImageData is an image inside a JSON body; operations marked mcp_image
// return it to MCP clients as ImageContent (api.ImageData is an alias).
type ImageData struct {
	MIMEType   string `json:"mime_type"`
	DataBase64 string `json:"data_base64,omitempty" doc:"Base64 image bytes; omitted when the caller asked for a link only"`
}

// SpectrogramPlot places the plot rectangle inside the image.
type SpectrogramPlot struct {
	X0        int     `json:"x0"`
	Y0        int     `json:"y0"`
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	DurationS float64 `json:"duration_s"`
	FreqScale string  `json:"freq_scale" example:"log"`
}

// SpectrogramOverlay says what was drawn on top of the plot.
type SpectrogramOverlay struct {
	Timed      bool     `json:"timed"`
	Source     *string  `json:"source"`
	Confidence *float64 `json:"confidence"`
	LineTiming string   `json:"line_timing" example:"none" doc:"none: lyric lines are listed inside their section, not timed"`
}

// SpectrogramView is the get_track_spectrogram answer (C51.5).
type SpectrogramView struct {
	TrackID     string             `json:"track_id"`
	ContentType string             `json:"content_type" example:"image/jpeg"`
	SHA256      string             `json:"sha256"`
	Bytes       int64              `json:"bytes"`
	Width       int                `json:"width"`
	Height      int                `json:"height"`
	Quality     int                `json:"quality"`
	Plot        SpectrogramPlot    `json:"plot"`
	Overlay     SpectrogramOverlay `json:"overlay"`
	URL         string             `json:"url" doc:"Signed, relative URL; valid until expires_at"`
	ExpiresAt   string             `json:"expires_at" doc:"RFC 3339 UTC"`
	TTLS        int64              `json:"ttl_s"`
	BlindSpots  []string           `json:"blind_spots" nullable:"false"`
	Image       *ImageData         `json:"image,omitempty" doc:"Present when inline=true (default)"`
}

// Side describes one track of a comparison.
type Side struct {
	TrackID     string   `json:"track_id"`
	Title       string   `json:"title"`
	Seed        int64    `json:"seed"`
	RequestID   string   `json:"request_id"`
	RootTrackID string   `json:"root_track_id"`
	DurationS   float64  `json:"duration_s"`
	Tier        string   `json:"tier" enum:"0,A"`
	Flags       []string `json:"flags" nullable:"false"`
	RecipeKnown bool     `json:"recipe_known"`
	Lossy       bool     `json:"lossy"`
}

// Deltas are b minus a; null when either side lacks the metric.
type Deltas struct {
	IntegratedLUFS   *float64 `json:"integrated_lufs"`
	TruePeakDBTP     *float64 `json:"true_peak_dbtp"`
	LRALU            *float64 `json:"lra_lu"`
	BandwidthHz      *float64 `json:"bandwidth_hz"`
	DurationS        *float64 `json:"duration_s"`
	ClippedS         *float64 `json:"clipped_s"`
	InternalSilenceS *float64 `json:"internal_silence_s"`
	TokenLoopSpanS   *float64 `json:"token_loop_span_s"`
}

// FlagDiff splits flag codes by side.
type FlagDiff struct {
	OnlyA []string `json:"only_a" nullable:"false"`
	OnlyB []string `json:"only_b" nullable:"false"`
	Both  []string `json:"both" nullable:"false"`
}

// SeedPair is the two seeds of a recipe diff.
type SeedPair struct {
	A int64 `json:"a"`
	B int64 `json:"b"`
}

// StyleTermDiff lists style terms gained and lost going from a to b.
type StyleTermDiff struct {
	Added   []string `json:"added" nullable:"false"`
	Removed []string `json:"removed" nullable:"false"`
}

// RecipeDiff compares the two requests.
type RecipeDiff struct {
	SameHash   bool          `json:"same_hash"`
	Changed    []string      `json:"changed" nullable:"false" doc:"seed, style, lyrics and/or cot"`
	Seed       SeedPair      `json:"seed"`
	StyleTerms StyleTermDiff `json:"style_terms"`
}

// CompareLadder places both tracks on the global ladder.
type CompareLadder struct {
	A        *StrengthOrNull `json:"a"`
	B        *StrengthOrNull `json:"b"`
	Nu       float64         `json:"nu"`
	PABeatsB *float64        `json:"p_a_beats_b" doc:"null unless both tracks have counted duels"`
	PTie     *float64        `json:"p_tie"`
}

// Comparison is the compare_tracks answer (C51.6).
type Comparison struct {
	A          Side             `json:"a"`
	B          Side             `json:"b"`
	Relation   string           `json:"relation" enum:"siblings,parent_child,child_parent,same_lineage,unrelated"`
	Deltas     Deltas           `json:"deltas"`
	Flags      FlagDiff         `json:"flags"`
	RecipeDiff RecipeDiff       `json:"recipe_diff"`
	Duels      ladder.PairStats `json:"duels"`
	Ladder     CompareLadder    `json:"ladder"`
	BlindSpots []string         `json:"blind_spots" nullable:"false"`
}

// PerceptionSection is one section in the /context perception block.
type PerceptionSection struct {
	Label      string   `json:"label"`
	Start      *float64 `json:"start"`
	End        *float64 `json:"end"`
	Source     string   `json:"source"`
	Confidence *float64 `json:"confidence"`
}

// Perception is the /context perception block; keys and order are M2a C35's (C51.8).
type Perception struct {
	Tier        string              `json:"tier"`
	LUFSI       *float64            `json:"lufs_i"`
	TruePeakDB  *float64            `json:"true_peak_db"`
	LRALU       *float64            `json:"lra_lu"`
	BandwidthHz *float64            `json:"bandwidth_hz"`
	Sections    []PerceptionSection `json:"sections"`
	Timed       bool                `json:"timed"`
	Flags       []string            `json:"flags"`
	BlindSpots  []string            `json:"blind_spots"`
}
