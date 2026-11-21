// Package fallback is the app's half of the upload fallback (BULK plan C399): it mints presigned PUT URLs into a private S3-compatible
// bucket next to the primary upload URLs, records every object it hands out, fetches and verifies the objects a worker put there when
// the app was unreachable (Rescue), deletes them once they cannot matter any more, and runs the operator's drill and self-check.
// It is off unless r2.env is present: New is never called without a configured client, and nothing here opens a connection on its own.
// No URL, key or signature is ever stored, logged, evented or returned; errors pass through internal/redact.
package fallback

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/objstore"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

// DefaultAfterS is how long the worker keeps retrying the primary before it switches (MM_FALLBACK_AFTER_S, 60..600).
const DefaultAfterS = 120

// Retention is the longest an object stays in the bucket (operator decision: 7 days); the deletion loop removes older ones whatever
// state their attempt is in, and the bucket's own lifecycle rule is the backstop.
const Retention = 7 * 24 * time.Hour

// Objects is the part of *objstore.Client this package uses.
type Objects interface {
	PresignPut(key string, ttl time.Duration) (string, error)
	Head(ctx context.Context, key string) (size int64, ok bool, err error)
	Get(ctx context.Context, key string, maxBytes int64) (io.ReadCloser, int64, error)
	Put(ctx context.Context, key string, body io.Reader, size int64) error
	Delete(ctx context.Context, key string) error
}

var _ Objects = (*objstore.Client)(nil)

// Options builds a Fallback.
type Options struct {
	Store   *store.Store
	Uploads *upload.Service
	Objects Objects
	Prefix  string // R2_PREFIX, "mm/" by default
	AfterS  int    // 0 = DefaultAfterS
	Clock   clock.Clock
	Log     *slog.Logger
}

// Fallback is the configured upload fallback.
type Fallback struct{ o Options }

// New builds the fallback. Objects, Store and Uploads are required.
func New(o Options) (*Fallback, error) {
	if o.Objects == nil || o.Store == nil || o.Uploads == nil {
		return nil, errors.New("fallback: Objects, Store and Uploads are required")
	}
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.AfterS == 0 {
		o.AfterS = DefaultAfterS
	}
	if o.AfterS < 60 || o.AfterS > 600 {
		return nil, fmt.Errorf("fallback: after_s %d is outside 60..600", o.AfterS)
	}
	return &Fallback{o: o}, nil
}

// key is the object key of an upload: the configured prefix and the upload's UUID, so nothing about the take or the artifact shows.
func (f *Fallback) key(uploadID string) string { return f.o.Prefix + uploadID }

// MintFallback hands out one presigned PUT URL per open upload of the attempt, valid until exp, and returns the worker's switch time.
// Every object is recorded (fallback_objects) before its URL is returned; if recording fails there is no fallback for the attempt and
// no URL leaves this function. The URLs are in memory only.
func (f *Fallback) MintFallback(ctx context.Context, attemptID string, exp time.Time) (urls map[string]string, afterS int, err error) {
	now := f.o.Clock.Now()
	ttl := exp.Sub(now)
	if ttl < objstore.MinTTL {
		return nil, 0, errors.New("fallback: the attempt window is over")
	}
	ttl = min(ttl, objstore.MaxTTL)
	ups, err := f.o.Uploads.AttemptUploads(ctx, attemptID)
	if err != nil {
		return nil, 0, err
	}
	ids := map[string]string{}
	for _, u := range ups {
		if u.State == "open" {
			ids[u.Artifact] = u.ID
		}
	}
	if len(ids) == 0 {
		return nil, 0, errors.New("fallback: the attempt has no open upload")
	}
	tx, err := f.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("fallback: record objects: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rolled back unless committed
	at := store.FormatTime(now)
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `INSERT INTO fallback_objects(upload_id, attempt_id, minted_at) VALUES (?, ?, ?) ON CONFLICT(upload_id) DO NOTHING`, id, attemptID, at); err != nil {
			return nil, 0, fmt.Errorf("fallback: record objects: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, fmt.Errorf("fallback: record objects: %w", err)
	}
	urls = make(map[string]string, len(ids))
	for artifact, id := range ids {
		u, err := f.o.Objects.PresignPut(f.key(id), ttl)
		if err != nil {
			return nil, 0, fmt.Errorf("fallback: presign: %w", err) // the rows stay; the deletion loop treats them like any other
		}
		urls[artifact] = u
	}
	return urls, f.o.AfterS, nil
}

// event appends a job event (never a URL, key or signature in detail).
func (f *Fallback) event(ctx context.Context, jobID, attemptID, name, detail string) error {
	_, err := f.o.Store.W.ExecContext(ctx, `INSERT INTO job_events(job_id, attempt_id, at, event, detail_json) VALUES (?, ?, ?, ?, ?)`,
		jobID, attemptID, store.FormatTime(f.o.Clock.Now()), name, detail)
	return err
}

// Status is the fallback block of get_provider_status: whether it is configured, how many bucket objects are still to be deleted and
// when a fallback receipt last committed.
type Status struct {
	Configured     bool
	ObjectsPending int
	LastUsedAt     *time.Time
}

// ReadStatus reads the status from the database alone (a database with no fallback rows answers zero and nil); configured is the
// caller's knowledge of r2.env. It never touches the network.
func ReadStatus(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, configured bool) (Status, error) {
	st := Status{Configured: configured}
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM fallback_objects WHERE deleted_at IS NULL`).Scan(&st.ObjectsPending); err != nil {
		return Status{}, fmt.Errorf("fallback: status: %w", err)
	}
	var last sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT MAX(received_at) FROM uploads WHERE via = 'fallback'`).Scan(&last); err != nil {
		return Status{}, fmt.Errorf("fallback: status: %w", err)
	}
	if last.Valid {
		t, err := store.ParseTime(last.String)
		if err != nil {
			return Status{}, fmt.Errorf("fallback: status: %w", err)
		}
		st.LastUsedAt = &t
	}
	return st, nil
}
