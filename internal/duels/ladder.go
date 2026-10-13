package duels

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/nicolasacchi/metamusic/internal/ladder"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// Prior defaults mirror ladder.Options (C47) so the model block can state them.
func (s *Service) priorSD() float64 {
	if s.o.Ladder.PriorSD > 0 {
		return s.o.Ladder.PriorSD
	}
	return 1.5
}

func (s *Service) nuPrior() (shape, rate float64) {
	shape, rate = s.o.Ladder.NuShape, s.o.Ladder.NuRate
	if shape <= 0 {
		shape = 2
	}
	if rate <= 0 {
		rate = 2
	}
	return shape, rate
}

// inputRow is one counted human verdict.
type inputRow struct {
	a, b    string // slot tracks
	verdict string
}

// scopeJoin restricts verdict queries to a lineage.
func scopeJoin(scope, root string) (join string, args []any) {
	if scope != "lineage" {
		return "", nil
	}
	return ` JOIN tracks ta ON ta.id = d.slot_a_track_id JOIN tracks tb ON tb.id = d.slot_b_track_id AND ta.root_track_id = ? AND tb.root_track_id = ?`, []any{root, root}
}

// tally counts the counted verdicts that involve one track.
type tally struct{ wins, losses, ties, bothBad int }

func (t tally) n() int { return t.wins + t.losses + t.ties + t.bothBad }

// snapshot is one consistent read of everything a ladder derives from verdict rows.
type snapshot struct {
	key                             fitKey
	rows                            []inputRow
	criticExcluded, sightedExcluded int
	ids                             []string
	tallies                         []tally
	cmps                            []ladder.Comparison
	bothBad, ties, nAB, aWins       int
}

