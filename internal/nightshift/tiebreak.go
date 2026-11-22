package nightshift

import (
	"context"
	"fmt"
	"math"
	"sort"
)

// TrustedCritic is a metric critic the Owner's own ratings have shown to be right (Critic Bench state trusted), with its values
// for the best takes of tonight's lineages. The bench lives in a package this one may not import, so the app adapts it.
type TrustedCritic struct {
	ID        string
	Direction int // +1: a larger value is better, -1: smaller (a hypothesis the bench has confirmed)
	Values    map[string]float64
}

// TrustSource returns the trusted metric critics with their values for the given takes. It only reads.
type TrustSource interface {
	TrustedMetrics(ctx context.Context, tracks []string) ([]TrustedCritic, error)
}

// TieBreak is the planner's last tie-breaker (C387.4): for every lineage, the sum over the trusted critics of
// direction x z(value of the lineage's best take), z being the population z-score over the lineages that have a value. A critic
// with fewer than two values or no spread says nothing; a lineage without a value gets 0 from it. Contrary and unknown critics are
// never passed in, and none is ever inverted. Pure and deterministic. It returns nil when nothing contributed.
func TieBreak(lineages []Lineage, critics []TrustedCritic) map[string]float64 {
	var out map[string]float64
	for _, c := range critics {
		if c.Direction == 0 {
			continue
		}
		type rv struct {
			root string
			v    float64
		}
		var vs []rv
		for _, l := range lineages {
			if v, ok := c.Values[l.BestTrackID]; ok && !math.IsNaN(v) && !math.IsInf(v, 0) {
				vs = append(vs, rv{l.RootID, v})
			}
		}
		if len(vs) < 2 {
			continue
		}
		sort.Slice(vs, func(i, j int) bool { return vs[i].root < vs[j].root }) // a fixed summation order
		sum := 0.0
		for _, x := range vs {
			sum += x.v
		}
		mean := sum / float64(len(vs))
		ss := 0.0
		for _, x := range vs {
			ss += (x.v - mean) * (x.v - mean)
		}
		sd := math.Sqrt(ss / float64(len(vs)))
		if sd == 0 {
			continue
		}
		if out == nil {
			out = map[string]float64{}
			for _, l := range lineages {
				out[l.RootID] = 0
			}
		}
		for _, x := range vs {
			out[x.root] += float64(c.Direction) * (x.v - mean) / sd
		}
	}
	return out
}

// TieBreakFor asks the source for the best takes of the lineages and computes TieBreak. A nil source or no lineages gives nil. An
// error is returned for the caller to log and plan without a tie-break: trust must never stop a night.
func TieBreakFor(ctx context.Context, src TrustSource, lineages []Lineage) (map[string]float64, error) {
	if src == nil || len(lineages) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(lineages))
	for _, l := range lineages {
		ids = append(ids, l.BestTrackID)
	}
	sort.Strings(ids)
	cs, err := src.TrustedMetrics(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("nightshift: trusted critics: %w", err)
	}
	return TieBreak(lineages, cs), nil
}
