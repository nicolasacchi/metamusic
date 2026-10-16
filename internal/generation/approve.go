package generation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/idem"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

func notOwner() error {
	return perr(http.StatusForbidden, "NOT_BATCH_OWNER", "only the batch's creator may do this", nil)
}

// spendErr maps the spend package's sentinel errors onto the problem model.
func (s *Service) spendErr(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, batchID, to string, err error) error {
	switch {
	case errors.Is(err, spend.ErrNotFound):
		return notFound()
	case errors.Is(err, spend.ErrNotBatchOwner):
		return notOwner()
	case errors.Is(err, spend.ErrBatchNotPending):
		return perr(http.StatusConflict, "BATCH_NOT_PENDING", "the batch was cancelled while awaiting confirmation; there is nothing to "+strings.TrimSuffix(to, "d")+" any more", map[string]any{"to": to})
	case errors.Is(err, spend.ErrInvalidTransition):
		var from string
		if e := q.QueryRowContext(ctx, `SELECT state FROM approvals WHERE batch_id = ?`, batchID).Scan(&from); e != nil {
			from = "unknown"
		}
		return perr(http.StatusConflict, "INVALID_TRANSITION", "the batch's approval is "+from+" and cannot become "+to, map[string]any{"from": from, "to": to})
	}
	return err
}

// Approval reads the approval of a batch.
func (s *Service) Approval(ctx context.Context, p auth.Principal, batchID string) (ApprovalView, error) {
	if err := s.ready(); err != nil {
		return ApprovalView{}, err
	}
	return loadApproval(ctx, s.o.Store.R, batchID)
}

// RequestApproval pushes the approval request for a pending batch (creator only; one push per 10 minutes).
func (s *Service) RequestApproval(ctx context.Context, p auth.Principal, batchID string) (ApprovalView, error) {
	if err := s.ready(); err != nil {
		return ApprovalView{}, err
	}
	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return ApprovalView{}, err
	}
	req, err := s.o.Spend.RequestApproval(ctx, tx, batchID, p)
	if err != nil {
		_ = s.o.Spend.RollbackTx(tx)
		return ApprovalView{}, s.spendErr(ctx, s.o.Store.R, batchID, "approved", err)
	}
	if err := s.o.Spend.CommitTx(ctx, tx); err != nil {
		return ApprovalView{}, err
	}
	view, err := loadApproval(ctx, s.o.Store.R, batchID)
	if err != nil {
		return view, err
	}
	if !req.NextRequestAllowedAt.IsZero() {
		t := req.NextRequestAllowedAt
		view.NextRequestAllowedAt = &t
	}
	if !req.Notified {
		return view, nil
	}
	view.Notified = true
	var name string
	_ = s.o.Store.R.QueryRowContext(ctx, `SELECT principal_name FROM batches WHERE id = ?`, batchID).Scan(&name)
	if s.o.Notify != nil {
		// a failed push is not an error of the request: the approval request itself is recorded
		_ = s.o.Notify.Notify(ctx, notify.Message{
			Title:    "Approval needed",
			Body:     fmt.Sprintf("Agent %s asks to spend up to %s for %d takes", name, req.CeilingMicro.DisplayCeil(), req.Takes),
			Priority: 4,
			Actions:  []notify.Action{{Action: "view", Label: "Review", URL: s.o.PublicURL + "/human/confirm/" + batchID}},
		})
	}
	return view, nil
}

// decide runs approve or reject in one transaction with the in-transaction audit row.
func (s *Service) decide(ctx context.Context, p auth.Principal, batchID, to string, ledgerCost bool,
	do func(tx *sql.Tx) (bool, error)) (Created, error) {
	if err := s.ready(); err != nil {
		return Created{}, err
	}
	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return Created{}, err
	}
	fail := func(err error) (Created, error) {
		_ = s.o.Spend.RollbackTx(tx)
		idem.MarksFrom(ctx).Reset()
		return Created{}, err
	}
	changed, err := do(tx)
	if err != nil {
		return fail(s.spendErr(ctx, tx, batchID, to, err))
	}
	c, err := loadCreated(ctx, tx, batchID)
	if err != nil {
		return fail(err)
	}
	if e, ok := audit.PendingFrom(ctx); ok {
		e.Status = http.StatusOK
		if ledgerCost {
			e.CostMicroUSD = c.Ceiling
		}
		if e.Duration = s.o.Now().Sub(e.At); e.Duration < 0 {
			e.Duration = 0
		}
		if err := audit.InsertTx(ctx, tx, e); err != nil {
			return fail(fmt.Errorf("generation: audit: %w", err))
		}
	}
	if err := s.o.Spend.CommitTx(ctx, tx); err != nil {
		return fail(err)
	}
	if changed && to == "approved" && s.o.Wake != nil {
		s.o.Wake()
	}
	return c, nil
}

// Approve approves a pending batch (human-only): the approval and the batch move together; the runner
// queues the jobs on its next step and is woken. Approving an approved batch changes nothing.
func (s *Service) Approve(ctx context.Context, p auth.Principal, batchID, reason string) (Created, error) {
	return s.decide(ctx, p, batchID, "approved", true, func(tx *sql.Tx) (bool, error) {
		return s.o.Spend.ApproveBatch(ctx, tx, batchID, p, reason)
	})
}

// Reject rejects a pending batch, releasing its whole reservation in the same transaction.
func (s *Service) Reject(ctx context.Context, p auth.Principal, batchID, reason string) (Created, error) {
	return s.decide(ctx, p, batchID, "rejected", false, func(tx *sql.Tx) (bool, error) {
		return s.o.Spend.RejectBatch(ctx, tx, batchID, p, reason)
	})
}
