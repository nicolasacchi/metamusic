package tape

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The frozen types of the tape service (C128). Lane T1 implements the Pipeline and lane T2 the Service methods; neither
// changes a name or a field here without a freeze fix.

// Item is one slot of the snapshot, in order: the resolved Take, its master and its loudness measure.
type Item struct {
	SlotID        string
	Position      int
	TakeID, Title string
	// Side is the slot's side label ("A"-"D") or ""; the chapter name carries it as a prefix (C128).
	Side                       string
	MasterRel, MasterSHA256    string
	MasterBytes                int64
	SampleRate, Bits, Channels int
	Lossy                      bool
	DurationS                  float64
	Measure                    Measure
}

// SlotWarning names a slot the snapshot flags: Code is "pin_ineligible" or "undecided_pick". Ids and codes only.
type SlotWarning struct{ SlotID, Code string }

// Snapshot is everything an export is rendered from, read in one transaction by recordview (C124.2). It carries no Muse
// text unless MuseNames is true (D-L2-16).
type Snapshot struct {
	RecordID    string
	Revision    int
	Status      string
	Title       string
	Items       []Item
	Tags        []flacmeta.Comment
	Description string
	CoverInput  cover.Input
	MuseNames   bool
	Warnings    []SlotWarning
}

// SnapOptions tunes Source.Snapshot.
type SnapOptions struct{ IncludeLiner bool }

// Source builds snapshots; recordview.Service implements it.
type Source interface {
	Snapshot(ctx context.Context, recordID string, o SnapOptions) (Snapshot, error)
}

// Loudness measures the Takes of a snapshot, analysing the missing ones first; ears adapts to it. A failure for one Take
// only marks that item unmeasured.
type Loudness interface {
	Ensure(ctx context.Context, takeIDs []string) (map[string]Measure, error)
}

// CoverRenderer is the part of the cover service a tape needs (the 512 px front cover).
type CoverRenderer interface {
	EnsureMosaic(ctx context.Context, in cover.Input, size int) (serve.Object, string, error)
}

// State is the lifecycle state of a tape row (C128.5).
type State string

// The tape states: queued, running, ready or failed; a ready tape expires after RetentionDays.
const (
	StateQueued  State = "queued"
	StateRunning State = "running"
	StateReady   State = "ready"
	StateFailed  State = "failed"
	StateExpired State = "expired"
)

// The error codes of a failed tape (tapes.error_code) and the request-time refusals (never stored on a row).
const (
	ErrDecodeFailed   = "TAPE_DECODE_FAILED"
	ErrEncodeFailed   = "TAPE_ENCODE_FAILED"
	ErrClipped        = "TAPE_CLIPPED"
	ErrSampleMismatch = "TAPE_SAMPLE_MISMATCH"
	ErrVerifyFailed   = "TAPE_VERIFY_FAILED"
	ErrDiskFull       = "TAPE_DISK_FULL"
	ErrSourceChanged  = "TAPE_SOURCE_CHANGED"
	ErrInterrupted    = "TAPE_INTERRUPTED"
	ErrNeedsReview    = "TAPE_NEEDS_REVIEW"
	ErrQuota          = "TAPE_QUOTA"
	ErrTooLong        = "TAPE_TOO_LONG"
)

// Progress is the phase a running tape reports, at most once per second: Phase is decode, encode, rewrite or verify.
type Progress struct {
	Phase       string
	Item, Items int
}

// Chapter is one chapter of a ready tape, from the cumulative sample counts (C128.4 step 5).
type Chapter struct {
	Index       int
	SlotID      string
	TakeID      string
	StartSample int64
	Samples     int64
	StartMS     int64
}

// PlannedItem is one item of the plan the pipeline renders: the gain, how it came about and the format flags.
type PlannedItem struct {
	Item      Item
	Gain      GainPlan
	Resampled bool // the source is not 48 kHz
	Upmixed   bool // a mono source duplicated to stereo
	Downmixed bool // more than two channels folded down
}

