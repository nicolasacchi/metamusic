package generation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func parseT(s string) time.Time {
	t, err := store.ParseTime(s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func optTime(n sql.NullString) *time.Time {
	if !n.Valid {
		return nil
	}
	t := parseT(n.String)
	return &t
}

func nullPtr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	v := n.String
	return &v
}

// loadApproval reads the approval row of a batch through q.
func loadApproval(ctx context.Context, q lineage.Querier, batchID string) (ApprovalView, error) {
	v := ApprovalView{BatchID: batchID}
	var reasons string
	var via, by, reason, decided, lastReq sql.NullString
	var exp string
	err := q.QueryRowContext(ctx, `SELECT state, reasons, via, decided_by, reason, decided_at, last_requested_at, expires_at FROM approvals WHERE batch_id = ?`, batchID).
		Scan(&v.State, &reasons, &via, &by, &reason, &decided, &lastReq, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return v, notFound()
	}
	if err != nil {
		return v, fmt.Errorf("generation: approval: %w", err)
	}
	v.Reasons = []string{}
	_ = json.Unmarshal([]byte(reasons), &v.Reasons)
	v.Via, v.DecidedBy, v.Reason = via.String, by.String, reason.String
	if by.Valid {
		v.Decider = actorFor(ctx, q, by.String)
	}
	v.DecidedAt, v.LastRequestedAt = optTime(decided), optTime(lastReq)
	v.ExpiresAt = parseT(exp)
	if v.LastRequestedAt != nil {
		t := v.LastRequestedAt.Add(spend.ApprovalRequestGap)
		v.NextRequestAllowedAt = &t
	}
	return v, nil
}

// loadCreated reads a whole batch (row, approval, jobs, money) through q; inside a transaction it sees the
// transaction's own writes.
func loadCreated(ctx context.Context, q lineage.Querier, batchID string) (Created, error) {
	c := Created{BatchID: batchID, JobIDs: []string{}}
	var rationale, finished, reconciled sql.NullString
	var created, pkey, pkind, pname string
	var expected, maxM, ceiling int64
	err := q.QueryRowContext(ctx, `SELECT state, via, estimate_id, n_takes, rationale, expected_micro_usd, max_micro_usd, ceiling_micro_usd, created_at,
		finished_at, reconciled_at, principal_id, principal_kind, principal_name FROM batches WHERE id = ?`, batchID).
		Scan(&c.State, &c.Via, &c.EstimateID, &c.Takes, &rationale, &expected, &maxM, &ceiling, &created, &finished, &reconciled, &pkey, &pkind, &pname)
	if errors.Is(err, sql.ErrNoRows) {
		return c, notFound()
	}
	if err != nil {
		return c, fmt.Errorf("generation: batch: %w", err)
	}
	c.Rationale, c.FinishedAt = nullPtr(rationale), optTime(finished)
	c.Expected, c.Max, c.Ceiling = money.MicroUSD(expected), money.MicroUSD(maxM), money.MicroUSD(ceiling)
	c.CreatedAt, c.Reconciled = parseT(created), reconciled.Valid
	c.CreatedBy = Actor{Key: pkey, Kind: pkind, Name: pname}
	if c.Approval, err = loadApproval(ctx, q, batchID); err != nil {
		return c, err
	}
	c.ApprovalState = c.Approval.State
	var res, set, rel int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN kind = 'reserve' THEN micro_usd END), 0), COALESCE(SUM(CASE WHEN kind = 'settle' THEN micro_usd END), 0),
		COALESCE(SUM(CASE WHEN kind = 'release' THEN micro_usd END), 0) FROM ledger WHERE batch_id = ?`, batchID).Scan(&res, &set, &rel); err != nil {
		return c, fmt.Errorf("generation: batch ledger: %w", err)
	}
	c.Reserved, c.Settled = money.MicroUSD(res), money.MicroUSD(set)
	c.Outstanding = money.MicroUSD(max(res-set-rel, 0))
	var overhead sql.NullInt64
	if err := q.QueryRowContext(ctx, `SELECT SUM(micro_usd) FROM ledger WHERE batch_id = ? AND kind = 'settle' AND reason = 'overhead'`, batchID).Scan(&overhead); err != nil {
		return c, err
	}
	rows, err := q.QueryContext(ctx, `SELECT j.id, j.take_index, j.state, j.outcome, j.error_code, j.title, r.request_id, r.seed, j.operator, j.source_track_id, j.draft_id,
		j.track_id, j.created_at, j.updated_at, j.expected_micro_usd,
		(SELECT SUM(l.micro_usd) FROM ledger l WHERE l.job_id = j.id AND l.kind = 'settle' AND l.reason IN ('execution','execution_unknown'))
		FROM jobs j JOIN recipes r ON r.id = j.recipe_id WHERE j.batch_id = ? ORDER BY j.take_index`, batchID)
	if err != nil {
		return c, fmt.Errorf("generation: batch jobs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		j := JobView{BatchID: batchID}
		var outcome, ecode, parent, draft, track sql.NullString
		var cre, upd string
		var exp int64
		var settled sql.NullInt64
		if err := rows.Scan(&j.ID, &j.TakeIndex, &j.State, &outcome, &ecode, &j.Title, &j.RequestID, &j.Seed, &j.Operator, &parent, &draft, &track, &cre, &upd, &exp, &settled); err != nil {
			return c, err
		}
		j.Outcome, j.ErrorCode, j.ParentTrackID, j.DraftID, j.TrackID = nullPtr(outcome), nullPtr(ecode), nullPtr(parent), nullPtr(draft), nullPtr(track)
		j.CreatedAt, j.UpdatedAt, j.Expected = parseT(cre), parseT(upd), money.MicroUSD(exp)
		j.Basis = "estimate"
		if settled.Valid {
			d, basis := lineage.DisplayCost(exp, int64(c.Takes), settled, overhead, c.Reconciled)
			j.Display, j.Basis = &d, basis
		}
		c.Jobs = append(c.Jobs, j)
		c.JobIDs = append(c.JobIDs, j.ID)
		switch j.State {
		case "awaiting_confirmation":
			c.Counts.Awaiting++
		case "queued":
			c.Counts.Queued++
		case "completed":
			if j.Outcome != nil && *j.Outcome == "truncated" {
				c.Counts.Truncated++
			} else {
				c.Counts.Completed++
			}
		case "failed":
			c.Counts.Failed++
		case "cancelled":
			c.Counts.Cancelled++
		case "expired":
			c.Counts.Expired++
		default:
			c.Counts.Running++
		}
	}
	return c, rows.Err()
}