// read takes the snapshot inside one read transaction: the cache key, the counted human
// verdicts (blind only unless sighted; critic rows are never selected, C48.6) and the
// exclusion counts.
func (s *Service) read(ctx context.Context, scope, root string, sighted bool, lineageIDs []string) (*snapshot, error) {
	tx, err := s.o.Store.R.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("duels: ladder: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	sn := &snapshot{key: fitKey{scope: scope, root: root, sighted: sighted}}
	if err := tx.QueryRowContext(ctx, `SELECT count(*), COALESCE(max(rowid), 0) FROM duel_verdicts WHERE source = 'human'`).Scan(&sn.key.n, &sn.key.maxRowid); err != nil {
		return nil, fmt.Errorf("duels: ladder key: %w", err)
	}
	join, args := scopeJoin(scope, root)
	//nolint:gosec // join is one of two constant fragments from scopeJoin; values are bound
	rows, err := tx.QueryContext(ctx, `SELECT d.slot_a_track_id, d.slot_b_track_id, v.verdict FROM duel_verdicts v JOIN duels d ON d.id = v.duel_id`+join+
		` WHERE v.source = 'human' AND (d.blind = 1 OR ?) ORDER BY v.created_at, v.id`, append(slices.Clone(args), sighted)...)
	if err != nil {
		return nil, fmt.Errorf("duels: ladder inputs: %w", err)
	}
	for rows.Next() {
		var r inputRow
		if err := rows.Scan(&r.a, &r.b, &r.verdict); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("duels: ladder inputs: %w", err)
		}
		sn.rows = append(sn.rows, r)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	//nolint:gosec // join is one of two constant fragments from scopeJoin; values are bound
	grows, err := tx.QueryContext(ctx, `SELECT v.source, d.blind, count(*) FROM duel_verdicts v JOIN duels d ON d.id = v.duel_id`+join+` GROUP BY v.source, d.blind`, args...)
	if err != nil {
		return nil, fmt.Errorf("duels: ladder counts: %w", err)
	}
	for grows.Next() {
		var src string
		var blind bool
		var n int
		if err := grows.Scan(&src, &blind, &n); err != nil {
			_ = grows.Close()
			return nil, fmt.Errorf("duels: ladder counts: %w", err)
		}
		switch {
		case src == "critic":
			sn.criticExcluded += n
		case !blind && !sighted:
			sn.sightedExcluded += n
		}
	}
	if err := grows.Close(); err != nil {
		return nil, err
	}
	sn.build(scope, lineageIDs)
	return sn, nil
}

// build derives the item order, the comparisons and the tallies from the rows.
func (sn *snapshot) build(scope string, lineageIDs []string) {
	sn.ids = lineageIDs
	if scope != "lineage" {
		seen := map[string]bool{}
		for _, r := range sn.rows {
			for _, id := range []string{r.a, r.b} {
				if !seen[id] {
					seen[id] = true
					sn.ids = append(sn.ids, id)
				}
			}
		}
		slices.Sort(sn.ids)
	}
	idx := make(map[string]int, len(sn.ids))
	for i, id := range sn.ids {
		idx[id] = i
	}
	sn.tallies = make([]tally, len(sn.ids))
	for _, r := range sn.rows {
		a, b := idx[r.a], idx[r.b]
		switch r.verdict {
		case "a", "b":
			w, l := a, b
			if r.verdict == "b" {
				w, l = b, a
			}
			sn.cmps = append(sn.cmps, ladder.Comparison{Winner: w, Loser: l})
			sn.tallies[w].wins++
			sn.tallies[l].losses++
			sn.nAB++
			if r.verdict == "a" {
				sn.aWins++
			}
		case "tie":
			sn.cmps = append(sn.cmps, ladder.Comparison{Winner: a, Loser: b, Tie: true})
			sn.tallies[a].ties++
			sn.tallies[b].ties++
			sn.ties++
		case "both_bad":
			sn.cmps = append(sn.cmps, ladder.Comparison{Winner: ladder.Anchor, Loser: a}, ladder.Comparison{Winner: ladder.Anchor, Loser: b})
			sn.tallies[a].bothBad++
			sn.tallies[b].bothBad++
			sn.bothBad++
		}
	}
}

// fitFor returns the snapshot, the (possibly cached) fit and the library refs of the items.
func (s *Service) fitFor(ctx context.Context, scope, root string, sighted bool) (*snapshot, *fitEntry, map[string]library.TrackRef, error) {
	var lineage []library.TrackRef
	var lineageIDs []string
	if scope == "lineage" {
		var err error
		if lineage, err = s.o.Library.LineageTracks(ctx, root); err != nil {
			if errors.Is(err, library.ErrNotFound) {
				return nil, nil, nil, problem.E(404, "NOT_FOUND", "lineage not found", map[string]any{"track_id": root})
			}
			return nil, nil, nil, err
		}
		for _, r := range lineage {
			lineageIDs = append(lineageIDs, r.ID)
		}
		slices.Sort(lineageIDs)
	}
	sn, err := s.read(ctx, scope, root, sighted, lineageIDs)
	if err != nil {
		return nil, nil, nil, err
	}
	e, ok := s.cache.get(sn.key)
	// a track joining the lineage without a verdict changes the item order
	if !ok || !slices.Equal(e.ids, sn.ids) {
		fit, err := ladder.FitBTD(len(sn.ids), sn.cmps, s.o.Ladder)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("duels: ladder fit: %w", err)
		}
		e = &fitEntry{ids: sn.ids, fit: fit}
		s.cache.put(sn.key, e)
	}
	refs := make(map[string]library.TrackRef, len(sn.ids))
	if scope == "lineage" {
		for _, r := range lineage {
			refs[r.ID] = r
		}
	} else if refs, err = s.o.Library.TrackRefs(ctx, sn.ids); err != nil {
		return nil, nil, nil, err
	}
	return sn, e, refs, nil
}

