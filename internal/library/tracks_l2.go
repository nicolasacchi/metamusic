package library

import (
	"context"
	"slices"
	"sort"
	"strings"
)

// The L2 take predicates of search_tracks (C125.3): unheard, in_record, sound and crate_id. Every join to an L2 table is gated
// by whether the schema has it (the D-M3-35 pattern), so the fixtures of earlier schemas still read and the predicates match
// nothing there.

// l2Gates records which L2-era tables the schema lacks.
type l2Gates struct{ records, crates, takeSounds bool }

func (s *Service) l2Gates(ctx context.Context) (l2Gates, error) {
	var g l2Gates
	for _, c := range []struct {
		table string
		gone  *bool
	}{{"records", &g.records}, {"crates", &g.crates}, {"take_sounds", &g.takeSounds}} {
		has, err := s.hasTableDB(ctx, c.table)
		if err != nil {
			return g, err
		}
		*c.gone = !has
	}
	return g, nil
}

// soundFacets are the facets a Take can be selected by (the seven Sound facets, C93).
var soundFacets = []string{"genre_family", "era", "tempo_band", "vocal", "mood", "language", "engine"}

// compileL2 appends the L2 clauses to where.
func compileL2(p SearchParams, where string, args *[]any) (string, error) {
	if p.None {
		where += ` AND 0`
	}
	if p.Unheard != nil {
		neg := ""
		if *p.Unheard {
			neg = "NOT "
		}
		where += ` AND ` + neg + `EXISTS (SELECT 1 FROM listen_events le WHERE le.track_id = t.id AND le.kind = 'start' AND le.principal_kind IN ('human','device'))`
	}
	if p.InRecord != nil {
		switch {
		case p.noL2.records && *p.InRecord:
			where += ` AND 0`
		case p.noL2.records: // nothing is in a Record before the schema has them
		default:
			neg := "NOT "
			if *p.InRecord {
				neg = ""
			}
			where += ` AND ` + neg + `EXISTS (SELECT 1 FROM record_slots rs JOIN records rc ON rc.id = rs.record_id
				LEFT JOIN song_picks sp ON sp.song_id = rs.song_id
				WHERE rs.removed_at IS NULL AND rc.hidden_at IS NULL AND COALESCE(rs.pinned_take_id, sp.take_id) = t.id)`
		}
	}
	if len(p.Sounds) > 0 {
		byFacet := map[string][]string{}
		for _, kv := range p.Sounds {
			facet, value, ok := strings.Cut(kv, ":")
			facet, value = strings.TrimSpace(facet), strings.TrimSpace(value)
			if !ok || value == "" || !slices.Contains(soundFacets, facet) {
				return where, invalid("sound", "want facet:value with a facet of %s", strings.Join(soundFacets, ", "))
			}
			if !slices.Contains(byFacet[facet], value) {
				byFacet[facet] = append(byFacet[facet], value)
			}
		}
		if p.noL2.takeSounds {
			return where + ` AND 0`, nil
		}
		facets := make([]string, 0, len(byFacet))
		for f := range byFacet {
			facets = append(facets, f)
		}
		sort.Strings(facets)
		for _, f := range facets {
			vals := byFacet[f]
			where += ` AND EXISTS (SELECT 1 FROM take_sounds tso WHERE tso.track_id = t.id AND tso.facet = ? AND tso.value IN (` + placeholders(len(vals)) + `))`
			*args = append(*args, f)
			for _, v := range vals {
				*args = append(*args, v)
			}
		}
	}
	if p.CrateID != "" {
		if p.noL2.crates {
			return where + ` AND 0`, nil
		}
		where += ` AND t.id IN (SELECT ci.track_id FROM crate_items ci WHERE ci.crate_id = ? AND ci.track_id IS NOT NULL AND ci.removed_at IS NULL)`
		*args = append(*args, p.CrateID)
	}
	return where, nil
}
