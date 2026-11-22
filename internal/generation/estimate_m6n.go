package generation

import (
	"context"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/money"
)

// Preview is Estimate without its side effects: the same planning, quote and cap fit, but no token, no estimate row and no
// audit entry (C368.3). With EstimateIn.MandateID set it reports coverage in Estimate.Mandate instead of refusing. The night shift
// prices candidate nights with it.
func (s *Service) Preview(ctx context.Context, p auth.Principal, in EstimateIn) (Estimate, error) {
	return s.estimate(ctx, p, in, true)
}

// coverEstimate asks the mandate whether it covers the planned takes. A preview reports the answer; an estimate refuses an
// uncovered plan (409 MANDATE_UNAVAILABLE) before it stores anything.
func (s *Service) coverEstimate(ctx context.Context, p auth.Principal, in EstimateIn, engineName string, takes []*planTake, ceiling money.MicroUSD, preview bool) (*MandateCoverage, error) {
	if in.MandateID == "" {
		return nil, nil
	}
	plain := make([]planTake, len(takes))
	for i, t := range takes {
		plain[i] = *t
	}
	mt, tierB := mandateTakesOf(plain)
	ok, reasons, err := s.mandates.Cover(ctx, s.o.Store.R, p, in.MandateID, engineName, mt, ceiling, tierB)
	if err != nil {
		return nil, err
	}
	if reasons == nil {
		reasons = []string{}
	}
	if !ok && !preview {
		return nil, mandateUnavailable(in.MandateID, reasons)
	}
	return &MandateCoverage{ID: in.MandateID, Covered: ok, Reasons: reasons}, nil
}