// Ladder fits and returns the ladder (C48.6).
func (s *Service) Ladder(ctx context.Context, q LadderQuery) (LadderPage, error) {
	if s.unconfigured() {
		return LadderPage{}, errors.ErrUnsupported
	}
	scope := q.Scope
	switch scope {
	case "":
		scope = "global"
	case "global", "lineage":
	default:
		return LadderPage{}, invalid("scope", "scope must be global or lineage")
	}
	if scope == "lineage" && q.RootTrackID == "" {
		return LadderPage{}, invalid("root_track_id", "root_track_id is required for scope=lineage")
	}
	root := q.RootTrackID
	if scope == "global" {
		root = ""
	}
	off, err := parseCursor(q.Cursor)
	if err != nil {
		return LadderPage{}, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = ladderLimit
	}
	limit = min(limit, maxLadder)
	sn, e, refs, err := s.fitFor(ctx, scope, root, q.IncludeSighted)
	if err != nil {
		return LadderPage{}, err
	}
	items := make([]LadderItem, len(e.ids))
	for i, id := range e.ids {
		u, sd := round4(e.fit.Theta[i]), round4(e.fit.SD[i])
		t := sn.tallies[i]
		it := LadderItem{TrackID: id, U: u, SD: sd, CI95: [2]float64{round4(u - 1.96*sd), round4(u + 1.96*sd)},
			NDuels: t.n(), Wins: t.wins, Losses: t.losses, Ties: t.ties, BothBad: t.bothBad, Provisional: t.n() < 3 || sd > 1.0}
		if r, ok := refs[id]; ok {
			it.Title, it.Seed, it.RootTrackID = r.Title, r.Seed, r.RootTrackID
		}
		items[i] = it
	}
	sort.SliceStable(items, func(a, b int) bool {
		if items[a].U != items[b].U {
			return items[a].U > items[b].U
		}
		return items[a].TrackID < items[b].TrackID
	})
	for i := range items {
		items[i].Rank = i + 1
	}
	shape, rate := s.nuPrior()
	page := LadderPage{Scope: scope, IncludeSighted: q.IncludeSighted, GeneratedAt: rfc(s.now()), Total: len(items), Items: []LadderItem{},
		Model: LadderModel{Kind: "btd", PriorSD: s.priorSD(), NuPrior: fmt.Sprintf("gamma(%g,%g)", shape, rate), Anchor: "reject", AnchorU: 0,
			Nu: e.fit.Nu, Iterations: e.fit.Iterations, Converged: e.fit.Converged},
		Counts: LadderCounts{VerdictsHumanCounted: len(sn.rows), VerdictsCriticExcluded: sn.criticExcluded, VerdictsSightedExcluded: sn.sightedExcluded,
			BothBad: sn.bothBad, Ties: sn.ties, Tracks: len(items)},
		Diagnostics: LadderDiagnostics{NAB: sn.nAB}}
	if scope == "lineage" {
		page.RootTrackID = &root
	}
	if sn.nAB > 0 {
		r := float64(sn.aWins) / float64(sn.nAB)
		page.Diagnostics.SlotAWinRate = &r
	}
	if off < len(items) {
		page.Items = append(page.Items, items[off:min(off+limit, len(items))]...)
	}
	if off+limit < len(items) {
		c := offsetCursor(off + limit)
		page.NextCursor = &c
	}
	return page, nil
}

// Strengths returns the global fit restricted to ids, and nu (C48.6).
func (s *Service) Strengths(ctx context.Context, ids []string) (map[string]ladder.Strength, float64, error) {
	if s.unconfigured() {
		return nil, 0, errors.ErrUnsupported
	}
	sn, e, _, err := s.fitFor(ctx, "global", "", false)
	if err != nil {
		return nil, 0, err
	}
	pos := make(map[string]int, len(e.ids))
	for i, id := range e.ids {
		pos[id] = i
	}
	out := make(map[string]ladder.Strength, len(ids))
	for _, id := range ids {
		if i, ok := pos[id]; ok {
			out[id] = ladder.Strength{U: round4(e.fit.Theta[i]), SD: round4(e.fit.SD[i]), NDuels: sn.tallies[i].n()}
		}
	}
	return out, e.fit.Nu, nil
}
