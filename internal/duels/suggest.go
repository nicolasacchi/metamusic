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
	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// Suggestion bounds (C258).
const (
	suggestLineageTracks = 40
	suggestGlobalTracks  = 60
	suggestDefaultK      = 6
	suggestMaxK          = 15
)

// Suggest ranks the pairs whose next human verdict would teach the ladder most (D-M5-31). It writes nothing. The
// candidate tracks are the 40 most recent of a lineage or the 60 most recent rated-or-dueled tracks of the library
// (discarded takes never); the model is the ladder's own fit over the human blind verdicts among them (critic and
// sighted verdicts never count); a pair with an unjudged open duel or three human verdicts is left out. With a section,
// only pairs whose two tracks have that section timed and can be windowed (the pairs create_duel would accept).
func (s *Service) Suggest(ctx context.Context, q SuggestQuery) (PairSuggestions, error) {
	if s.unconfigured() {
		return PairSuggestions{}, errors.ErrUnsupported
	}
	scope := q.Scope
	if scope == "" {
		scope = "lineage"
	}
	if scope != "lineage" && scope != "global" {
		return PairSuggestions{}, invalid("scope", "scope must be lineage or global")
	}
	if scope == "lineage" && q.RootTrackID == "" {
		return PairSuggestions{}, invalid("root_track_id", "root_track_id is required for scope=lineage")
	}
	var key sectionKey
	if q.Section != nil {
		var err error
		if key, err = parseSection(q.Section); err != nil {
			return PairSuggestions{}, err
		}
	}
	k := q.K
	if k <= 0 {
		k = suggestDefaultK
	}
	k = min(k, suggestMaxK)

	refs, err := s.suggestCandidates(ctx, scope, q.RootTrackID)
	if err != nil {
		return PairSuggestions{}, err
	}
	ids := make([]string, 0, len(refs))
	for id := range refs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	idx := make(map[string]int, len(ids))
	for i, id := range ids {
		idx[id] = i
	}
	sn := &snapshot{}
	verdicts, open := map[[2]int]int{}, map[[2]int]bool{}
	if err := s.suggestVerdicts(ctx, idx, sn, verdicts, open); err != nil {
		return PairSuggestions{}, err
	}
	sn.build("lineage", ids) // the item order is the candidate order, whatever the verdict rows mention
	fit, err := ladder.FitBTD(len(ids), sn.cmps, s.o.Ladder)
	if err != nil {
		return PairSuggestions{}, fmt.Errorf("duels: suggest fit: %w", err)
	}
	out := PairSuggestions{Scope: scope, Pairs: []PairSuggestion{}, Model: SuggestModel{Nu: round4(fit.Nu), VerdictsCounted: len(sn.rows)}}
	if scope == "lineage" {
		root := q.RootTrackID
		out.RootTrackID = &root
	}
	limit := k
	var spans map[string]clip.SectionSpan
	if q.Section != nil {
		label := key.canonical()
		out.Section = &label
		if spans, err = s.suggestSpans(ctx, ids, key); err != nil {
			return PairSuggestions{}, err
		}
		limit = len(ids) * len(ids) // filter first, then cut
	}
	for _, ps := range ladder.SuggestPairs(fit, ids, verdicts, open, limit) {
		if q.Section != nil {
			sa, okA := spans[ps.AID]
			sb, okB := spans[ps.BID]
			if !okA || !okB {
				continue
			}
			if _, _, err := clip.ResolveSectionWindows(sa, sb, refs[ps.AID].DurationS, refs[ps.BID].DurationS); err != nil {
				continue
			}
		}
		out.Pairs = append(out.Pairs, PairSuggestion{ATrackID: ps.AID, BTrackID: ps.BID, ATitle: refs[ps.AID].Title, BTitle: refs[ps.BID].Title,
			Score: round4(ps.Score), PAWinsB: round4(ps.PAWins), SDA: round4(ps.SDA), SDB: round4(ps.SDB), HumanVerdict: ps.HumanVerdicts})
		if len(out.Pairs) == k {
			break
		}
	}
	return out, nil
}