// Job is one render: the snapshot, the plan and the metadata the pipeline writes.
type Job struct {
	Snapshot   Snapshot
	Items      []PlannedItem
	TargetLUFS float64
	Chapters   []string // chapter names, aligned with Items (the lossy suffix and side prefix are already applied)
	Tags       []flacmeta.Comment
	CoverKey   string // key of the 512 px front cover
}

// Result is a finished render: the file is in the pipeline's work area at Path until the caller claims it and calls Cleanup.
type Result struct {
	Path         string // absolute path of the finished FLAC
	SHA256       string
	Bytes        int64
	TotalSamples int64
	Chapters     []Chapter
	PeakSample   float64 // largest post-gain sample peak, linear (REPLAYGAIN_ALBUM_PEAK)
	Cleanup      func()  // removes the work dir; never nil
}

// Pipeline renders a Job. Production: ffmpeg decode, the Go gain stage, the reference flac encoder, the Go metadata rewrite
// and verification (lane T1); tests use tapetest.FakePipeline.
type Pipeline interface {
	Render(ctx context.Context, j Job, progress func(Progress)) (Result, error)
}

// PipelineOptions configures the production Pipeline.
type PipelineOptions struct {
	Tools   mediatool.Tools
	Data    *os.Root
	DataDir string // absolute path of Data, for the argv of the tools
	Log     *slog.Logger
	Version string // the binary version, written into the vendor string and the key
}

// Options configures New. FlacVersion is the first line of `flac --version`, computed once by app.Build at startup (empty
// when the encoder is missing, which makes export answer 503 UNAVAILABLE {component: flac}); BuildVersion is the binary
// version. Both enter the input key.
type Options struct {
	Store        *store.Store
	Tools        mediatool.Tools
	Source       Source
	Loudness     Loudness
	Covers       CoverRenderer
	Data         *os.Root
	Pipeline     Pipeline
	Signer       *serve.Signer
	Now          func() time.Time
	NewID        func() string
	Owner        string
	Log          *slog.Logger
	FlacVersion  string
	BuildVersion string
}

// ExportInput is the request of Export. AcceptWarnings is honoured for human principals only (D-L2-33).
type ExportInput struct {
	Revision       *int
	TargetLUFS     *float64
	IncludeLiner   bool
	AcceptWarnings bool
}

// ItemPlan is the gain of one item as the Tape reports it.
type ItemPlan struct {
	SlotID, TakeID            string
	Position                  int
	GainCDB                   int
	Status                    GainStatus
	PredictedLUFS             *float64
	Lossy, Resampled, Upmixed bool
	Downmixed                 bool
}

// Spread is the loudness spread of the unclamped items before and after the gains, in LU.
type Spread struct{ Before, After float64 }

// Download is the signed link of a ready tape.
type Download struct {
	URL       string
	Filename  string
	Bytes     int64
	ExpiresAt string
	CueURL    *string
}

// Failure is the error of a failed tape: a stable code and a message with ids only.
type Failure struct{ Code, Message string }

// Tape is one tapes row as the service returns it.
type Tape struct {
	ID               string
	RecordID         string
	RecordRevision   int
	State            State
	Reused           bool
	Progress         *Progress
	TargetLUFS       float64
	SpreadLU         *Spread
	Items            []ItemPlan
	Chapters         []Chapter // nil until ready
	Warnings         []SlotWarning
	AcceptedWarnings bool
	ContainsLossy    bool
	LossySources     int
	TotalMS          *int64
	Bytes            *int64
	SHA256           *string
	Download         *Download
	Error            *Failure
	CreatedAt        string
	StartedAt        *string
	FinishedAt       *string
}

// Brief is a tape's line in a Record view.
type Brief struct {
	ID            string
	State         State
	CreatedAt     string
	FinishedAt    *string
	ContainsLossy bool
	Bytes         *int64
	ExpiresAt     *string
}
