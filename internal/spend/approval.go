package spend

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Approval constants (C72.6).
const (
	ViaHumanCreate = "human_create"
	ViaAPI         = "api"

	ApprovalTTL         = 24 * time.Hour
	ApprovalRequestGap  = 10 * time.Minute
	ReasonHumanAPIPath  = "human_api_path"
	ReasonTakesOverLim  = "takes_over_limit"
	ReasonCostOverLimit = "cost_over_limit"

	// ExpirySystem decides expired approvals.
	ExpirySystem = "system:reconciler"

	// ViaCancel marks an approval closed because its batch was cancelled while awaiting confirmation. The
	// schema has no 'withdrawn' state (and migrations 0008/0009 are reserved), so such an approval and its
	// batch end as 'rejected' and this via tells them apart from a human rejection.
	ViaCancel = "cancel"
)

// Errors of the approval and batch writers.
var (
	// ErrInvalidTransition: the approval or batch is not in a state the move starts from (409).
	ErrInvalidTransition = errors.New("spend: invalid transition")
	// ErrBatchNotPending: the batch was cancelled while it awaited confirmation; its approval was withdrawn
	// and there is nothing left to approve or reject (409 BATCH_NOT_PENDING).
	ErrBatchNotPending = errors.New("spend: batch is not pending")
	// ErrNotBatchOwner: an agent other than the batch's creator (403).
	ErrNotBatchOwner = errors.New("spend: not the batch owner")
)

// ApprovalNeed says whether a create needs a human decision and why.
type ApprovalNeed struct {
	Needed  bool
	Reasons []string
}

// NeedsApproval decides C72.6: via human_create never needs one; any other human path always does;
// an agent needs one when takes or the ceiling exceed its auto-approve limits.
func NeedsApproval(p auth.Principal, via string, takes int, ceiling money.MicroUSD, tok TokenLimits, c Caps) ApprovalNeed {
	n := ApprovalNeed{Reasons: []string{}}
	if p.Kind != auth.KindAgent {
		if via != ViaHumanCreate {
			n.Needed = true
			n.Reasons = append(n.Reasons, ReasonHumanAPIPath)
		}
		return n
	}
	limitTakes, limitMicro := c.AutoApproveTakes, c.AutoApproveMicro
	if tok.AutoApproveTakes != nil {
		limitTakes = *tok.AutoApproveTakes
	}
	if tok.AutoApproveMicro != nil {
		limitMicro = *tok.AutoApproveMicro
	}
	if takes > limitTakes {
		n.Reasons = append(n.Reasons, ReasonTakesOverLim)
	}
	if ceiling > limitMicro {
		n.Reasons = append(n.Reasons, ReasonCostOverLimit)
	}
	n.Needed = len(n.Reasons) > 0
	return n
}

