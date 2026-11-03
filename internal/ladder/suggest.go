package ladder

import "sort"

// MaxHumanVerdictsPerPair is the number of human verdicts after which a pair is considered settled and never suggested.
const MaxHumanVerdictsPerPair = 3

// PairScore is one suggested duel: the item indices (A < B), their ids when SuggestPairs was given ids, the
// information score and the quantities it is made of.
type PairScore struct {
	A, B          int
	AID, BID      string
	Score         float64 // p(1-p)(sdA^2 + sdB^2)
	PAWins        float64 // P(A beats B) + P(tie)/2
	SDA, SDB      float64
	HumanVerdicts int
}

// SuggestPairs ranks the pairs of the items of fit by expected information (D-M5-31), pure and deterministic: the score of a
// pair is p(1-p)(sdA^2 + sdB^2) with p = win + tie/2 from the Davidson outcome distribution, so close, poorly known pairs rank first.
// verdicts counts human verdicts per pair and open marks pairs with an unjudged duel; both are keyed by the two item
// indices in either order. Pairs with an open duel or MaxHumanVerdictsPerPair verdicts are left out. ids, when it has one entry
// per item, fills AID/BID. At most k pairs are returned, best first, equal scores by (A, B).
func SuggestPairs(fit Fit, ids []string, verdicts map[[2]int]int, open map[[2]int]bool, k int) []PairScore {
	n := min(len(fit.Theta), len(fit.SD))
	if k <= 0 || n < 2 {
		return []PairScore{}
	}
	norm := func(i, j int) [2]int {
		if i > j {
			i, j = j, i
		}
		return [2]int{i, j}
	}
	vcount := map[[2]int]int{}
	for p, c := range verdicts {
		vcount[norm(p[0], p[1])] += c
	}
	isOpen := map[[2]int]bool{}
	for p, o := range open {
		if o {
			isOpen[norm(p[0], p[1])] = true
		}
	}
	var out []PairScore
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			key := [2]int{i, j}
			if isOpen[key] || vcount[key] >= MaxHumanVerdictsPerPair {
				continue
			}
			win, tie, _ := PBeats(fit.Theta[i], fit.Theta[j], fit.Nu)
			p := win + tie/2
			ps := PairScore{A: i, B: j, Score: p * (1 - p) * (fit.SD[i]*fit.SD[i] + fit.SD[j]*fit.SD[j]),
				PAWins: p, SDA: fit.SD[i], SDB: fit.SD[j], HumanVerdicts: vcount[key]}
			if len(ids) == len(fit.Theta) {
				ps.AID, ps.BID = ids[i], ids[j]
			}
			out = append(out, ps)
		}
	}
	sort.SliceStable(out, func(x, y int) bool { return out[x].Score > out[y].Score })
	if len(out) > k {
		out = out[:k]
	}
	if out == nil {
		out = []PairScore{}
	}
	return out
}
