package fallback

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/nicolasacchi/metamusic/internal/objstore"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

// maxResultBytes bounds the GET of mm-result.json (the same bound the runner reads it with).
const maxResultBytes = 64 << 10

// Job event names and details written here (the jobs package lists them in its allowlist).
const evMismatch = "fallback_mismatch"

// Rescue completes an attempt from the bucket: it looks for the attempt's mm-result.json object and, when it is there, fetches every
// artifact the output lists that the app has not received, verifies size and sha256 against the output's digests, commits each through
// upload.Service.ReceiveFallback and the result last. found reports that the result was received by this call. An attempt with no
// fallback object, an absent result, a result for another take or attempt, a missing artifact or any mismatch is not an error: nothing
// is committed that failed verification, the upload stays open (a mismatch writes one fallback_mismatch event per distinct cause) and
// found is false. The error return is for faults worth retrying (network, database).
func (f *Fallback) Rescue(ctx context.Context, attemptID string) (found bool, err error) {
	at, ok, err := f.loadAttempt(ctx, attemptID)
	if err != nil || !ok {
		return false, err
	}
	var result upload.OpenUpload
	have := map[string]upload.OpenUpload{}
	ups, err := f.o.Uploads.AttemptUploads(ctx, attemptID)
	if err != nil {
		return false, err
	}
	for _, u := range ups {
		have[u.Artifact] = u
		if u.Artifact == provider.ResultArtifact {
			result = u
		}
	}
	if result.ID == "" || result.State != "open" || !at.minted[result.ID] {
		return false, nil // no object was minted for the result, or it was already received
	}
	raw, ok, err := f.fetchSmall(ctx, result.ID)
	if err != nil || !ok {
		return false, err
	}
	out, err := provider.DecodeOutput(raw)
	if err != nil || out.Contract != provider.ContractV2 || out.TakeID != at.jobID || out.AttemptID != attemptID {
		return false, f.mismatch(ctx, at, "result", "mm-result.json does not belong to this attempt")
	}
	names := make([]string, 0, len(out.Artifacts))
	for n := range out.Artifacts {
		if n != provider.ResultArtifact {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	// first decide what is missing: an artifact that is neither received nor in the bucket means the worker has not finished
	type todo struct {
		u  upload.OpenUpload
		dg provider.ArtifactDigest
	}
	var fetch []todo
	for _, n := range names {
		u, listed := have[n]
		if !listed {
			return false, f.mismatch(ctx, at, n, "the output lists an artifact this attempt has no upload for")
		}
		if u.State != "open" {
			continue // received through the route (the transfer check compares it with the output)
		}
		if !at.minted[u.ID] {
			return false, nil
		}
		_, present, err := f.o.Objects.Head(ctx, f.key(u.ID))
		if err != nil {
			return false, fmt.Errorf("fallback: %s", redact.Error(err))
		}
		if !present {
			return false, nil
		}
		fetch = append(fetch, todo{u, out.Artifacts[n]})
	}
	for _, t := range fetch {
		switch err := f.receiveObject(ctx, t.u, &t.dg, nil); {
		case errors.Is(err, errMismatch):
			return false, f.mismatch(ctx, at, t.u.Artifact, "the bucket object differs from the output's size or sha256")
		case errors.Is(err, upload.ErrFallbackClosed), errors.Is(err, upload.ErrFallbackReceived):
			return false, nil
		case err != nil:
			return false, err
		}
	}
	// the result last: its bytes were read and decoded above; commit exactly those bytes
	if err := f.receiveObject(ctx, result, nil, raw); err != nil {
		if errors.Is(err, upload.ErrFallbackClosed) || errors.Is(err, upload.ErrFallbackReceived) {
			return false, nil
		}
		return false, err
	}
	if _, err := f.o.Store.W.ExecContext(ctx, `UPDATE fallback_objects SET fetched_at = ? WHERE attempt_id = ? AND fetched_at IS NULL
		AND upload_id IN (SELECT id FROM uploads WHERE attempt_id = ? AND via = 'fallback')`, store.FormatTime(f.o.Clock.Now()), attemptID, attemptID); err != nil {
		return true, fmt.Errorf("fallback: record fetch: %w", err)
	}
	return true, nil
}

var errMismatch = errors.New("fallback: object does not match its digest")

// receiveObject streams one bucket object into the upload's part file, verifying it against dg (an artifact) or against the bytes
// already read (the result), and commits it. A bound of max_bytes applies to the stream either way.
func (f *Fallback) receiveObject(ctx context.Context, u upload.OpenUpload, dg *provider.ArtifactDigest, known []byte) error {
	part, err := f.o.Uploads.BeginFallback(ctx, u.ID)
	if err != nil {
		return err
	}
	var src io.Reader
	if known != nil {
		src = bytesReader(known)
	} else {
		body, _, err := f.o.Objects.Get(ctx, f.key(u.ID), u.MaxBytes)
		if err != nil {
			f.o.Uploads.AbortFallback(u.ID, part)
			if errors.Is(err, objstore.ErrTooLarge) {
				return errMismatch
			}
			return fmt.Errorf("fallback: %s", redact.Error(err))
		}
		defer func() { _ = body.Close() }()
		src = body
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(part, h), src)
	if err != nil {
		f.o.Uploads.AbortFallback(u.ID, part)
		if errors.Is(err, objstore.ErrTooLarge) {
			return errMismatch
		}
		return fmt.Errorf("fallback: %s", redact.Error(err))
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if (dg != nil && (n != dg.Bytes || sum != dg.SHA256)) || n < 1 || n > u.MaxBytes {
		f.o.Uploads.AbortFallback(u.ID, part)
		return errMismatch
	}
	return f.o.Uploads.ReceiveFallback(ctx, u.ID, part, sum, n)
}

// fetchSmall reads a small object (the result) completely; ok is false when it is not in the bucket.
func (f *Fallback) fetchSmall(ctx context.Context, uploadID string) ([]byte, bool, error) {
	size, ok, err := f.o.Objects.Head(ctx, f.key(uploadID))
	if err != nil {
		return nil, false, fmt.Errorf("fallback: %s", redact.Error(err))
	}
	if !ok {
		return nil, false, nil
	}
	if size < 1 || size > maxResultBytes {
		return nil, false, nil // not a result the worker could have written within its limit; the cause is visible as no progress
	}
	body, _, err := f.o.Objects.Get(ctx, f.key(uploadID), maxResultBytes)
	if err != nil {
		if errors.Is(err, objstore.ErrNotFound) || errors.Is(err, objstore.ErrTooLarge) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("fallback: %s", redact.Error(err))
	}
	defer func() { _ = body.Close() }()
	raw, err := io.ReadAll(body)
	if err != nil {
		return nil, false, fmt.Errorf("fallback: %s", redact.Error(err))
	}
	return raw, true, nil
}

// attemptInfo is what Rescue needs to know about an attempt.
type attemptInfo struct {
	jobID, attemptID string
	minted           map[string]bool // upload ids that have a fallback_objects row
}

// loadAttempt reads the attempt's job and its minted objects; ok is false when the attempt has no fallback object at all.
func (f *Fallback) loadAttempt(ctx context.Context, attemptID string) (attemptInfo, bool, error) {
	at := attemptInfo{attemptID: attemptID, minted: map[string]bool{}}
	rows, err := f.o.Store.R.QueryContext(ctx, `SELECT o.upload_id, a.job_id FROM fallback_objects o JOIN job_attempts a ON a.id = o.attempt_id WHERE o.attempt_id = ?`, attemptID)
	if err != nil {
		return at, false, fmt.Errorf("fallback: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id, &at.jobID); err != nil {
			return at, false, err
		}
		at.minted[id] = true
	}
	if err := rows.Err(); err != nil {
		return at, false, err
	}
	return at, len(at.minted) > 0, nil
}

// mismatch records a fallback_mismatch event, once per distinct cause, and answers nil: the upload stays open.
func (f *Fallback) mismatch(ctx context.Context, at attemptInfo, artifact, reason string) error {
	detail, _ := json.Marshal(map[string]any{"artifact": artifact, "reason": reason})
	var n int
	if err := f.o.Store.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event = ? AND detail_json = ?`, at.jobID, evMismatch, string(detail)).Scan(&n); err != nil {
		return fmt.Errorf("fallback: %w", err)
	}
	if n > 0 {
		return nil
	}
	if err := f.event(ctx, at.jobID, at.attemptID, evMismatch, string(detail)); err != nil {
		return fmt.Errorf("fallback: %w", err)
	}
	return nil
}

// LiveAttempts lists the attempts the periodic rescue should look at: attempts with a minted object whose upload is still open, on a
// job that has not ended, and whose attempt is not rejected.
func (f *Fallback) LiveAttempts(ctx context.Context) ([]string, error) {
	rows, err := f.o.Store.R.QueryContext(ctx, `SELECT DISTINCT o.attempt_id FROM fallback_objects o
		JOIN uploads u ON u.id = o.upload_id JOIN job_attempts a ON a.id = o.attempt_id JOIN jobs j ON j.id = a.job_id
		WHERE o.deleted_at IS NULL AND u.state = 'open' AND a.state <> 'rejected'
		  AND j.state NOT IN ('completed','failed','cancelled','expired') ORDER BY o.attempt_id`)
	if err != nil {
		return nil, fmt.Errorf("fallback: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
