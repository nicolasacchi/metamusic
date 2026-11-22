// Package criticbench measures how well each critic (a Tier A metric, an agent's stars or duel verdicts, later Tier B scalars and
// the Diction Lab line classifier) agrees with this operator's own judgement (M6N C385-C387). Wilson intervals and pair
// statistics are pure functions (wilson.go, pairs.go); the registry and the service read analyses, ratings and duel verdicts with
// SQL and never write. A critic is trusted only when it agrees with the operator's ratings; nothing is ever discarded
// automatically.
package criticbench

import "math"

// The trust rule (D-M6N-14).
const (
	MinN          = 30   // smallest effective sample size that can say anything
	TrustedLower  = 0.55 // trusted iff n_eff >= MinN and lo > TrustedLower
	ContraryUpper = 0.45 // contrary iff n_eff >= MinN and hi < ContraryUpper
	wilsonZ       = 1.96 // 95 %
)

// Critic states.
const (
	StateInsufficient = "insufficient"
	StateUnknown      = "unknown"
	StateTrusted      = "trusted"
	StateContrary     = "contrary"
)

// Wilson is the 95 % Wilson score interval of k successes in n trials. No trials, or a count out of range, is clamped: (0, 0) is
// [0, 1].
func Wilson(k, n int) (lo, hi float64) {
	if n <= 0 {
		return 0, 1
	}
	k = max(0, min(k, n))
	nf, p := float64(n), float64(k)/float64(n)
	z2 := wilsonZ * wilsonZ
	denom := 1 + z2/nf
	centre := (p + z2/(2*nf)) / denom
	half := wilsonZ * math.Sqrt(p*(1-p)/nf+z2/(4*nf*nf)) / denom
	return max(0, centre-half), min(1, centre+half)
}

// Score is one critic's agreement with one reference.
type Score struct {
	Pairs         int      `json:"pairs" doc:"Pairs of the reference on which the critic has a value"`
	NEff          int      `json:"n_eff" doc:"Effective sample size: the smaller of the pairs the critic called and the distinct takes in them"`
	Agree         int      `json:"agree" doc:"Pairs the critic called the way the operator did"`
	Predicted     int      `json:"predicted" doc:"Pairs on which the critic called a winner"`
	Rate          float64  `json:"rate" doc:"agree / predicted"`
	Lo            float64  `json:"lo" doc:"Wilson 95 % lower bound at n_eff"`
	Hi            float64  `json:"hi" doc:"Wilson 95 % upper bound at n_eff"`
	State         string   `json:"state" enum:"insufficient,unknown,trusted,contrary"`
	Coverage      float64  `json:"coverage" doc:"Pairs on which the critic has a value, over the pairs of the window"`
	Disagreements []string `json:"disagreements,omitempty" doc:"Newest 10 pair keys (a|b) or duel ids the critic called the other way; only when one critic is requested"`
}

// newScore builds a Score from the counts. The interval is taken on (round(Rate x nEff), nEff), so a take shared by many pairs
// never inflates confidence. Figures are rounded to 4 decimals (stable JSON).
func newScore(pairs, nEff, agree, predicted int, coverage float64) Score {
	s := Score{Pairs: pairs, NEff: nEff, Agree: agree, Predicted: predicted, Coverage: round4(coverage), Lo: 0, Hi: 1}
	if predicted > 0 {
		rate := float64(agree) / float64(predicted)
		s.Rate = round4(rate)
		lo, hi := Wilson(int(math.Round(rate*float64(nEff))), nEff)
		s.Lo, s.Hi = round4(lo), round4(hi)
	}
	s.State = stateOf(nEff, s.Lo, s.Hi)
	return s
}

func stateOf(nEff int, lo, hi float64) string {
	switch {
	case nEff < MinN:
		return StateInsufficient
	case lo > TrustedLower:
		return StateTrusted
	case hi < ContraryUpper:
		return StateContrary
	}
	return StateUnknown
}

func round4(x float64) float64 { return math.Round(x*1e4) / 1e4 }
