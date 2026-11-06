// Package transform is the domain service of T1: CPU sound transforms whose result is an ordinary Take (engine dsp,
// D-T1-1). It never spends money and never calls a backend: it must not import api, jobs, spend, provider, generation or
// upload (TestImportGraph_T1). F1 freezes the contracts (spec, limits, codes, the kind registry); B2 and B3 build the
// service behind Service.
package transform

import (
	"context"
	"log/slog"
	"os"
	"time"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/features"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Options carries the service's dependencies. B2 and B3 add the fields their lanes need; F1 fixes only what the API layer needs
// to be wired.
type Options struct {
	Store   *store.Store
	Library *library.Service
	Config  config.Config
	Now     func() time.Time
	NewID   func() string
	// Wake nudges the runner after a commit; nil-safe, injected, never an import of the runner's package (D-T1-17).
	Wake func()

	// Run side (lane B3, C210). Everything below is optional for the read/create side: a Service built without Data and
	// Pipeline can estimate, create and read, and its runner idles.
	Ears     Ears            // Tier A after ingest and the parent's measured loudness
	Pipeline Ingester        // the M1 freeze (*ingest.Pipeline)
	Media    Media           // decode/encode steps; nil means ToolMedia over Tools
	Tools    mediatool.Tools // ffmpeg, flac, metaflac
	Data     *os.Root        // the data root work dirs and takes live under
	DataDir  string          // absolute path Data was opened on
	Owner    string          // runner id of the row lease (D-T1-17); default "transform-runner"
	Self     string          // the binary path (os.Executable) the supervisor starts as `dsp-run`
	Log      *slog.Logger
	Fault    func(point string) error // tests: after_decode, after_child, after_encode, after_ingest
	Child    ChildRunner              // nil means the exec supervisor over Self; tests inject transformtest.FakeChild
	Resample Resampler                // second input at another rate (D-T1-19); the app wires dsp.Resample, nil refuses such an input
	// Toolchain reports the running toolchain for transform.json and Verify; nil means the build's own.
	Toolchain func() Toolchain
	// Changed is called with the transform id after every row change the runner makes, so the in-process hub of B2's Wait can
	// broadcast; nil-safe.
	Changed func(id string)

	// --- spec side (T1-B2) ---

	// Features reads take_features rows for the alignment ladder (D-T1-9). nil builds one on Store.
	Features *features.Service
	// ToolStatus reports which media tools exist; nil assumes ffmpeg and flac are present and fluidsynth is not.
	ToolStatus func() ToolStatus
	// ScoreABC returns the score.abc text of a Take that has one, the last rung of the key ladder (C204.2). nil skips the rung:
	// an estimate reads the database only (D-T1-6), so the app supplies this hook only where a file read is acceptable.
	ScoreABC func(ctx context.Context, trackID string) (abc string, ok bool)
	// FreeBytes is the space available under dir (the disk limit of C209.4); nil skips the check.
	FreeBytes func(dir string) (uint64, error)
	// RenderWarnings returns the render-time warnings recorded in a finished transform's transform.json (B3 reads the artifact);
	// nil means none.
	RenderWarnings func(ctx context.Context, takeID string) ([]Warning, error)
	// SoundfontSHA12 is the first 12 hex digits of a configured soundfont's sha256 for the capabilities answer (T1b); nil omits it.
	SoundfontSHA12 func(config.Soundfont) string
}

// ToolStatus is the media tools the host has (C212.3 tools block).
type ToolStatus struct {
	FFmpeg, FLAC, Fluidsynth bool
	FluidsynthVersion        string
}

// Service implements the seven transform operations (C212). F1 builds the skeleton so the registry can wire it and its
// operations answer 501 NOT_IMPLEMENTED naming the lane that fills them.
type Service struct {
	o   Options
	run runState // the runner's state (runner.go)
	hub *hub
}

// New builds the service.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.NewID == nil {
		o.NewID = func() string { return uuid.NewV7().String() }
	}
	if o.Features == nil && o.Store != nil {
		o.Features = features.New(o.Store, o.Now)
	}
	svc := &Service{o: o, hub: newHub()}
	prev := o.Changed
	svc.o.Changed = func(id string) { // the runner's row changes wake B2's Wait
		svc.hub.notify(id)
		if prev != nil {
			prev(id)
		}
	}
	return svc
}

// Enabled reports whether MM_TRANSFORM_ENABLED is on.
func (s *Service) Enabled() bool { return s != nil && s.o.Config.TransformEnabled }
