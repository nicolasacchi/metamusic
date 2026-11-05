// Package transform is the domain service of T1: CPU sound transforms whose result is an ordinary Take (engine dsp,
// D-T1-1). It never spends money and never calls a backend: it must not import api, jobs, spend, provider, generation or
// upload (TestImportGraph_T1). F1 freezes the contracts (spec, limits, codes, the kind registry); B2 and B3 build the
// service behind Service.
package transform

import (
	"log/slog"
	"os"
	"time"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Options carries the service's dependencies. B2 and B3 add the fields their lanes need (Lineage, Ears, Pipeline, Tools, Data,
// Fault, ...); F1 fixes only what the API layer needs to be wired.
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
	Resample Resampler                // second input at another rate (D-T1-19); nil refuses such an input until dsp.Resample lands
	// Toolchain reports the running toolchain for transform.json and Verify; nil means the build's own.
	Toolchain func() Toolchain
	// Changed is called with the transform id after every row change the runner makes, so the in-process hub of B2's Wait can
	// broadcast; nil-safe.
	Changed func(id string)
}

// Service implements the seven transform operations (C212). F1 builds the skeleton so the registry can wire it and its
// operations answer 501 NOT_IMPLEMENTED naming the lane that fills them.
type Service struct {
	o   Options
	run runState // the runner's state (runner.go)
}

// New builds the service.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{o: o}
}

// Enabled reports whether MM_TRANSFORM_ENABLED is on.
func (s *Service) Enabled() bool { return s != nil && s.o.Config.TransformEnabled }