// suggestCandidates returns the candidate tracks by id.
func (s *Service) suggestCandidates(ctx context.Context, scope, root string) (map[string]library.TrackRef, error) {
	var refs []library.TrackRef
	if scope == "lineage" {
		all, err := s.o.Library.LineageTracks(ctx, root)
		if errors.Is(err, library.ErrNotFound) {
			return nil, problem.E(404, "NOT_FOUND", "lineage not found", map[string]any{"track_id": root})
		}
		if err != nil {
			return nil, err
		}
		refs = all
	} else {
		rows, err := s.o.Store.R.QueryContext(ctx, `SELECT t.id FROM tracks t WHERE t.status <> 'discarded' AND (
			EXISTS (SELECT 1 FROM ratings r WHERE r.track_id = t.id AND r.source = 'human' AND r.stars IS NOT NULL)
			OR EXISTS (SELECT 1 FROM duels d WHERE d.slot_a_track_id = t.id OR d.slot_b_track_id = t.id))
			ORDER BY t.recorded_at DESC, t.id DESC LIMIT ?`, suggestGlobalTracks)
		if err != nil {
			return nil, fmt.Errorf("duels: suggest candidates: %w", err)
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("duels: suggest candidates: %w", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		m, err := s.o.Library.TrackRefs(ctx, ids)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if r, ok := m[id]; ok {
				refs = append(refs, r)
			}
		}
	}
	live := refs[:0:0]
	for _, r := range refs {
		if r.Status != "discarded" {
			live = append(live, r)
		}
	}
	// the most recent first; the lineage keeps its 40 most recent, the global list was cut in SQL
	sort.SliceStable(live, func(a, b int) bool {
		if !live[a].RecordedAt.Equal(live[b].RecordedAt) {
			return live[a].RecordedAt.After(live[b].RecordedAt)
		}
		return live[a].ID > live[b].ID
	})
	if scope == "lineage" && len(live) > suggestLineageTracks {
		live = live[:suggestLineageTracks]
	}
	out := make(map[string]library.TrackRef, len(live))
	for _, r := range live {
		out[r.ID] = r
	}
	return out, nil
}

// suggestVerdicts reads every human verdict between candidates: blind ones feed the fit (sn.rows), all of them count
// toward a pair's settled state; open marks pairs with an unexpired duel that has no human verdict yet.
func (s *Service) suggestVerdicts(ctx context.Context, idx map[string]int, sn *snapshot, verdicts map[[2]int]int, open map[[2]int]bool) error {
	pair := func(a, b string) ([2]int, bool) {
		i, okA := idx[a]
		j, okB := idx[b]
		if !okA || !okB {
			return [2]int{}, false
		}
		if i > j {
			i, j = j, i
		}
		return [2]int{i, j}, true
	}
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT d.slot_a_track_id, d.slot_b_track_id, v.verdict, d.blind FROM duel_verdicts v JOIN duels d ON d.id = v.duel_id
		WHERE v.source = 'human' ORDER BY v.created_at, v.id`)
	if err != nil {
		return fmt.Errorf("duels: suggest verdicts: %w", err)
	}
	for rows.Next() {
		var r inputRow
		var blind bool
		if err := rows.Scan(&r.a, &r.b, &r.verdict, &blind); err != nil {
			_ = rows.Close()
			return fmt.Errorf("duels: suggest verdicts: %w", err)
		}
		p, ok := pair(r.a, r.b)
		if !ok {
			continue
		}
		verdicts[p]++
		if blind {
			sn.rows = append(sn.rows, r)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	orows, err := s.o.Store.R.QueryContext(ctx, `SELECT d.slot_a_track_id, d.slot_b_track_id FROM duels d WHERE d.expires_at > ?
		AND NOT EXISTS (SELECT 1 FROM duel_verdicts h WHERE h.duel_id = d.id AND h.source = 'human')`, rfc(s.now()))
	if err != nil {
		return fmt.Errorf("duels: suggest open: %w", err)
	}
	for orows.Next() {
		var a, b string
		if err := orows.Scan(&a, &b); err != nil {
			_ = orows.Close()
			return fmt.Errorf("duels: suggest open: %w", err)
		}
		if p, ok := pair(a, b); ok {
			open[p] = true
		}
	}
	return orows.Close()
}

// suggestSpans returns the span of the section on every candidate that has it timed exactly once.
func (s *Service) suggestSpans(ctx context.Context, ids []string, key sectionKey) (map[string]clip.SectionSpan, error) {
	out := map[string]clip.SectionSpan{}
	for _, id := range ids {
		t, err := library.Timing(ctx, s.o.Store.R, id)
		if errors.Is(err, library.ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if sec, ok := key.find(t); ok {
			out[id] = clip.SectionSpan{StartS: sec.StartS, EndS: sec.EndS}
		}
	}
	return out, nil
}
