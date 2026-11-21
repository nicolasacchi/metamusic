package criticbench

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// agents builds the agent critics (C386.2): the stars an agent gave (critic_ratings, one row per take: the last agent to rate a
// take wins, so a take rated by two agents is credited to the later one) on both references, and the duel verdicts an agent
// gave (duel_verdicts source 'critic') on the duels reference with a slot-bias probe.
type agents struct{}

func (agents) Critics(ctx context.Context, q Reader, ref *Reference) ([]Critic, error) {
	stars, names, err := agentStars(ctx, q)
	if err != nil {
		return nil, err
	}
	var out []Critic
	for _, pk := range sortedKeys(stars) {
		dir, md := 1, 1.0
		rt, du := Metric(dir, md, stars[pk], ref.RatingPairs), Metric(dir, md, stars[pk], ref.DuelPairs)
		c := Critic{ID: "agent.stars:" + pk, Label: "Stars by " + names[pk], Kind: KindMetric, Tier: TierAgent, Direction: &dir, MinDelta: &md,
			Provenance: "critic_ratings: the stars this agent gave (one row per take, the last agent to rate a take wins)",
			Scores:     Scores{Ratings: &rt, Duels: &du}}
		c.settle()
		out = append(out, c)
	}
	verdicts, aCount, abCount, err := agentVerdicts(ctx, q)
	if err != nil {
		return nil, err
	}
	for _, pk := range sortedKeys(verdicts) {
		du := Pairwise(ref.DuelPairs, verdicts[pk])
		share, lo, hi, biased := SlotBias(aCount[pk], abCount[pk])
		c := Critic{ID: "agent.duels:" + pk, Label: "Duel verdicts by " + pk, Kind: KindPairwise, Tier: TierAgent,
			Provenance: "duel_verdicts with source critic: this agent's verdict on each duel",
			Scores:     Scores{Duels: &du}, SlotBias: &SlotBiasOf{N: abCount[pk], Share: round4(share), Lo: round4(lo), Hi: round4(hi), Biased: biased}}
		c.settle()
		out = append(out, c)
	}
	return out, nil
}

func agentStars(ctx context.Context, q Reader) (map[string]map[string]float64, map[string]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT track_id, principal_id, principal_name, stars FROM critic_ratings WHERE stars IS NOT NULL`)
	if err != nil {
		return nil, nil, fmt.Errorf("criticbench: read critic ratings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	stars, names := map[string]map[string]float64{}, map[string]string{}
	for rows.Next() {
		var tr, pk, name string
		var st sql.NullInt64
		if err := rows.Scan(&tr, &pk, &name, &st); err != nil {
			return nil, nil, fmt.Errorf("criticbench: scan critic ratings: %w", err)
		}
		if stars[pk] == nil {
			stars[pk] = map[string]float64{}
		}
		stars[pk][tr] = float64(st.Int64)
		names[pk] = name
	}
	return stars, names, rows.Err()
}

func agentVerdicts(ctx context.Context, q Reader) (map[string]map[string]string, map[string]int, map[string]int, error) {
	rows, err := q.QueryContext(ctx, `SELECT duel_id, principal_id, verdict, winner_track_id FROM duel_verdicts WHERE source = 'critic'`)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("criticbench: read critic verdicts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out, aCount, abCount := map[string]map[string]string{}, map[string]int{}, map[string]int{}
	for rows.Next() {
		var duel, pk, verdict string
		var winner sql.NullString
		if err := rows.Scan(&duel, &pk, &verdict, &winner); err != nil {
			return nil, nil, nil, fmt.Errorf("criticbench: scan critic verdicts: %w", err)
		}
		if out[pk] == nil {
			out[pk] = map[string]string{}
		}
		out[pk][duel] = winner.String // "" for a tie or both_bad: judged, not predicted
		switch verdict {
		case "a":
			aCount[pk]++
			abCount[pk]++
		case "b":
			abCount[pk]++
		}
	}
	return out, aCount, abCount, rows.Err()
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
