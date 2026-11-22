package generation

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/jobspec"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// The M6N mandate seam (C368.2). generation never imports mandates: the app injects the implementation through UseMandates. A
// mandate stands in for the Owner's per-batch approval only within its scope and remaining budget; it never raises a spend cap, so
// the caps are checked first and exactly as before. A Service on which UseMandates was never called refuses every EstimateIn that
// names a mandate, and an estimate without a mandate behaves byte-identically to the one before this seam existed.

// MandateTake is one planned take as a mandate sees it; spend declares it so that mandates implements MandateCover without importing generation.
type MandateTake = spend.MandateTake

// MandateCover is implemented by mandates.Service (it resolves the lineage roots and the Owner's stars of the parents itself).
type MandateCover interface {
	// Cover reports whether the mandate covers the plan for principal p and every reason it does not. It writes nothing.
	Cover(ctx context.Context, q lineage.Querier, p auth.Principal, mandateID, engine string, takes []MandateTake, ceiling money.MicroUSD, tierB bool) (bool, []string, error)
	// Recheck is Cover inside the create transaction: usage is read through tx, so a revoke or another batch that committed since the
	// estimate is seen. A refusal is a *problem.Error 409 MANDATE_UNAVAILABLE.
	Recheck(ctx context.Context, tx *sql.Tx, p auth.Principal, mandateID, engine string, takes []MandateTake, ceiling money.MicroUSD, tierB bool) error
}

// UseMandates wires the mandate seam; nil (the default) refuses an estimate that names a mandate with 503 UNAVAILABLE.
func (s *Service) UseMandates(c MandateCover) { s.mandates = c }

// MandateCoverage is what Preview says of EstimateIn.MandateID (Go-only: the REST and MCP estimate input has no such field).
type MandateCoverage struct {
	ID      string
	Covered bool
	Reasons []string
}

func mandatesUnavailable() error {
	return perr(http.StatusServiceUnavailable, "UNAVAILABLE", "the mandate service is not available", map[string]any{"component": "mandates"})
}

// mandateUnavailable is the 409 of a plan the mandate does not cover.
func mandateUnavailable(id string, reasons []string) error {
	if reasons == nil {
		reasons = []string{}
	}
	return perr(http.StatusConflict, "MANDATE_UNAVAILABLE", "the mandate does not cover this batch", map[string]any{"mandate_id": id, "reasons": reasons})
}

// mandateTakesOf maps planned takes to what a mandate sees, and says whether any take has a listening check (Tier B).
func mandateTakesOf(takes []planTake) (out []MandateTake, tierB bool) {
	for _, t := range takes {
		out = append(out, MandateTake{Mode: t.Mode, ParentTrackID: t.ParentTrackID})
		for _, n := range t.Needs {
			if n == jobspec.CapEars {
				tierB = true
			}
		}
	}
	return out, tierB
}
