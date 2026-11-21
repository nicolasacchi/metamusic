package generation

import (
	"context"
	"database/sql"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// recheckMandate is create step 4b (C368.4): when the estimate was quoted under a mandate, the mandate must still cover the batch
// as seen through the create transaction (a revoke, an expiry or another batch may have committed since). A refusal rolls the
// transaction back, so the token is not consumed.
func (s *Service) recheckMandate(ctx context.Context, tx *sql.Tx, p auth.Principal, est estimateRow) error {
	if est.MandateID == "" {
		return nil
	}
	if s.mandates == nil {
		return mandatesUnavailable()
	}
	mt, tierB := mandateTakesOf(est.Items)
	return s.mandates.Recheck(ctx, tx, p, est.MandateID, est.Engine, mt, money.MicroUSD(est.Ceiling), tierB)
}

// mandateNeed is the approval of a mandate batch: none is needed, the mandate stands in for it (C368.5).
func mandateNeed(id string) spend.ApprovalNeed {
	return spend.ApprovalNeed{Reasons: []string{}, Mandate: id}
}
