package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

// IngestKindJob implements KindJobIngester (C323.6): the result of an analyze or plan job is stored by the ingester registered
// for its kind, inside one transaction under the runner's lease fence, together with the job's event, the environment
// fingerprint and the consumption of the uploads. A kind without an ingester fails with KIND_UNAVAILABLE and writes nothing.
func (b *IngestBridge) IngestKindJob(ctx context.Context, job JobRow, att AttemptRow, files []upload.Received, out provider.Output) (string, string, error) {
	owner := leaseOwnerFrom(ctx)
	if owner == "" {
		return "", "", errors.New("jobs: ingest without a runner lease")
	}
	ki, ok := b.o.Kinds[job.Kind]
	if !ok || ki == nil || isGenerate(job.Kind) {
		return "", "", &IngestFailedError{Code: CodeKindUnavailable, Err: fmt.Errorf("jobs: no ingester for kind %q", job.Kind)}
	}
	if b.o.Store == nil || b.o.Uploads == nil {
		return "", "", errors.New("jobs: ingest bridge is not wired")
	}
	if out.Status != "ok" || out.TakeID != job.ID {
		return "", "", violation("WORKER_CONTRACT", "output does not belong to this job")
	}
	byName := map[string]upload.Received{}
	for _, f := range files {
		byName[f.Artifact] = f
	}
	if _, ok := byName[provider.ResultArtifact]; !ok {
		return "", "", violation("ARTIFACT_MISSING", "%s was not received", provider.ResultArtifact)
	}
	for name, rep := range out.Artifacts {
		u, ok := byName[name]
		switch {
		case !ok:
			return "", "", violation("ARTIFACT_MISSING", "reported artifact %s was not received", name)
		case rep.SHA256 != u.SHA256 || rep.Bytes != u.Bytes:
			return "", "", violation("ARTIFACT_MISMATCH", "artifact %s differs from its report", name)
		}
	}
	envID, err := b.envFor(ctx, att, out)
	if err != nil {
		return "", "", err
	}
	in := KindIngest{Job: job, Attempt: att, Files: files, Output: out, EnvID: envID}
	var track *string
	outcome := ""
	err = b.fencedTx(ctx, owner, job, func(tx *sql.Tx, f store.LeaseFence) error {
		in.Fence = f
		if _, err := b.putEnv(ctx, tx, f, job, att, out, envID); err != nil {
			return err
		}
		var err error
		if track, outcome, err = ki.IngestKind(ctx, tx, in); err != nil {
			return err
		}
		switch outcome {
		case "":
			outcome = "clean"
		case "clean", "truncated":
		default:
			return fmt.Errorf("jobs: ingester of %s returned outcome %q", job.Kind, outcome)
		}
		detail := map[string]any{"kind": job.Kind, "outcome": outcome}
		if track != nil {
			detail["track_id"] = *track
		}
		if err := insertEvent(ctx, tx, f, Event{JobID: job.ID, AttemptID: att.ID, Name: EvKindIngested, Detail: detail}); err != nil {
			return fmt.Errorf("jobs: kind event: %w", err)
		}
		ids := make([]string, 0, len(files))
		for _, u := range files {
			ids = append(ids, u.UploadID)
		}
		return b.o.Uploads.Consume(ctx, tx, ids)
	})
	if err != nil {
		var ife *IngestFailedError
		var cv *ContractViolation
		if errors.As(err, &ife) || errors.As(err, &cv) || errors.Is(err, store.ErrLeaseLost) || errors.Is(err, errStale) {
			return "", "", err
		}
		b.o.Log.Warn("kind ingest commit", "job", job.ID, "kind", job.Kind, "err", err.Error())
		return "", "", &IngestFailedError{Code: "DB_INSERT", Err: err}
	}
	ids := make([]string, 0, len(files))
	for _, u := range files {
		ids = append(ids, u.UploadID)
	}
	if err := b.o.Uploads.RemoveConsumedFiles(ctx, ids); err != nil {
		b.o.Log.Warn("remove consumed uploads", "job", job.ID)
	}
	ki.AfterKindCommit(ctx, in)
	if track != nil {
		return *track, outcome, nil
	}
	return "", outcome, nil
}
