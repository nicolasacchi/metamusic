package nightshift

import (
	"strings"

	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/mandates"
)

// TierBOption is the night's listening-check choice for generation.EstimateIn.TierB (C376.3, Ears C339.3). It is nil (auto) when
// the Owner's setting is on and the mandate allows Tier B: the estimate then adds the listening check where the routed backend's
// ears capability is open and says tier_b_unavailable per take where it is closed. In every other case it is an explicit false
// (the mandate never sees a Tier B take it did not allow). The night never sends true, so a closed capability never refuses a night.
func TierBOption(listeningChecks string, scope mandates.Scope) *bool {
	if listeningChecks == ListeningOn && scope.TierB {
		return nil
	}
	off := false
	return &off
}

// tierBWarn is a per-take Tier B warning kept in plan_json (code only: no lyrics, no text).
type tierBWarn struct {
	TakeIndex int    `json:"take_index"`
	Code      string `json:"code"`
}

// tierBDoc fills the quote record with what the estimate decided about listening checks: whether any take has one, which takes, and
// the Tier B warnings (tier_b_unavailable, tier_b_no_lyrics, ...).
func tierBDoc(q *quoteDoc, est generation.Estimate) {
	q.TierB, q.TierBTakes, q.TierBWarnings = false, nil, nil
	for _, it := range est.Items {
		for _, t := range it.Takes {
			if t.TierB {
				q.TierB = true
				q.TierBTakes = append(q.TierBTakes, t.TakeIndex)
			}
		}
	}
	for _, w := range est.Warnings {
		if strings.HasPrefix(w.Code, "tier_b_") {
			q.TierBWarnings = append(q.TierBWarnings, tierBWarn{TakeIndex: w.TakeIndex, Code: w.Code})
		}
	}
}
