package jobs

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrReconcileRequired says a restored database may not be armed until one billing reconciliation has
// covered the time from the snapshot to now (C76.7, D-M3-31).
var ErrReconcileRequired = errors.New("jobs: arming needs a billing reconciliation that covers the restore")

// RestoreGate is what the arming rule of C76.7 sees for one backend.
type RestoreGate struct {
	// Restored: the database was restored and not yet armed since.
	Restored bool
	// RestoredAt is when the restore disarmed the backend.
	RestoredAt time.Time
	// Covered: MAX(billing_windows.window_end) >= restored_at.
	Covered bool
}

// ArmGate reads the restore state of backend through x.
func ArmGate(ctx context.Context, x Querier, backend string) (RestoreGate, error) {
	var restored, maxEnd sql.NullString
	err := x.QueryRowContext(ctx, `SELECT (SELECT restored_at FROM provider_state WHERE backend = ?), (SELECT MAX(window_end) FROM billing_windows)`, backend).Scan(&restored, &maxEnd)
	if err != nil {
		return RestoreGate{}, err
	}
	var g RestoreGate
	if !restored.Valid {
		return g, nil
	}
	g.Restored, g.RestoredAt = true, mustT(restored.String)
	g.Covered = maxEnd.Valid && !mustT(maxEnd.String).Before(g.RestoredAt)
	return g, nil
}

// CheckArm is the arming rule: a restored backend arms only after a covering reconciliation. With billing
// `none` no window ever covers it, so only the explicit acknowledgment of `runpod arm --after-restore`
// (UpdateIn.AfterRestore) can arm.
func CheckArm(ctx context.Context, x Querier, backend string, afterRestore bool) error {
	g, err := ArmGate(ctx, x, backend)
	if err != nil {
		return err
	}
	if g.Restored && !g.Covered && !afterRestore {
		return ErrReconcileRequired
	}
	return nil
}