// InsertApproval writes the approval row of a new batch and returns its state: 'pending' (expires in
// 24 h), 'approved' (human_create) or 'auto' (an agent within its limits), the latter two decided now.
func (s *Service) InsertApproval(ctx context.Context, tx *sql.Tx, batchID string, p auth.Principal, via string, need ApprovalNeed) (string, error) {
	now := s.now()
	reasons := need.Reasons
	if reasons == nil {
		reasons = []string{}
	}
	rj, _ := json.Marshal(reasons)
	state, viaCol, by, at := "pending", any(nil), any(nil), any(nil)
	switch {
	case need.Needed:
	case via == ViaHumanCreate && p.Kind != auth.KindAgent:
		state, viaCol, by, at = "approved", ViaHumanCreate, p.Key(), store.FormatTime(now)
	default:
		state, viaCol, by, at = "auto", "auto", p.Key(), store.FormatTime(now)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO approvals(batch_id, state, reasons, via, decided_by, decided_at, created_at, expires_at)
		VALUES (?,?,?,?,?,?,?,?)`, batchID, state, string(rj), viaCol, by, at, store.FormatTime(now), store.FormatTime(now.Add(ApprovalTTL)))
	if err != nil {
		return "", fmt.Errorf("spend: insert approval: %w", err)
	}
	return state, nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Service) approvalState(ctx context.Context, tx *sql.Tx, batchID string) (state string, expires time.Time, err error) {
	var exp string
	err = tx.QueryRowContext(ctx, `SELECT state, expires_at FROM approvals WHERE batch_id = ?`, batchID).Scan(&state, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, ErrNotFound
	}
	if err != nil {
		return "", time.Time{}, err
	}
	expires, err = store.ParseTime(exp)
	return state, expires, err
}

// ApproveBatch approves a pending batch in the caller's transaction: the approval and the batch
// (awaiting_confirmation -> active) move together; the jobs move to queued on the runner's next step.
// changed is false when it was already approved. Rejected, expired and lapsed approvals refuse.
func (s *Service) ApproveBatch(ctx context.Context, tx *sql.Tx, batchID string, by auth.Principal, reason string) (changed bool, err error) {
	now := store.FormatTime(s.now())
	res, err := tx.ExecContext(ctx, `UPDATE approvals SET state='approved', via='web', decided_by=?, decided_at=?, reason=?
		WHERE batch_id=? AND state='pending' AND expires_at > ?`, by.Key(), now, nullStr(reason), batchID, now)
	if err != nil {
		return false, fmt.Errorf("spend: approve: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		if _, err := tx.ExecContext(ctx, `UPDATE batches SET state='active' WHERE id=? AND state='awaiting_confirmation'`, batchID); err != nil {
			return false, fmt.Errorf("spend: approve batch: %w", err)
		}
		return true, nil
	}
	state, _, err := s.approvalState(ctx, tx, batchID)
	if err != nil {
		return false, err
	}
	if state == "approved" || state == "auto" {
		return false, nil
	}
	if s.withdrawn(ctx, tx, batchID) {
		return false, fmt.Errorf("%w: batch was cancelled", ErrBatchNotPending)
	}
	return false, fmt.Errorf("%w: approval is %s", ErrInvalidTransition, state)
}

// RejectBatch rejects a pending batch: approval and batch move to rejected, the whole outstanding is
// released ('rejected') and the batch is reconciled, all in the caller's transaction. A batch that is
// already rejected is left alone (changed false).
func (s *Service) RejectBatch(ctx context.Context, tx *sql.Tx, batchID string, by auth.Principal, reason string) (changed bool, err error) {
	nowT := s.now()
	now := store.FormatTime(nowT)
	res, err := tx.ExecContext(ctx, `UPDATE approvals SET state='rejected', via='web', decided_by=?, decided_at=?, reason=?
		WHERE batch_id=? AND state='pending'`, by.Key(), now, nullStr(reason), batchID)
	if err != nil {
		return false, fmt.Errorf("spend: reject: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		state, _, err := s.approvalState(ctx, tx, batchID)
		if err != nil {
			return false, err
		}
		if s.withdrawn(ctx, tx, batchID) {
			return false, fmt.Errorf("%w: batch was cancelled", ErrBatchNotPending)
		}
		if state == "rejected" {
			return false, nil
		}
		return false, fmt.Errorf("%w: approval is %s", ErrInvalidTransition, state)
	}
	return true, s.closeUnstarted(ctx, tx, nil, batchID, "rejected", now)
}

// withdrawn reports whether the batch's approval was closed by a cancel.
func (s *Service) withdrawn(ctx context.Context, tx *sql.Tx, batchID string) bool {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM approvals WHERE batch_id = ? AND state = 'rejected' AND via = ?`, batchID, ViaCancel).Scan(&n)
	return err == nil && n > 0
}

