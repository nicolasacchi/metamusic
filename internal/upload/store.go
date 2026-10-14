package upload

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// uploadsDir is the directory of received and in-progress uploads under the data root (C68).
const uploadsDir = "incoming/uploads"

// Mode bits of uploaded files: 0600 while streaming, 0400 once received.
const (
	partMode  os.FileMode = 0o600
	finalMode os.FileMode = 0o400
)

func uploadPath(id string) string { return uploadsDir + "/" + id }

// openUploadSQL is the upload insert up to its lease predicate.
const openUploadSQL = `INSERT INTO uploads(id, job_id, attempt_id, artifact, max_bytes, exp, state, created_at)
	SELECT ?, ?, ?, ?, ?, ?, 'open', ? WHERE `

// OpenAttempt inserts the attempt's upload rows in state 'open', inside the claim transaction. Every
// statement carries the lease predicate; a statement that affects no row means the lease is gone.
func (s *Service) OpenAttempt(ctx context.Context, tx *sql.Tx, job JobRef, attemptID string, artifacts map[string]int64, exp int64, lease store.LeaseFence) error {
	names := make([]string, 0, len(artifacts))
	for n := range artifacts {
		names = append(names, n)
	}
	sort.Strings(names)
	now := store.FormatTime(s.o.Clock.Now())
	for _, name := range names {
		limit, ok := provider.ArtifactMaxBytes(name)
		if !ok {
			return fmt.Errorf("upload: %q is not an allowlisted artifact", name)
		}
		if artifacts[name] < 1 || artifacts[name] > limit {
			return fmt.Errorf("upload: max_bytes for %q out of range", name)
		}
		args := []any{s.newID(), job.JobID, attemptID, name, artifacts[name], exp, now}
		args = append(args, lease.Args()...)
		q := openUploadSQL + lease.Where() //nolint:gosec // G202: fixed C69 predicate, values are bound
		res, err := tx.ExecContext(ctx, q, args...)
		n, err := lease.Fenced(res, err)
		if err != nil {
			return fmt.Errorf("upload: open attempt: %w", err)
		}
		if n == 0 {
			return store.ErrLeaseLost
		}
	}
	return nil
}

// Mint builds the capability URLs of an attempt in memory only; they are never persisted. The upload
// rows must have been committed by OpenAttempt.
func (s *Service) Mint(job JobRef, attemptID string, exp int64) (uploads map[string]provider.UploadTarget, startURL string, err error) {
	rows, err := s.o.Store.R.QueryContext(context.Background(), `SELECT id, artifact, max_bytes FROM uploads WHERE attempt_id = ? AND state = 'open'`, attemptID)
	if err != nil {
		return nil, "", fmt.Errorf("upload: mint: %w", err)
	}
	defer rows.Close()
	uploads = map[string]provider.UploadTarget{}
	for rows.Next() {
		var id, artifact string
		var maxBytes int64
		if err := rows.Scan(&id, &artifact, &maxBytes); err != nil {
			return nil, "", err
		}
		uploads[artifact] = provider.UploadTarget{URL: s.o.Signer.URL(s.o.PublicURL, id, job.JobID, artifact, maxBytes, exp), MaxBytes: maxBytes}
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(uploads) == 0 {
		return nil, "", errors.New("upload: mint: no open uploads for the attempt")
	}
	return uploads, s.o.Signer.StartURL(s.o.PublicURL, attemptID, job.JobID, exp), nil
}

// Received lists the attempt's received (not yet consumed) uploads, ordered by artifact name.
func (s *Service) Received(ctx context.Context, q Querier, attemptID string) ([]Received, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, artifact, sha256, bytes FROM uploads WHERE attempt_id = ? AND state = 'received' ORDER BY artifact`, attemptID)
	if err != nil {
		return nil, fmt.Errorf("upload: list received: %w", err)
	}
	defer rows.Close()
	var out []Received
	for rows.Next() {
		var r Received
		if err := rows.Scan(&r.UploadID, &r.Artifact, &r.SHA256, &r.Bytes); err != nil {
			return nil, err
		}
		r.Path = uploadPath(r.UploadID)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Consume marks received uploads consumed, in the take transaction (whose statements carry the lease
// predicate).
func (s *Service) Consume(ctx context.Context, tx *sql.Tx, uploadIDs []string) error {
	for _, id := range uploadIDs {
		res, err := tx.ExecContext(ctx, `UPDATE uploads SET state = 'consumed' WHERE id = ? AND state = 'received'`, id)
		if err != nil {
			return fmt.Errorf("upload: consume: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("upload: consume: %s is not a received upload", id)
		}
	}
	return nil
}

// CloseAttempt closes the open uploads of an attempt (a late PUT then answers 410), in the caller's
// transaction (whose statements carry the lease predicate).
func (s *Service) CloseAttempt(ctx context.Context, tx *sql.Tx, attemptID string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE uploads SET state = 'closed' WHERE attempt_id = ? AND state = 'open'`, attemptID); err != nil {
		return fmt.Errorf("upload: close attempt: %w", err)
	}
	return nil
}

