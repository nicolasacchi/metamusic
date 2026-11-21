// Package earsjobs wires Tier B listening checks into the job pipeline: the take hook that analyses a render's
// analysis.json after the take commits. It imports internal/jobs and internal/ears; internal/ears itself cannot import jobs,
// because jobs already imports ears (M5b C330.1).
package earsjobs

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"slices"

	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/jobspec"
	"github.com/nicolasacchi/metamusic/internal/redact"
)

// Analyzer is what the hook needs from the Ears service; *ears.Service implements it.
type Analyzer interface {
	AnalyzeTierB(ctx context.Context, trackID string) (ears.TierBOutcome, error)
}

// TierBHook is a jobs.TakeHook: after a take commits, a render that needed the ears capability has its stored analysis.json
// analysed. A failure is logged and left to the sweep (C337.2); it never fails the take.
type TierBHook struct {
	Ears Analyzer
}

var _ jobs.TakeHook = TierBHook{}

// AfterTakeTx does nothing: the analysis runs after the commit, so a Tier B problem can never roll a take back.
func (TierBHook) AfterTakeTx(context.Context, *sql.Tx, jobs.TakeInfo) error { return nil }

// AfterTakeCommit analyses the take's document when its job needed ears.
func (h TierBHook) AfterTakeCommit(ctx context.Context, t jobs.TakeInfo) {
	if h.Ears == nil || !slices.Contains(t.Needs, jobspec.CapEars) {
		return
	}
	_, err := h.Ears.AnalyzeTierB(ctx, t.TrackID)
	switch {
	case err == nil:
	case errors.Is(err, ears.ErrNoTierBDocument):
		slog.Warn("tier B: the render needed ears but delivered no analysis document", "track", t.TrackID, "job", t.JobID)
	default:
		slog.Warn("tier B: analysis failed; the sweep will retry", "track", t.TrackID, "job", t.JobID, "error", redact.String(err.Error()))
	}
}
