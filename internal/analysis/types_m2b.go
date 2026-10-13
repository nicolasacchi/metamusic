package analysis

// Tier A definition identifiers (C49, C50). analyses.version = <definition> + " " + tool token.
const (
	SilenceDef   = "mm-silence/1"
	ClippingDef  = "mm-clipping/1"
	TokenLoopDef = "mm-token-loops/1" //nolint:gosec // a definition identifier, not a credential
	SpectroDef   = "mm-spectro/1"
)

// Span is a closed time interval in seconds.
type Span struct {
	StartS float64 `json:"start_s"`
	EndS   float64 `json:"end_s"`
}

// Silence is the silencedetect result (mm-silence/1).
type Silence struct {
	ThresholdDB    float64 `json:"threshold_db"`           // -50
	MinDurationS   float64 `json:"min_duration_s"`         // 1.0
	Spans          []Span  `json:"spans" nullable:"false"` // at most 100, then SpansTruncated
	SpansTruncated bool    `json:"spans_truncated"`        // more spans existed than were kept
	LeadingS       float64 `json:"leading_s"`              // duration of the leading span (0 if none)
	TrailingS      float64 `json:"trailing_s"`             // duration of the trailing span (0 if none)
	InternalS      float64 `json:"internal_s"`             // sum of internal span durations, over all spans
	TotalS         float64 `json:"total_s"`                // leading + internal + trailing
	DurationS      float64 `json:"duration_s"`             // the track duration used for closing spans
}

// Clipping is the astats-based clipping measurement (mm-clipping/1, D-M2b-8).
type Clipping struct {
	FrameS          float64 `json:"frame_s"`                // 0.1 (nominal)
	PeakDBFS        float64 `json:"peak_dbfs"`              // max over frames (-120 floor)
	FullScaleFrames int     `json:"full_scale_frames"`      // Peak_level >= -0.01
	ClippedFrames   int     `json:"clipped_frames"`         // and Flat_factor finite and > 0
	ClippedS        float64 `json:"clipped_s"`              // sum of clipped frame durations
	Spans           []Span  `json:"spans" nullable:"false"` // clipped frames merged across gaps <= 0.5 s; at most 100
	SpansTruncated  bool    `json:"spans_truncated"`
}
