package criticbench

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// metricDef is one Tier A metric critic (C386.1). The directions are hypotheses, and the output says so.
type metricDef struct {
	id, label, analyzer string
	direction           int
	minDelta            float64
	get                 func(m map[string]any) (float64, bool)
	path                string
}

func num(path ...string) func(map[string]any) (float64, bool) {
	return func(m map[string]any) (float64, bool) {
		var cur any = m
		for _, k := range path {
			mm, ok := cur.(map[string]any)
			if !ok {
				return 0, false
			}
			cur = mm[k]
		}
		f, ok := cur.(float64)
		return f, ok
	}
}

// longestLoopS is the longest repeat of any token stream (analyses.token_loops: streams[].longest_repeat.s).
func longestLoopS(m map[string]any) (float64, bool) {
	streams, _ := m["streams"].([]any)
	best, ok := 0.0, false
	for _, s := range streams {
		sm, _ := s.(map[string]any)
		if v, has := num("longest_repeat", "s")(sm); has {
			best, ok = max(best, v), true
		}
	}
	return best, ok
}

var tierADefs = []metricDef{
	{"tier_a.true_peak", "True peak (dBTP)", "loudness", -1, 0.5, num("true_peak_dbtp"), "loudness.true_peak_dbtp"},
	{"tier_a.loudness_range", "Loudness range (LU)", "loudness", +1, 1.0, num("lra_lu"), "loudness.lra_lu"},
	{"tier_a.bandwidth", "Bandwidth (Hz)", "spectrum", +1, 500, num("bandwidth_hz"), "spectrum.bandwidth_hz"},
	{"tier_a.clipped_s", "Clipped seconds", "clipping", -1, 0.5, num("clipped_s"), "clipping.clipped_s"},
	{"tier_a.silence_internal_s", "Internal silence (s)", "silence", -1, 1.0, num("internal_s"), "silence.internal_s"},
	{"tier_a.token_loop_s", "Longest token loop (s)", "token_loops", -1, 2.0, longestLoopS, "token_loops.streams[].longest_repeat.s"},
	{"tier_a.energy", "Energy", "energy", +1, 0.05, num("energy"), "energy.energy"},
}

// tierA reads the latest analyses row of every track and analyzer.
type tierA struct{}

func (tierA) Critics(ctx context.Context, q Reader, ref *Reference) ([]Critic, error) {
	want := map[string]struct{}{}
	for _, ps := range [][]Pair{ref.RatingPairs, ref.DuelPairs} {
		for _, p := range ps {
			want[p.A], want[p.B] = struct{}{}, struct{}{}
		}
	}
	analyzers, marks := []string{}, []string{}
	seen := map[string]bool{}
	for _, d := range tierADefs {
		if !seen[d.analyzer] {
			seen[d.analyzer] = true
			analyzers = append(analyzers, d.analyzer)
			marks = append(marks, "?")
		}
	}
	args := make([]any, len(analyzers))
	for i, a := range analyzers {
		args[i] = a
	}
	// ascending, so the latest row per (track, analyzer) is the last one read
	rows, err := q.QueryContext(ctx, `SELECT track_id, analyzer, metrics_json FROM analyses WHERE analyzer IN (`+strings.Join(marks, ",")+
		`) ORDER BY created_at, version`, args...)
	if err != nil {
		return nil, fmt.Errorf("criticbench: read analyses: %w", err)
	}
	defer func() { _ = rows.Close() }()
	type key struct{ track, analyzer string }
	latest := map[key]string{}
	anyRow := map[string]bool{}
	for rows.Next() {
		var tr, an, js string
		if err := rows.Scan(&tr, &an, &js); err != nil {
			return nil, fmt.Errorf("criticbench: scan analyses: %w", err)
		}
		anyRow[an] = true
		if _, ok := want[tr]; ok {
			latest[key{tr, an}] = js
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("criticbench: read analyses: %w", err)
	}
	decoded := map[key]map[string]any{}
	var out []Critic
	for _, d := range tierADefs {
		if !anyRow[d.analyzer] {
			continue
		}
		vals := map[string]float64{}
		for tr := range want {
			k := key{tr, d.analyzer}
			js, ok := latest[k]
			if !ok {
				continue
			}
			m, done := decoded[k]
			if !done {
				if json.Unmarshal([]byte(js), &m) != nil {
					m = nil
				}
				decoded[k] = m
			}
			if v, ok := d.get(m); ok {
				vals[tr] = v
			}
		}
		dir, md := d.direction, d.minDelta
		rt, du := Metric(dir, md, vals, ref.RatingPairs), Metric(dir, md, vals, ref.DuelPairs)
		c := Critic{ID: d.id, Label: d.label, Kind: KindMetric, Tier: TierA, Direction: &dir, MinDelta: &md,
			Provenance: "analyses." + d.path + " (latest row per take); the direction is a hypothesis, not a finding",
			Scores:     Scores{Ratings: &rt, Duels: &du}}
		c.settle()
		out = append(out, c)
	}
	return out, nil
}
