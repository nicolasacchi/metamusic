package pods

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// Store helpers of the lifecycle loop (C396.3): the observations and one-time marks it writes. Like the helpers of sql.go they take a DB (a
// *sql.Tx composes several in one transaction) and never begin one.

// SetCreateSent records when the create request went out (the lifetime of a pod is measured from it, D-BULK-7).
func SetCreateSent(ctx context.Context, db DB, podID string, at time.Time) error {
	_, err := db.ExecContext(ctx, `UPDATE pods SET create_sent_at = ?, updated_at = ? WHERE id = ?`, store.FormatTime(at), store.FormatTime(at), podID)
	return err
}

// SetProviderRef records the RunPod id of the pod (set once; a different second value is refused by the database).
func SetProviderRef(ctx context.Context, db DB, podID, ref string, at time.Time) error {
	_, err := db.ExecContext(ctx, `UPDATE pods SET provider_ref = ?, updated_at = ? WHERE id = ?`, ref, store.FormatTime(at), podID)
	return err
}

// MarkRunningSeen records the first observation of status RUNNING (once).
func MarkRunningSeen(ctx context.Context, db DB, podID string, at time.Time) error {
	_, err := db.ExecContext(ctx, `UPDATE pods SET running_seen_at = ?, updated_at = ? WHERE id = ? AND running_seen_at IS NULL`, store.FormatTime(at), store.FormatTime(at), podID)
	return err
}

// MarkGoneSeen records the first observation that the pod is gone (once).
func MarkGoneSeen(ctx context.Context, db DB, podID string, at time.Time) error {
	_, err := db.ExecContext(ctx, `UPDATE pods SET gone_seen_at = ?, updated_at = ? WHERE id = ? AND gone_seen_at IS NULL`, store.FormatTime(at), store.FormatTime(at), podID)
	return err
}

// SetRestarts records the number of container restarts the loop has acted on (0..2).
func SetRestarts(ctx context.Context, db DB, podID string, n int, at time.Time) error {
	_, err := db.ExecContext(ctx, `UPDATE pods SET restarts = ?, updated_at = ? WHERE id = ?`, n, store.FormatTime(at), podID)
	return err
}

// SetReportedPrice records the hourly rate RunPod reported for the pod (diagnostic; the frozen price is the batch price).
func SetReportedPrice(ctx context.Context, db DB, podID string, micro int64, at time.Time) error {
	_, err := db.ExecContext(ctx, `UPDATE pods SET reported_pph_micro_usd = ?, updated_at = ? WHERE id = ?`, micro, store.FormatTime(at), podID)
	return err
}

// SetErrorCode writes the error code of a pod row (the supply-ended codes of C396.3 land here after the row terminated).
func SetErrorCode(ctx context.Context, db DB, podID, code string, at time.Time) error {
	_, err := db.ExecContext(ctx, `UPDATE pods SET error_code = ?, updated_at = ? WHERE id = ?`, code, store.FormatTime(at), podID)
	return err
}

// BootWeightsReady reports whether the pod's last boot report said the weights are loaded.
func BootWeightsReady(ctx context.Context, db DB, podID string) (bool, error) {
	var ok sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT json_extract(boot_json, '$.weights_ready') FROM pods WHERE id = ?`, podID).Scan(&ok)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return err == nil && ok.Valid && ok.Int64 == 1, err
}

// MarkBatchSettled sets settled on every terminated, unsettled pod of the batch and reports how many it changed: the one that gets
// a non-zero answer settles the batch's pod excess, so the settlement happens once (C393.5).
func MarkBatchSettled(ctx context.Context, db DB, batchID string, at time.Time) (int64, error) {
	res, err := db.ExecContext(ctx, `UPDATE pods SET settled = 1, updated_at = ? WHERE batch_id = ? AND state = 'terminated' AND settled = 0`, store.FormatTime(at), batchID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PodsOfBatch lists every pod row of a batch (terminated ones too), oldest first.
func PodsOfBatch(ctx context.Context, db DB, batchID string) ([]Row, error) {
	return queryRows(ctx, db, `SELECT `+podCols+` FROM pods WHERE batch_id = ? ORDER BY n, gpu_try, created_at`, batchID)
}

// AllLiveRows lists every pod that is not terminated, oldest first.
func AllLiveRows(ctx context.Context, db DB) ([]Row, error) {
	return queryRows(ctx, db, `SELECT `+podCols+` FROM pods WHERE state <> 'terminated' ORDER BY created_at, n, gpu_try`)
}

func queryRows(ctx context.Context, db DB, q string, args ...any) ([]Row, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		p, err := scanPod(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// EventCount counts the events of a pod with the given name.
func EventCount(ctx context.Context, db DB, podID, event string) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pod_events WHERE pod_id = ? AND event = ?`, podID, event).Scan(&n)
	return n, err
}

// UnhandedAssignments lists the open, unhanded assignments of a batch.
func UnhandedAssignments(ctx context.Context, db DB, batchID string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT attempt_id FROM pod_assignments WHERE batch_id = ? AND pod_id IS NULL AND done_at IS NULL ORDER BY queued_at, attempt_id`, batchID)
	if err != nil {
		return nil, err
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
