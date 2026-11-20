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
	// the artifact rules of the kind (C321.5, Revision 4): the contract, the shape of the output, the allowlist of the kind and
	// the artifacts it must report. Only yue2 speaks the kinds.
	wire, werr := wireOf(provider.EngineYuE2, job.Kind, job.Needs, job.SpecJSON)
	if werr != nil || wire.Wire != provider.ContractV2 || out.Contract != provider.ContractV2 {
		return "", "", violation("WORKER_CONTRACT", "output does not carry the contract of this job")
	}
	if code, text := wire.outputProblem(out); code != "" {
		return "", "", violation(code, "%s", text)
	}
	byName := map[string]upload.Received{}
	for _, f := range files {
		byName[f.Artifact] = f
	}
	if _, ok := byName[provider.ResultArtifact]; !ok {
		return "", "", violation("ARTIFACT_MISSING", "%s was not received", provider.ResultArtifact)
	}
	for _, name := range wire.kindRequired(false, nil, out) {
		if _, ok := out.Artifacts[name]; !ok {
			return "", "", violation("ARTIFACT_MISSING", "required artifact %s is not reported", name)
		}
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
		// a plan job has no track to carry the environment: the job row holds it, like an attached stem (C166)
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET env_id = ? WHERE id = ? AND state = 'ingesting' AND lease_owner = ?`+f.And(), //nolint:gosec // G202: fixed C69 predicate
			append([]any{envID, job.ID, f.Owner}, f.Args()...)...); err != nil {
			return fmt.Errorf("jobs: kind env: %w", err)
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
		if out.AudioSeconds != nil {
			if v, err := out.AudioSeconds.Float64(); err == nil && v > 0 {
				detail["audio_s"] = v
			}
		}
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