// WithdrawBatch closes a batch that is still awaiting confirmation because it was cancelled: the pending
// approval and the batch both end as 'rejected' (approval via 'cancel', decided by who), the whole
// outstanding is released ('rejected') and reconciled_at is set, all in the caller's transaction. The
// jobs are the caller's (a cancel intent; the runner ends them). A batch that is not awaiting confirmation
// is left alone (changed false), so the call is idempotent. lease is nil on a request path and the
// reconciler's fence for the stuck-row rule.
func (s *Service) WithdrawBatch(ctx context.Context, tx *sql.Tx, batchID, by, reason string, lease *store.LeaseFence) (changed bool, err error) {
	now := store.FormatTime(s.now())
	q, args := `UPDATE approvals SET state='rejected', via=?, decided_by=?, decided_at=?, reason=? WHERE batch_id=? AND state='pending'`,
		[]any{ViaCancel, by, now, nullStr(reason), batchID}
	if lease != nil {
		q += lease.And() //nolint:gosec // G202: fixed C69 predicate, values are bound
		args = append(args, lease.Args()...)
	}
	res, err := tx.ExecContext(ctx, q, args...)
	if err != nil {
		return false, fmt.Errorf("spend: withdraw: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	return true, s.closeUnstarted(ctx, tx, lease, batchID, "rejected", now)
}

// StuckWithdrawn lists batches still awaiting confirmation with a pending approval although every one of
// their jobs is cancelled (a cancel that predates WithdrawBatch), oldest first.
func (s *Service) StuckWithdrawn(ctx context.Context, q Querier, limit int) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT b.id FROM batches b JOIN approvals a ON a.batch_id = b.id
		WHERE b.state = 'awaiting_confirmation' AND a.state = 'pending'
		AND EXISTS (SELECT 1 FROM jobs j WHERE j.batch_id = b.id)
		AND NOT EXISTS (SELECT 1 FROM jobs j WHERE j.batch_id = b.id AND j.state <> 'cancelled')
		ORDER BY b.created_at LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ExpireApproval expires a lapsed pending approval (reconciler, lease predicate): approval expired
// (via expiry, decided by the reconciler), batch expired, 'expired' release, reconciled_at set.
func (s *Service) ExpireApproval(ctx context.Context, tx *sql.Tx, batchID string, lease store.LeaseFence) error {
	now := store.FormatTime(s.now())
	//nolint:gosec // G202: lease.And() is the fixed C69 predicate; every value is bound
	res, err := tx.ExecContext(ctx, `UPDATE approvals SET state='expired', via='expiry', decided_by=?, decided_at=?
		WHERE batch_id=? AND state='pending' AND expires_at <= ?`+lease.And(), append([]any{ExpirySystem, now, batchID, now}, lease.Args()...)...)
	n, err := lease.Fenced(res, err)
	if err != nil {
		return fmt.Errorf("spend: expire: %w", err)
	}
	if n == 0 {
		if err := lease.Confirm(ctx, tx, 0); err != nil {
			return err
		}
		return fmt.Errorf("%w: approval is not a lapsed pending one", ErrInvalidTransition)
	}
	return s.closeUnstarted(ctx, tx, &lease, batchID, "expired", now)
}

// closeUnstarted ends a batch that never reached the provider: state, finished_at, reconciled_at, and the
// release of its outstanding. reason is both the new batch state and the release reason.
func (s *Service) closeUnstarted(ctx context.Context, tx *sql.Tx, lease *store.LeaseFence, batchID, reason, now string) error {
	var owner string
	if err := tx.QueryRowContext(ctx, `SELECT principal_id FROM batches WHERE id = ?`, batchID).Scan(&owner); err != nil {
		return err
	}
	out, err := BatchOutstanding(ctx, tx, batchID)
	if err != nil {
		return err
	}
	if out > 0 {
		if err := s.ledger.append(ctx, tx, lease, []Entry{{Kind: KindRelease, MicroUSD: out, BatchID: &batchID, PrincipalID: owner, Reason: reason}}); err != nil {
			return err
		}
	}
	q, args := `UPDATE batches SET state=?, finished_at=?, reconciled_at=? WHERE id=? AND state='awaiting_confirmation'`, []any{reason, now, now, batchID}
	if lease != nil {
		q += lease.And() //nolint:gosec // G202: fixed C69 predicate, values are bound
		args = append(args, lease.Args()...)
	}
	res, err := tx.ExecContext(ctx, q, args...)
	n, err := (store.LeaseFence{}).Fenced(res, err)
	if err != nil {
		return fmt.Errorf("spend: close batch: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: batch is not awaiting confirmation", ErrInvalidTransition)
	}
	return nil
}

// PendingExpired lists the batches whose pending approval lapsed (reconciler input), oldest first.
func (s *Service) PendingExpired(ctx context.Context, q Querier, limit int) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT batch_id FROM approvals WHERE state='pending' AND expires_at <= ? ORDER BY expires_at LIMIT ?`,
		store.FormatTime(s.now()), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ApprovalRequest is the outcome of request_approval.
type ApprovalRequest struct {
	// Notified: the caller should push one notification now (the count was bumped).
	Notified bool
	State    string
	// NextRequestAllowedAt is set when a request was suppressed by the 10 minute gap.
	NextRequestAllowedAt time.Time
	CeilingMicro         money.MicroUSD
	Takes                int
	ExpiresAt            time.Time
}

// RequestApproval records an approval request by the batch's creator (agent) and rate-limits the push
// to one per 10 minutes. The push itself is the caller's, after commit.
func (s *Service) RequestApproval(ctx context.Context, tx *sql.Tx, batchID string, by auth.Principal) (ApprovalRequest, error) {
	var r ApprovalRequest
	var owner, exp string
	var ceiling int64
	err := tx.QueryRowContext(ctx, `SELECT b.principal_id, b.ceiling_micro_usd, b.n_takes, a.state, a.expires_at
		FROM batches b JOIN approvals a ON a.batch_id = b.id WHERE b.id = ?`, batchID).Scan(&owner, &ceiling, &r.Takes, &r.State, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if owner != by.Key() {
		return r, ErrNotBatchOwner
	}
	r.CeilingMicro = money.MicroUSD(ceiling)
	if r.ExpiresAt, err = store.ParseTime(exp); err != nil {
		return r, err
	}
	if r.State != "pending" {
		return r, nil
	}
	now := s.now()
	res, err := tx.ExecContext(ctx, `UPDATE approvals SET requested_count = requested_count + 1, last_requested_at = ?
		WHERE batch_id = ? AND state = 'pending' AND (last_requested_at IS NULL OR last_requested_at <= ?)`,
		store.FormatTime(now), batchID, store.FormatTime(now.Add(-ApprovalRequestGap)))
	if err != nil {
		return r, fmt.Errorf("spend: request approval: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		r.Notified = true
		return r, nil
	}
	var last string
	if err := tx.QueryRowContext(ctx, `SELECT last_requested_at FROM approvals WHERE batch_id = ?`, batchID).Scan(&last); err != nil {
		return r, err
	}
	lt, err := store.ParseTime(last)
	if err != nil {
		return r, err
	}
	r.NextRequestAllowedAt = lt.Add(ApprovalRequestGap)
	return r, nil
}