// RemoveConsumedFiles deletes consumed upload files after the take commits; a file that is already gone
// is not an error.
func (s *Service) RemoveConsumedFiles(ctx context.Context, uploadIDs []string) error {
	var first error
	for _, id := range uploadIDs {
		if err := s.o.Data.Remove(uploadPath(id)); err != nil && !errors.Is(err, fs.ErrNotExist) && first == nil {
			first = fmt.Errorf("upload: remove %s: %w", id, err)
		}
	}
	return first
}

// SweepHold is how long received files of terminal jobs stay for inspection (C68).
const SweepHold = 24 * time.Hour

// SweepFiles removes the files of uploads whose job reached a terminal state at least 24 h ago, and
// stale .part files older than that. removed counts the files deleted.
func (s *Service) SweepFiles(ctx context.Context, now time.Time) (removed int, err error) {
	cutoff := store.FormatTime(now.Add(-SweepHold))
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT u.id FROM uploads u JOIN jobs j ON j.id = u.job_id
		WHERE u.state IN ('received','consumed') AND j.finished_at IS NOT NULL AND j.finished_at <= ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("upload: sweep: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	for _, id := range ids {
		switch err := s.o.Data.Remove(uploadPath(id)); {
		case err == nil:
			removed++
		case !errors.Is(err, fs.ErrNotExist):
			return removed, fmt.Errorf("upload: sweep %s: %w", id, err)
		}
	}
	dir, err := s.o.Data.OpenRoot(uploadsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return removed, nil
	}
	if err != nil {
		return removed, err
	}
	defer func() { _ = dir.Close() }()
	d, err := dir.Open(".")
	if err != nil {
		return removed, err
	}
	defer func() { _ = d.Close() }()
	ents, err := d.ReadDir(-1)
	if err != nil {
		return removed, err
	}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".part") || s.inFlight(strings.TrimSuffix(e.Name(), ".part")) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(now.Add(-SweepHold)) {
			continue
		}
		if err := dir.Remove(path.Base(e.Name())); err == nil {
			removed++
		}
	}
	return removed, nil
}

// osCreateExcl creates the part file exclusively for writing (C74.2 step 7).
const osCreateExcl = os.O_WRONLY | os.O_CREATE | os.O_EXCL

// ReadFile reads a received upload of at most maxBytes (small control artifacts such as mm-result.json).
func (s *Service) ReadFile(uploadID string, maxBytes int64) ([]byte, error) {
	f, err := s.o.Data.Open(uploadPath(uploadID))
	if err != nil {
		return nil, fmt.Errorf("upload: open %s: %w", uploadID, err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("upload: read %s: %w", uploadID, err)
	}
	if int64(len(b)) > maxBytes {
		return nil, fmt.Errorf("upload: %s exceeds %d bytes", uploadID, maxBytes)
	}
	return b, nil
}
