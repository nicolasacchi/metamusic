package earsjobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// EvAnalysisDuplicate is the job event of an analyze job whose take already had a document when its own arrived (a race loser,
// C340.5): the document is kept as analysis-<n>.json and not analysed. Its detail keys are track_id and artifact.
const EvAnalysisDuplicate = "analysis_duplicate"

// analysisArtifact is the name of the raw Tier B document (provider.AnalysisArtifact; this package imports no provider, C327.1).
const analysisArtifact = "analysis.json"

// Attacher is what the analyze ingester needs from the Ears service; *ears.Service implements it.
type Attacher interface {
	AttachAnalysis(ctx context.Context, tx *sql.Tx, trackID, srcPath, wantSHA string, wantBytes int64) (ears.AttachedAnalysis, error)
	AnalyzeTierB(ctx context.Context, trackID string) (ears.TierBOutcome, error)
}

// AnalyzeIngester is the jobs.KindIngester of kind analyze (C340.5): it attaches the Tier B document of a finished analyze job to the
// EXISTING take it listened to. It never writes the take's audio, tags or recipe (masters are frozen, recipes immutable), creates no
// track and no mutation, and only the analyses rows and measured sections of AnalyzeTierB follow, after the commit.
type AnalyzeIngester struct {
	ears Attacher

	mu   sync.Mutex
	dups map[string]bool // job id -> its document was a duplicate: AfterKindCommit does not analyse it
}

var _ jobs.KindIngester = (*AnalyzeIngester)(nil)

// NewAnalyzeIngester builds the ingester over the Ears service.
func NewAnalyzeIngester(e Attacher) *AnalyzeIngester {
	return &AnalyzeIngester{ears: e, dups: map[string]bool{}}
}

// IngestKind runs inside the take transaction: it verifies analysis.json against the received file and the strict mm.ears-b/1 parser
// (a document that fails is WORKER_CONTRACT: paid, valueless, visible; one whose status is error is stored, D-EARS-10), attaches it
// to the job's source take, and returns that take's id with outcome clean.
func (a *AnalyzeIngester) IngestKind(ctx context.Context, tx *sql.Tx, in jobs.KindIngest) (*string, string, error) {
	if in.Job.Kind != jobs.KindAnalyze || in.Job.SourceTrackID == nil || *in.Job.SourceTrackID == "" {
		return nil, "", errors.New("earsjobs: an analyze job names the take it analyses")
	}
	track := *in.Job.SourceTrackID
	a.mu.Lock()
	delete(a.dups, in.Job.ID) // each ingest attempt sets the flag afresh: a rolled-back attempt must not leak it into a retry
	a.mu.Unlock()
	var doc *struct {
		path, sha string
		bytes     int64
	}
	for _, f := range in.Files {
		if f.Artifact == analysisArtifact {
			doc = &struct {
				path, sha string
				bytes     int64
			}{f.Path, f.SHA256, f.Bytes}
		}
	}
	if doc == nil {
		return nil, "", &jobs.ContractViolation{Code: "ARTIFACT_MISSING", Text: analysisArtifact + " was not received"}
	}
	rep, ok := in.Output.Artifacts[analysisArtifact]
	if !ok || rep.SHA256 != doc.sha || rep.Bytes != doc.bytes {
		return nil, "", &jobs.ContractViolation{Code: "ARTIFACT_MISMATCH", Text: analysisArtifact + " differs from its report"}
	}
	att, err := a.ears.AttachAnalysis(ctx, tx, track, doc.path, doc.sha, doc.bytes)
	switch {
	case errors.Is(err, tierb.ErrContract):
		return nil, "", &jobs.ContractViolation{Code: "WORKER_CONTRACT", Text: analysisArtifact + " is not a valid mm.ears-b/1 document"}
	case errors.Is(err, ears.ErrAttachMismatch):
		return nil, "", &jobs.ContractViolation{Code: "ARTIFACT_MISMATCH", Text: analysisArtifact + " differs from what was received"}
	case err != nil:
		return nil, "", fmt.Errorf("earsjobs: attach analysis: %w", err)
	}
	if att.Duplicate {
		if err := recordDuplicate(ctx, tx, in, track, att.Name); err != nil {
			return nil, "", err
		}
		a.mu.Lock()
		a.dups[in.Job.ID] = true
		a.mu.Unlock()
	}
	return &track, "clean", nil
}

// recordDuplicate appends the analysis_duplicate event under the lease fence, like every job event.
func recordDuplicate(ctx context.Context, tx *sql.Tx, in jobs.KindIngest, track, artifact string) error {
	var attempt any
	if in.Attempt.ID != "" {
		attempt = in.Attempt.ID
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO job_events(job_id, attempt_id, at, event, detail_json) SELECT ?, ?, ?, ?, json_object('track_id', ?, 'artifact', ?) WHERE `+in.Fence.Where(), //nolint:gosec // G202: fixed lease predicate, values are bound
		append([]any{in.Job.ID, attempt, store.FormatTime(in.Fence.Now), EvAnalysisDuplicate, track, artifact}, in.Fence.Args()...)...)
	n, err := in.Fence.Fenced(res, err)
	if err != nil {
		return fmt.Errorf("earsjobs: duplicate event: %w", err)
	}
	if n == 0 {
		return store.ErrLeaseLost
	}
	return nil
}

// AfterKindCommit analyses the stored document after the commit (CPU only: measured sections and the analyses rows). A failure is
// logged and left to the sweep; a duplicate document is not analysed.
func (a *AnalyzeIngester) AfterKindCommit(ctx context.Context, in jobs.KindIngest) {
	a.mu.Lock()
	dup := a.dups[in.Job.ID]
	delete(a.dups, in.Job.ID)
	a.mu.Unlock()
	if dup || in.Job.SourceTrackID == nil {
		return
	}
	if _, err := a.ears.AnalyzeTierB(ctx, *in.Job.SourceTrackID); err != nil {
		slog.Warn("tier B: analysis of an analyze job failed; the sweep will retry", "track", *in.Job.SourceTrackID, "job", in.Job.ID, "error", redact.String(err.Error()))
	}
}
