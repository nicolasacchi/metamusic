package generation

import (
	"context"
	"database/sql"
	"net/http"
	"slices"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/jobspec"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

// ReasonWorkerOutdated is the GENERATION_DISABLED reason of a batch whose needs the backend's worker image has not been opened for
// (C322.5). The string is an API value (details.reason).
const ReasonWorkerOutdated = "worker_outdated"

// batchNeeds is the union of the needs of every take, sorted; empty for a batch of plain renders.
func batchNeeds(takes []*planTake) []string {
	var out []string
	for _, t := range takes {
		for _, n := range t.Needs {
			if !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
	}
	slices.Sort(out)
	return out
}

// requireCaps refuses (503 GENERATION_DISABLED, reason worker_outdated, details {backend, reason, missing}) a batch that needs a
// worker capability the backend's image has not been opened for. A batch with no needs never reads the gate, so every plain /1 render
// behaves exactly as before. q is the transaction create runs in, or the reader pool at estimate.
func (s *Service) requireCaps(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, st provider.State, needs []string) error {
	if len(needs) == 0 {
		return nil
	}
	open, err := provider.WorkerCaps(ctx, q, st.Backend, st.Quote, s.o.Now())
	if err != nil {
		return err
	}
	missing := provider.MissingCaps(needs, open)
	if len(missing) == 0 {
		return nil
	}
	return perr(http.StatusServiceUnavailable, "GENERATION_DISABLED", "generation is not enabled: "+ReasonWorkerOutdated,
		map[string]any{"reason": ReasonWorkerOutdated, "backend": st.Backend, "missing": missing})
}

// itemNeeds is batchNeeds over the stored items of an estimate.
func itemNeeds(items []planTake) []string {
	ptrs := make([]*planTake, len(items))
	for i := range items {
		ptrs[i] = &items[i]
	}
	return batchNeeds(ptrs)
}

// v2WireCanaryID reports whether a request id is that of the v2-wire canary case (mm-canary-<day>-v2-wire-<n>, C322.8).
func v2WireCanaryID(id string) bool {
	return strings.HasPrefix(id, "mm-canary-") && strings.Contains(id, "-v2-wire-")
}

// markV2Canary gives the plain yue2 renders of the v2-wire canary case the need [v2], so the runner sends them as mm.worker/2
// (the only way a plain render is sent as /2 before a track adds options). Any other take is untouched; a canary id on a take that is
// already /2 or not a render keeps its own needs.
func (p *planner) markV2Canary() {
	if p.ace {
		return
	}
	for _, t := range p.takes {
		if len(t.Needs) == 0 && (t.Kind == "" || t.Kind == jobspec.KindGenerate) && v2WireCanaryID(t.RequestID) {
			t.Needs = []string{jobspec.CapV2}
		}
	}
}
