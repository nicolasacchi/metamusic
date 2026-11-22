package nightshift

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/ladder"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/mandates"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// RecentNights is how far back an unrated night take blocks its lineage (C373.1).
const RecentNights = 3

// StrengthSource is the ladder read (duels.Service implements it).
type StrengthSource interface {
	Strengths(ctx context.Context, ids []string) (map[string]ladder.Strength, float64, error)
}

// Reader builds the planner's lineages from SQL. It reads only: no write, no spend, no lyrics (the recipe's style is the only
// text it loads), and the Owner's signal comes from the owner_ratings view alone (D-U1-9): agent critics never rate a take here.
type Reader struct {
	Strengths StrengthSource         // required
	Backlog   generation.BacklogGate // nil: no backlog gate
}

type take struct {
	id, root, engine, style string
	stars                   *int
}

// Lineages returns one entry per root that has a best take, in root order. Roots filtered by the mandate scope are still
// returned (the planner reports why it dropped them); only the engines the scope names are read.
func (r *Reader) Lineages(ctx context.Context, q lineage.Querier, scope mandates.Scope, now time.Time) ([]Lineage, error) {
	takes, err := readTakes(ctx, q, scope.Engines)
	if err != nil {
		return nil, err
	}
	if len(takes) == 0 {
		return nil, nil
	}
	ids := make([]string, len(takes))
	for i, t := range takes {
		ids[i] = t.id
	}
	st, _, err := r.Strengths.Strengths(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("nightshift: strengths: %w", err)
	}
	byRoot := map[string][]take{}
	var roots []string
	for _, t := range takes {
		if _, ok := byRoot[t.root]; !ok {
			roots = append(roots, t.root)
		}
		byRoot[t.root] = append(byRoot[t.root], t)
	}
	sort.Strings(roots)
	unjudged, err := recentUnjudged(ctx, q, now.Add(-RecentNights*24*time.Hour))
	if err != nil {
		return nil, err
	}
	var out []Lineage
	for _, root := range roots {
		best, ok := bestTake(byRoot[root], st)
		if !ok {
			continue
		}
		l := Lineage{RootID: root, BestTrackID: best.id, Engine: best.engine, Style: best.style, BestStars: best.stars,
			RecentUnjudged: unjudged[root]}
		if s, has := st[best.id]; has && s.NDuels > 0 {
			u, sd := s.U, s.SD
			l.U, l.SD, l.NDuels = &u, &sd, s.NDuels
		}
		if r.Backlog != nil && r.Backlog.Limit() > 0 {
			n, err := r.Backlog.Backlog(ctx, q, root)
			if err != nil {
				return nil, fmt.Errorf("nightshift: backlog: %w", err)
			}
			l.Backlogged = n >= r.Backlog.Limit()
		}
		out = append(out, l)
	}
	return out, nil
}

// bestTake is the best take rule (C373.1): the highest U among takes with at least 3 counted duels, else the highest Owner
// stars; unrated takes without enough duels are not candidates. Ties go to the newer take id.
func bestTake(ts []take, st map[string]ladder.Strength) (take, bool) {
	var best *take
	var bestU float64
	for i := range ts {
		t := &ts[i]
		s := st[t.id]
		if s.NDuels < minDuelsForU {
			continue
		}
		if best == nil || s.U > bestU || (s.U == bestU && t.id > best.id) {
			best, bestU = t, s.U
		}
	}
	if best != nil {
		return *best, true
	}
	for i := range ts {
		t := &ts[i]
		if t.stars == nil {
			continue
		}
		if best == nil || *t.stars > *best.stars || (*t.stars == *best.stars && t.id > best.id) {
			best = t
		}
	}
	if best == nil {
		return take{}, false
	}
	return *best, true
}

// readTakes lists the non-discarded renders of the given engines with the Owner's stars. A render is a take whose recipe has a
// recipe_keys row (reference stubs have none).
func readTakes(ctx context.Context, q lineage.Querier, engines []string) ([]take, error) {
	if len(engines) == 0 {
		return nil, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(engines)), ",")
	args := make([]any, len(engines))
	for i, e := range engines {
		args[i] = e
	}
	rows, err := q.QueryContext(ctx, `SELECT t.id, t.root_track_id, r.engine, r.style, o.stars
		FROM tracks t JOIN recipes r ON r.id = t.recipe_id
		JOIN recipe_keys k ON k.recipe_id = r.id
		LEFT JOIN owner_ratings o ON o.track_id = t.id
		WHERE t.status <> 'discarded' AND r.engine IN (`+marks+`) ORDER BY t.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("nightshift: read takes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []take
	for rows.Next() {
		var t take
		var s sql.NullInt64
		if err := rows.Scan(&t.id, &t.root, &t.engine, &t.style, &s); err != nil {
			return nil, fmt.Errorf("nightshift: read takes: %w", err)
		}
		if s.Valid {
			v := int(s.Int64)
			t.stars = &v
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// recentUnjudged marks the roots with a night-shift take since `since` that the Owner has not rated.
func recentUnjudged(ctx context.Context, q lineage.Querier, since time.Time) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT t.root_track_id
		FROM batches b JOIN jobs j ON j.batch_id = b.id JOIN tracks t ON t.id = j.track_id
		WHERE b.principal_id = ? AND b.created_at >= ? AND t.status <> 'discarded'
		AND NOT EXISTS (SELECT 1 FROM owner_ratings o WHERE o.track_id = t.id)`, HolderKey, store.FormatTime(since))
	if err != nil {
		return nil, fmt.Errorf("nightshift: recent unjudged: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var root string
		if err := rows.Scan(&root); err != nil {
			return nil, fmt.Errorf("nightshift: recent unjudged: %w", err)
		}
		out[root] = true
	}
	return out, rows.Err()
}
