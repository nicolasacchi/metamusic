package sounds

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/songs"
)

// liveWhere restricts a query over songs s to Songs a search shows: live, and not hidden unless asked for.
func liveWhere(f songs.ListFilter) string {
	w := `s.retired_at IS NULL`
	if !f.IncludeHidden {
		w += ` AND s.hidden_at IS NULL`
	}
	return w
}

// Facets counts the seven facets over the Songs matching f (C93.5). The counts of a facet leave that facet's own
// selection out (standard faceted search), so the siblings of a selected value stay visible.
//
// One statement reads the matching Songs with their Sound rows (the non-Sound filters, including the derived-state
// subqueries, are evaluated once); the selections and the counts are applied here, so the cost does not grow with the
// seven facets.
func (s *Service) Facets(ctx context.Context, f songs.ListFilter) (FacetCounts, error) {
	for facet := range f.Sounds {
		if !ValidFacet(facet) {
			return FacetCounts{}, fmt.Errorf("sounds: facets: unknown facet %q", facet)
		}
	}
	v, err := currentVersion(ctx, s.st.R)
	if err != nil {
		return FacetCounts{}, err
	}
	out := FacetCounts{TaxonomyVersion: v, Selected: map[string][]string{}, Facets: map[string][]FacetValueCount{}}
	selected := map[string]map[string]bool{}
	for k, vals := range f.Sounds {
		if len(vals) > 0 {
			out.Selected[k] = append([]string(nil), vals...)
			set := make(map[string]bool, len(vals))
			for _, x := range vals {
				set[x] = true
			}
			selected[k] = set
		}
	}
	base := f
	base.Sounds = nil
	where, args := songs.FilterSQL(base, "")
	// one row per Song with its Sound rows folded into one text (unit/record separators cannot occur in a facet or a
	// value): ten times fewer rows cross the driver boundary than one row per Sound
	//nolint:gosec // G202: fragments are constants of this package and songs.FilterSQL; every value is a bound argument
	rows, err := s.st.R.QueryContext(ctx, `SELECT COALESCE(group_concat(ss.facet || char(31) || ss.value || char(31) || COALESCE(json_extract(ss.detail_json, '$.mismatch') = 1, 0), char(30)), '')
		FROM songs s LEFT JOIN song_sounds ss ON ss.song_id = s.id WHERE `+liveWhere(f)+` AND `+where+` GROUP BY s.id`, args...)
	if err != nil {
		return out, fmt.Errorf("sounds: facets: %w", err)
	}
	defer rows.Close()
	counts := map[facetKey]*FacetValueCount{}
	var mine []facetKey
	var mismatch []bool
	for rows.Next() {
		var folded string
		if err := rows.Scan(&folded); err != nil {
			return out, err
		}
		mine, mismatch = mine[:0], mismatch[:0]
		if folded != "" {
			for _, rec := range strings.Split(folded, "\x1e") {
				parts := strings.Split(rec, "\x1f")
				if len(parts) != 3 {
					return out, fmt.Errorf("sounds: facets: malformed sound record")
				}
				mine, mismatch = append(mine, facetKey{parts[0], parts[1]}), append(mismatch, parts[2] == "1")
			}
		}
		out.SongsMatching += tally(counts, selected, mine, mismatch)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	for _, facet := range Facets {
		vals := []FacetValueCount{}
		for k, c := range counts {
			if k.facet == facet {
				vals = append(vals, *c)
			}
		}
		sort.Slice(vals, func(i, j int) bool {
			if vals[i].Count != vals[j].Count {
				return vals[i].Count > vals[j].Count
			}
			return vals[i].Value < vals[j].Value
		})
		out.Facets[facet] = vals
	}
	return out, nil
}

type facetKey struct{ facet, value string }

// tally adds one Song to the counts and reports whether it matches every selection. A Song counts for a facet when it
// meets every selection except that facet's own, so a Song missing exactly one selection counts for that facet only.
func tally(counts map[facetKey]*FacetValueCount, selected map[string]map[string]bool, mine []facetKey, mismatch []bool) int {
	unsatisfied, lone := 0, ""
	for facet, want := range selected {
		ok := false
		for _, k := range mine {
			if k.facet == facet && want[k.value] {
				ok = true
				break
			}
		}
		if !ok {
			unsatisfied++
			lone = facet
		}
	}
	if unsatisfied > 1 {
		return 0
	}
	for i, k := range mine {
		if unsatisfied == 1 && k.facet != lone {
			continue
		}
		c := counts[k]
		if c == nil {
			c = &FacetValueCount{Value: k.value}
			if k.facet == "tempo_band" {
				c.Mismatches = new(int)
			}
			counts[k] = c
		}
		c.Count++
		if c.Mismatches != nil && mismatch[i] {
			*c.Mismatches++
		}
	}
	if unsatisfied == 0 {
		return 1
	}
	return 0
}
