package app

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/nicolasacchi/metamusic/internal/attention"
	"github.com/nicolasacchi/metamusic/internal/callbacks"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/songpr"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/trackctx"
	"github.com/nicolasacchi/metamusic/internal/voice"
)

// m5Services are the M5 services Build constructs (C250). Section duels and pair suggestions live on the existing Duels service.
type m5Services struct {
	SongPR    *songpr.Service
	Callbacks *callbacks.Service
	Attention *attention.Service
	Voice     *voice.Service
}

// m5Inputs is what the M5 wiring files draw on.
type m5Inputs struct {
	st       *store.Store
	now      func() time.Time
	newID    func() string
	notifier notify.Notifier
	cfg      config.Config
	late     *m5Late
	ctxb     *trackctx.Builder // M5-A1 wires the close-the-loop reader into /context
}

// buildM5 constructs the M5 services and fills the late seams; each lane fills only its own m5_<lane>.go (the L3 pattern).
// Nothing is started.
func buildM5(in m5Inputs) *m5Services {
	s := &m5Services{}
	buildM5SongPR(in, s)
	buildM5Callbacks(in, s)
	buildM5Attention(in, s)
	buildM5Voice(in, s)
	return s
}

// m5Late holds the seams the M3 services need before the M5 services exist: buildM3 hands them to the ingest bridge and to
// generation, buildM5 points them at the real services. A seam with no target behaves as if it were not wired: the take hook
// does nothing (TestIngest_NoHook_Unchanged), the backlog gate lets everything through, the patch source reports
// errors.ErrUnsupported. Build runs single-threaded before anything starts, so the plain fields need no lock.
type m5Late struct {
	hook    lateTakeHook
	patches latePatchSource
	backlog lateBacklogGate
}

// check refuses a wiring in which generation can create Song PR renders but the other seams are not there: without the take hook a
// Song PR child would land without its callbacks (or, with the callbacks service unconfigured, its take would fail ingest after the GPU
// has been paid for), and without the backlog gate an agent would not be paced by the operator's listening. Build calls it once the
// M5 services exist; a wiring with no Song PR source (the PatchSource stays unsupported, so no song_pr job can be created) is fine.
func (l *m5Late) check() error {
	if l.patches.target == nil {
		return nil
	}
	if l.hook.target == nil {
		return errors.New("app: Song PR renders are wired without the callbacks take hook: a child would land without its callbacks")
	}
	if l.backlog.target == nil {
		return errors.New("app: Song PR renders are wired without the callback backlog gate")
	}
	return nil
}

type lateTakeHook struct{ target jobs.TakeHook }

func (l *lateTakeHook) AfterTakeTx(ctx context.Context, tx *sql.Tx, t jobs.TakeInfo) error {
	if l.target == nil {
		return nil
	}
	return l.target.AfterTakeTx(ctx, tx, t)
}

func (l *lateTakeHook) AfterTakeCommit(ctx context.Context, t jobs.TakeInfo) {
	if l.target != nil {
		l.target.AfterTakeCommit(ctx, t)
	}
}

type latePatchSource struct{ target generation.PatchSource }

func (l *latePatchSource) PatchForEstimate(ctx context.Context, q lineage.Querier, id string) (generation.PatchRow, error) {
	if l.target == nil {
		return generation.PatchRow{}, errors.ErrUnsupported
	}
	return l.target.PatchForEstimate(ctx, q, id)
}

func (l *latePatchSource) RecheckComments(ctx context.Context, q lineage.Querier, id string) error {
	if l.target == nil {
		return errors.ErrUnsupported
	}
	return l.target.RecheckComments(ctx, q, id)
}

func (l *latePatchSource) RenderCount(ctx context.Context, q lineage.Querier, id string) (int, error) {
	if l.target == nil {
		return 0, errors.ErrUnsupported
	}
	return l.target.RenderCount(ctx, q, id)
}

type lateBacklogGate struct{ target generation.BacklogGate }

func (l *lateBacklogGate) Backlog(ctx context.Context, q lineage.Querier, root string) (int, error) {
	if l.target == nil {
		return 0, nil
	}
	return l.target.Backlog(ctx, q, root)
}

func (l *lateBacklogGate) Limit() int {
	if l.target == nil {
		return 0
	}
	return l.target.Limit()
}
