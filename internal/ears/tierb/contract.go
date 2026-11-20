// Package tierb is the pure server-side half of Tier B (M5b "listening checks"): the strict parser of the worker's
// raw analysis.json (contract mm.ears-b/1), text normalisation, word alignment, diction and measured sections.
// It imports only the standard library, golang.org/x/text, internal/textfold and internal/engine/yue2, and never
// internal/jobs, internal/generation or internal/spend (C330.1).
package tierb

// ContractID is the value of the "contract" member of every analysis.json.
const ContractID = "mm.ears-b/1"

// Document limits (C332.2). MaxDocBytes is G1's artifact maximum for analysis.json.
const (
	MaxDocBytes    = 4 << 20
	MaxWords       = 20000
	MaxSegments    = 4000
	MaxPitchFrames = 36000 // 720 s at 20 ms
	MaxErrors      = 20
	PitchHopS      = 0.02
)

// Status of a document: ok iff the asr stage is ok.
const (
	StatusOK    = "ok"
	StatusError = "error"
)

// Stage outcomes.
const (
	StageOK           = "ok"
	StageError        = "error"
	StageSkipped      = "skipped"
	StageNotRequested = "not_requested"
)

// Error codes of a document with status error and of Raw.Errors.
const (
	CodeModelsMissing    = "MODELS_MISSING"
	CodeInputDecode      = "INPUT_DECODE"
	CodeSeparationFailed = "SEPARATION_FAILED"
	CodeASRFailed        = "ASR_FAILED"
	CodeDeadline         = "DEADLINE"
	CodeOOM              = "OOM"
	CodeSubprocessFailed = "SUBPROCESS_FAILED"
)

// Raw is a parsed analysis.json. Nullable members are pointers (nil = JSON null).
type Raw struct {
	Contract  string       `json:"contract"`
	Status    string       `json:"status"`
	Code      *string      `json:"code"`
	Stages    Stages       `json:"stages"`
	Models    Models       `json:"models"`
	Input     *Input       `json:"input"`
	Timings   Timings      `json:"timings"`
	Resources Resources    `json:"resources"`
	ASR       *ASR         `json:"asr"`
	Pitch     *Pitch       `json:"pitch"`
	Vocals    *Vocals      `json:"vocals"`
	Errors    []ErrorEntry `json:"errors"`
}

// Stages holds one outcome per stage.
type Stages struct {
	Separate   string `json:"separate"`
	ASR        string `json:"asr"`
	Transcribe string `json:"transcribe"`
}

// Models records the pinned models used (nil when the stage did not run).
type Models struct {
	Separator *Model    `json:"separator"`
	ASR       *ASRModel `json:"asr"`
	Pitch     *Model    `json:"pitch"`
}

// Model is a pinned model.
type Model struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// ASRModel is the ASR model with its compute type.
type ASRModel struct {
	Name        string `json:"name"`
	SHA256      string `json:"sha256"`
	ComputeType string `json:"compute_type"`
}

// Input describes the decoded audio (nil when decoding failed).
type Input struct {
	DurationS  float64 `json:"duration_s"`
	SampleRate int     `json:"sample_rate"`
	Channels   int     `json:"channels"`
	Codec      string  `json:"codec"`
}

// Timings are subprocess wall seconds per phase (0 for a phase that did not run).
type Timings struct {
	LoadS       float64 `json:"load_s"`
	SeparateS   float64 `json:"separate_s"`
	ASRS        float64 `json:"asr_s"`
	TranscribeS float64 `json:"transcribe_s"`
	TotalS      float64 `json:"total_s"`
}

// Resources are GPU observations for calibration.
type Resources struct {
	Device            string `json:"device"`
	PeakVRAMMiB       int    `json:"peak_vram_mib"`
	ParentReservedMiB *int   `json:"parent_reserved_mib"`
}

// ASR is the recogniser output: words with times, grouped into segments.
type ASR struct {
	Language string    `json:"language"`
	Forced   bool      `json:"forced"`
	Segments []Segment `json:"segments"`
	Words    []Word    `json:"words"`
}

// Segment indexes Words half-open: Words[W0:W1].
type Segment struct {
	Start      float64 `json:"start"`
	End        float64 `json:"end"`
	AvgLogprob float64 `json:"avg_logprob"`
	NoSpeechP  float64 `json:"no_speech_p"`
	W0         int     `json:"w0"`
	W1         int     `json:"w1"`
}

// Word is one recognised word with its time span and probability.
type Word struct {
	W     string  `json:"w"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	P     float64 `json:"p"`
}

// Pitch is the vocal pitch contour: Cents is 100 x the MIDI note number (0 = unvoiced), Conf is 0..100.
type Pitch struct {
	HopS  float64 `json:"hop_s"`
	Cents []int   `json:"cents"`
	Conf  []int   `json:"conf"`
}

// Vocals summarises the separated vocal stem.
type Vocals struct {
	RMSdBFS float64 `json:"rms_dbfs"`
	ActiveS float64 `json:"active_s"`
}

// ErrorEntry is one recorded stage failure.
type ErrorEntry struct {
	Stage   string `json:"stage"`
	Code    string `json:"code"`
	Message string `json:"message"`
}
