package jobs

import (
	"context"
	"database/sql"
	"slices"

	"github.com/nicolasacchi/metamusic/internal/jobspec"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

// The G1 seams of the jobs package (foundations plan C323.6-8): a job has a kind, a kind other than generate is ingested by a
// registered KindIngester, and what a finished job teaches rtf_stats depends on what it needed.

// The job kinds (jobs.kind CHECK).
const (
	KindGenerate = jobspec.KindGenerate
	KindAnalyze  = jobspec.KindAnalyze
	KindPlan     = jobspec.KindPlan
)

// CodeKindUnavailable is the ingest_code of a finished analyze or plan job whose kind has no ingester wired: the job fails
// INGEST_FAILED with this detail rather than being completed without its result being stored.
const CodeKindUnavailable = "KIND_UNAVAILABLE"

// KindIngest is what a KindIngester receives: the job (its Kind, Needs and SpecJSON included), the winning attempt, the received
// files, the verified worker output, the fingerprint id of the worker's environment and the lease fence of the transaction.
type KindIngest struct {
	Job     JobRow
	Attempt AttemptRow
	Files   []upload.Received
	Output  provider.Output
	EnvID   string
	Fence   store.LeaseFence
}

// KindIngester stores the result of a job of one kind other than generate. IngestKind runs inside the fenced take transaction (an
// error rolls everything back and the job fails INGEST_FAILED); it returns the track the job produced, if any (a plan job has none),
// and the job outcome ("clean" or "truncated"; empty means clean). AfterKindCommit runs once after the commit and may only warn.
// A track's ingester lives in that track's own package and is registered through internal/app.
type KindIngester interface {
	IngestKind(ctx context.Context, tx *sql.Tx, in KindIngest) (trackID *string, outcome string, err error)
	AfterKindCommit(ctx context.Context, in KindIngest)
}

// KindJobIngester is the optional half of an Ingester that handles jobs whose kind is not generate. IngestBridge implements it
// over BridgeOptions.Kinds; the runner asks for it only for such jobs, so a plain Ingester (tests, a custom one) keeps serving
// generate jobs unchanged and fails the others with KIND_UNAVAILABLE.
type KindJobIngester interface {
	IngestKindJob(ctx context.Context, job JobRow, att AttemptRow, files []upload.Received, out provider.Output) (trackID, outcome string, err error)
}

// isGenerate reports whether a job is rendered and ingested as a take; an unset Kind is generate (JobRow literals of older code).
func isGenerate(kind string) bool { return kind == "" || kind == KindGenerate }

// rtfNeeds are the capabilities a render may have and still teach rtf_stats (G1 Revision 2): the /2 render time covers the render
// only (Tier B time is reported in ears.seconds), and agent and fallback are transport. Anything else (analyze, plan, attach)
// changes what the e2e time contains.
var rtfNeeds = []string{jobspec.CapV2, jobspec.CapEars, jobspec.CapAgent, jobspec.CapFallback}

// LearnsRTF reports whether a finished job of kind with needs may teach the RTF statistics (C323.8): only a generate job whose
// needs are within {v2, ears, agent, fallback}.
func LearnsRTF(kind string, needs []string) bool {
	if !isGenerate(kind) {
		return false
	}
	for _, n := range needs {
		if !slices.Contains(rtfNeeds, n) {
			return false
		}
	}
	return true
}

type multiTakeHook []TakeHook

// MultiTakeHook runs several take hooks as one, in the order given (C323.7). The first AfterTakeTx error stops the chain and is
// returned, so the take rolls back; AfterTakeCommit calls every hook. Nil hooks are skipped; no hook at all yields a hook that
// does nothing.
func MultiTakeHook(hs ...TakeHook) TakeHook {
	var out multiTakeHook
	for _, h := range hs {
		if h != nil {
			out = append(out, h)
		}
	}
	return out
}

func (m multiTakeHook) AfterTakeTx(ctx context.Context, tx *sql.Tx, t TakeInfo) error {
	for _, h := range m {
		if err := h.AfterTakeTx(ctx, tx, t); err != nil {
			return err
		}
	}
	return nil
}

func (m multiTakeHook) AfterTakeCommit(ctx context.Context, t TakeInfo) {
	for _, h := range m {
		h.AfterTakeCommit(ctx, t)
	}
}
