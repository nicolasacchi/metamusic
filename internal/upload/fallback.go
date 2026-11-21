package upload

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// The receiving half of the upload fallback (C399.5). The app's own route serves the worker's PUT; when the worker put an artifact into
// the fallback bucket instead, internal/upload/fallback fetches and verifies the object and hands the file to ReceiveFallback, which
// commits it exactly as the route does: file 0400 under incoming/uploads/, the row `received` with via = 'fallback', the upload event,
// and a runner wake.

var (
	// ErrFallbackClosed means the attempt may not deliver any more (the job is terminal, the attempt rejected, another attempt
	// already won, or the upload row is closed). Nothing was stored.
	ErrFallbackClosed = errors.New("upload: the attempt is closed")
	// ErrFallbackReceived means the upload was already received (through either route). Nothing was stored.
	ErrFallbackReceived = errors.New("upload: already received")
	// ErrFallbackBusy means another PUT or fallback receipt of the same upload is in flight and did not leave in time.
	ErrFallbackBusy = errors.New("upload: another receipt of this upload is in progress")
)

// OpenUpload is what the fallback fetcher needs to know about one upload of an attempt.
type OpenUpload struct {
	ID, Artifact, State string
	MaxBytes            int64
}

// AttemptUploads lists the uploads of an attempt (any state), ordered by artifact name.
func (s *Service) AttemptUploads(ctx context.Context, attemptID string) ([]OpenUpload, error) {
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT id, artifact, state, max_bytes FROM uploads WHERE attempt_id = ? ORDER BY artifact`, attemptID)
	if err != nil {
		return nil, fmt.Errorf("upload: list attempt uploads: %w", err)
	}
	defer rows.Close()
	var out []OpenUpload
	for rows.Next() {
		var u OpenUpload
		if err := rows.Scan(&u.ID, &u.Artifact, &u.State, &u.MaxBytes); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// BeginFallback claims the in-flight slot of an open upload and creates its part file (0600) for the caller to fill. The caller must
// end with ReceiveFallback or AbortFallback, which give the slot back. ErrFallbackReceived and ErrFallbackClosed are not faults.
func (s *Service) BeginFallback(ctx context.Context, uploadID string) (*os.File, error) {
	if _, err := s.fallbackRow(ctx, uploadID); err != nil {
		return nil, err
	}
	if !s.lock(ctx, uploadID) {
		return nil, ErrFallbackBusy
	}
	// the state may have changed while we waited for the slot
	if _, err := s.fallbackRow(ctx, uploadID); err != nil {
		s.unlock(uploadID)
		return nil, err
	}
	if err := s.o.Data.MkdirAll(uploadsDir, 0o700); err != nil {
		s.unlock(uploadID)
		return nil, fmt.Errorf("upload: %w", err)
	}
	part := uploadPath(uploadID) + ".part"
	_ = s.o.Data.Remove(part) // left by a crashed process; we hold the slot
	f, err := s.o.Data.OpenFile(part, osCreateExcl, partMode)
	if err != nil {
		s.unlock(uploadID)
		return nil, fmt.Errorf("upload: %w", err)
	}
	return f, nil
}

// fallbackRow reads the upload with its job and attempt state and applies the closed rules of the route.
func (s *Service) fallbackRow(ctx context.Context, uploadID string) (uploadRow, error) {
	var row uploadRow
	err := s.o.Store.R.QueryRowContext(ctx, `SELECT u.id, u.job_id, u.attempt_id, u.artifact, u.state, u.max_bytes, u.exp, u.sha256, u.bytes, j.state, a.state
		FROM uploads u JOIN jobs j ON j.id = u.job_id JOIN job_attempts a ON a.id = u.attempt_id WHERE u.id = ?`, uploadID).
		Scan(&row.ID, &row.JobID, &row.AttemptID, &row.Artifact, &row.State, &row.MaxBytes, &row.Exp, &row.SHA, &row.Bytes, &row.JobState, &row.AttemptState)
	if errors.Is(err, sql.ErrNoRows) {
		return row, fmt.Errorf("upload: unknown upload %s", uploadID)
	}
	if err != nil {
		return row, fmt.Errorf("upload: %w", err)
	}
	var n int
	if err := s.o.Store.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_events WHERE job_id = ? AND event = 'result_received' AND attempt_id IS NOT NULL AND attempt_id <> ?`,
		row.JobID, row.AttemptID).Scan(&n); err != nil {
		return row, fmt.Errorf("upload: %w", err)
	}
	switch {
	case row.State == "received" || row.State == "consumed":
		return row, ErrFallbackReceived
	case row.State == "closed" || closedReason(row.JobState, row.AttemptState, n > 0, false):
		return row, ErrFallbackClosed
	}
	return row, nil
}

// AbortFallback abandons a fallback receipt begun with BeginFallback.
func (s *Service) AbortFallback(uploadID string, part *os.File) {
	_ = part.Close()
	_ = s.o.Data.Remove(uploadPath(uploadID) + ".part")
	s.unlock(uploadID)
}

// ReceiveFallback commits a verified fallback object: part holds exactly n bytes whose sha256 is sha (the caller verified both
// against the worker's output and, for the bytes, while streaming). It always gives the slot back and removes the part file on failure.
func (s *Service) ReceiveFallback(ctx context.Context, uploadID string, part *os.File, sha string, n int64) error {
	defer s.unlock(uploadID)
	partPath := uploadPath(uploadID) + ".part"
	ok := false
	defer func() {
		if !ok {
			_ = part.Close()
			_ = s.o.Data.Remove(partPath)
		}
	}()
	row, err := s.fallbackRow(ctx, uploadID)
	if err != nil {
		return err
	}
	if info, err := part.Stat(); err != nil || info.Size() != n || n > row.MaxBytes {
		return fmt.Errorf("upload: the fallback part of %s does not hold %d bytes within max_bytes", uploadID, n)
	}
	if err := part.Sync(); err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	if err := part.Close(); err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	if err := s.o.Data.Chmod(partPath, finalMode); err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	final := uploadPath(uploadID)
	if err := s.o.Data.Rename(partPath, final); err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	ok = true // the part file is the final file now
	switch err := s.commit(ctx, row, sha, n, viaFallback, map[string]any{"via": viaFallback}); {
	case errors.Is(err, errClosed):
		_ = s.o.Data.Remove(final)
		return ErrFallbackClosed
	case err != nil:
		_ = s.o.Data.Remove(final)
		return fmt.Errorf("upload: commit fallback receipt: %w", err)
	}
	if row.Artifact == "mm-result.json" {
		s.wake()
	}
	return nil
}

// drilling reports whether an operator's outage drill covers the attempt right now (upload_drills, C399.8).
func (s *Service) drilling(ctx context.Context, attemptID string) (bool, error) {
	var until string
	err := s.o.Store.R.QueryRowContext(ctx, `SELECT until FROM upload_drills WHERE attempt_id = ?`, attemptID).Scan(&until)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return until > store.FormatTime(s.o.Clock.Now()), nil
}
