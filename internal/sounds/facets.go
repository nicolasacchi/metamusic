package sounds

import (
	"context"
	"fmt"

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
	for k, vals := range f.Sounds {
		if len(vals) > 0 {
			out.Selected[k] = append([]string(nil), vals...)
		}
	}
	base := liveWhere(f)
	where, args := songs.FilterSQL(f, "")
	if err := s.st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM songs s WHERE `+base+` AND `+where, args...).Scan(&out.SongsMatching); err != nil {
		return out, fmt.Errorf("sounds: songs matching: %w", err)
	}
	for _, facet := range Facets {
		where, args := songs.FilterSQL(f, facet)
		// (song_id, facet, value) is unique in song_sounds, so COUNT(*) counts Songs; only tempo_band reads the detail.
		mm := `0`
		if facet == "tempo_band" {
			mm = `COALESCE(SUM(json_extract(ss.detail_json, '$.mismatch') = 1), 0)`
		}
		//nolint:gosec // the fragments are constants of this package and songs.FilterSQL; every value is a bound argument
		q := `SELECT ss.value, COUNT(*) AS c, ` + mm + `
			FROM song_sounds ss JOIN songs s ON s.id = ss.song_id
			WHERE ss.facet = ? AND ` + base + ` AND ` + where + ` GROUP BY ss.value ORDER BY c DESC, ss.value`
		rows, err := s.st.R.QueryContext(ctx, q, append([]any{facet}, args...)...)
		if err != nil {
			return out, fmt.Errorf("sounds: facet %s: %w", facet, err)
		}
		vals := []FacetValueCount{}
		for rows.Next() {
			var c FacetValueCount
			var mm int
			if err := rows.Scan(&c.Value, &c.Count, &mm); err != nil {
				_ = rows.Close()
				return out, err
			}
			if facet == "tempo_band" {
				c.Mismatches = &mm
			}
			vals = append(vals, c)
		}
		if err := rows.Close(); err != nil {
			return out, err
		}
		out.Facets[facet] = vals
	}
	return out, nil
}
