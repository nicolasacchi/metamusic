package fallback

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Drill bounds (C399.8): the primary upload route answers 503 for the attempt for this long, at least 30 s and at most 600 s.
const (
	MinDrillS = 30
	MaxDrillS = 600
)

// ErrNoFallbackObjects is the refusal to drill an attempt that was sent no fallback URLs: the drill would only break the upload.
var ErrNoFallbackObjects = errors.New("fallback: the attempt has no fallback objects")

// CreateDrill is RecordDrill over the fallback's own store and clock.
func (f *Fallback) CreateDrill(ctx context.Context, attemptID string, seconds int, reason, by string) (time.Time, error) {
	return RecordDrill(ctx, f.o.Store, f.o.Clock, attemptID, seconds, reason, by)
}

// RecordDrill writes the attempt's drill row and its audit row in one transaction: for the next seconds the primary route answers 503
// UPLOAD_DRILL for that attempt. One drill per attempt (the table is append-only); it never touches the network, so the CLI can run it
// without a configured bucket.
func RecordDrill(ctx context.Context, st *store.Store, clk clock.Clock, attemptID string, seconds int, reason, by string) (until time.Time, err error) {
	if seconds < MinDrillS || seconds > MaxDrillS {
		return time.Time{}, fmt.Errorf("fallback: a drill lasts %d to %d seconds", MinDrillS, MaxDrillS)
	}
	if n := utf8.RuneCountInString(reason); n < 1 || n > 500 {
		return time.Time{}, errors.New("fallback: a reason of 1 to 500 characters is required")
	}
	if by == "" {
		return time.Time{}, errors.New("fallback: a drill needs the name of who runs it")
	}
	var n int
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM fallback_objects WHERE attempt_id = ? AND deleted_at IS NULL`, attemptID).Scan(&n); err != nil {
		return time.Time{}, fmt.Errorf("fallback: %w", err)
	}
	if n == 0 {
		return time.Time{}, ErrNoFallbackObjects
	}
	now := clk.Now()
	until = now.Add(time.Duration(seconds) * time.Second)
	tx, err := st.W.BeginTx(ctx, nil)
	if err != nil {
		return time.Time{}, fmt.Errorf("fallback: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rolled back unless committed
	if _, err := tx.ExecContext(ctx, `INSERT INTO upload_drills(attempt_id, until, reason, created_by, created_at) VALUES (?, ?, ?, ?, ?)`,
		attemptID, store.FormatTime(until), reason, by, store.FormatTime(now)); err != nil {
		return time.Time{}, fmt.Errorf("fallback: record drill: %w", err)
	}
	e, err := audit.SystemEntry(OpDrill, now, map[string]any{"attempt_id": attemptID, "seconds": seconds, "reason": reason}, 0, 0, false)
	if err != nil {
		return time.Time{}, err
	}
	if err := audit.InsertTx(ctx, tx, e); err != nil {
		return time.Time{}, fmt.Errorf("fallback: audit: %w", err)
	}
	return until, tx.Commit()
}
