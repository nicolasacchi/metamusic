package criticbench

import (
	"context"
	"fmt"
	"slices"
)

// TierBScalar is the listening-check numbers of one take (Ears C346.2); a field is nil when its row is missing. It mirrors
// ears.TierBScalar, which the app adapts, so this package never imports the Ears service.
type TierBScalar struct {
	CER, WER, MelodyChroma *float64
}

// ScalarSource reads the stored Tier B scalars of takes; a take with no Tier B row is absent from the result. Reading is free: no
// GPU work happens here (D-M6N-OP-5).
type ScalarSource interface {
	TierBScalars(ctx context.Context, trackIDs []string) (map[string]TierBScalar, error)
}

// tierBDef is one Tier B metric critic (C386.3). The directions are hypotheses, and the output says so.
type tierBDef struct {
	id, label, path string
	direction       int
	minDelta        float64
	get             func(TierBScalar) *float64
}

var tierBDefs = []tierBDef{
	{"tier_b.cer", "Listening-check CER", "tier_b_diction $.overall.cer", -1, 0.05, func(s TierBScalar) *float64 { return s.CER }},
	{"tier_b.melody_chroma", "Melody chroma match", "tier_b_adherence $.melody_chroma", +1, 0.1, func(s TierBScalar) *float64 { return s.MelodyChroma }},
	{"tier_b.wer", "Listening-check WER", "tier_b_diction $.overall.wer", -1, 0.05, func(s TierBScalar) *float64 { return s.WER }},
}

// tierB is the Source of the Tier B metric critics. It reads stored scalars only.
type tierB struct{ src ScalarSource }

// NewTierB builds the Tier B source over a scalar reader (the Ears service, adapted by the app).
func NewTierB(src ScalarSource) Source { return tierB{src: src} }

func (t tierB) values(ctx context.Context, tracks []string) (map[string]TierBScalar, error) {
	sc, err := t.src.TierBScalars(ctx, tracks)
	if err != nil {
		return nil, fmt.Errorf("criticbench: tier B scalars: %w", err)
	}
	return sc, nil
}

func pick(sc map[string]TierBScalar, d tierBDef) map[string]float64 {
	vals := map[string]float64{}
	for tr, s := range sc {
		if v := d.get(s); v != nil {
			vals[tr] = *v
		}
	}
	return vals
}

func sortedTracks(want map[string]struct{}) []string {
	out := make([]string, 0, len(want))
	for t := range want {
		out = append(out, t)
	}
	slices.Sort(out)
	return out
}

// Critics lists a critic only when it has a readable value for some take of the reference windows (the B1 rule).
func (t tierB) Critics(ctx context.Context, _ Reader, ref *Reference) ([]Critic, error) {
	want := sortedTracks(refTracks(ref))
	if len(want) == 0 {
		return nil, nil
	}
	sc, err := t.values(ctx, want)
	if err != nil {
		return nil, err
	}
	var out []Critic
	for _, d := range tierBDefs {
		vals := pick(sc, d)
		if len(vals) == 0 {
			continue
		}
		dir, md := d.direction, d.minDelta
		rt, du := Metric(dir, md, vals, ref.RatingPairs), Metric(dir, md, vals, ref.DuelPairs)
		c := Critic{ID: d.id, Label: d.label, Kind: KindMetric, Tier: TierB, Direction: &dir, MinDelta: &md,
			Provenance: "analyses." + d.path + " (current row; takes rendered with a listening check only); the direction is a hypothesis, not a finding",
			Scores:     Scores{Ratings: &rt, Duels: &du}}
		c.settle()
		out = append(out, c)
	}
	return out, nil
}

// MetricValues implements MetricValuer.
func (t tierB) MetricValues(ctx context.Context, _ Reader, id string, tracks []string) (map[string]float64, bool, error) {
	for _, d := range tierBDefs {
		if d.id != id {
			continue
		}
		sc, err := t.values(ctx, tracks)
		if err != nil {
			return nil, true, err
		}
		return pick(sc, d), true, nil
	}
	return nil, false, nil
}
