package songs

import (
	"sort"
	"strings"
)

// SoundClauses renders the Sound part of a ListFilter as a WHERE fragment over songs s with its arguments (C93.5,
// C90.7): OR inside a facet, AND across facets, one EXISTS over song_sounds per selected facet, plus TempoMismatch.
// excludeFacet leaves one facet's own selection out, which is how faceted counts keep a selected value's siblings
// visible. The fragment is "1 = 1" when nothing applies.
func SoundClauses(f ListFilter, excludeFacet string) (where string, args []any) {
	facets := make([]string, 0, len(f.Sounds))
	for facet, vals := range f.Sounds {
		if facet != excludeFacet && len(vals) > 0 {
			facets = append(facets, facet)
		}
	}
	sort.Strings(facets)
	parts := []string{"1 = 1"}
	for _, facet := range facets {
		vals := f.Sounds[facet]
		parts = append(parts, `EXISTS (SELECT 1 FROM song_sounds ss WHERE ss.song_id = s.id AND ss.facet = ? AND ss.value IN (`+
			strings.TrimSuffix(strings.Repeat("?,", len(vals)), ",")+`))`)
		args = append(args, facet)
		for _, v := range vals {
			args = append(args, v)
		}
	}
	if f.TempoMismatch != nil {
		not := ""
		if !*f.TempoMismatch {
			not = "NOT "
		}
		parts = append(parts, not+`EXISTS (SELECT 1 FROM song_sounds ss WHERE ss.song_id = s.id AND ss.facet = 'tempo_band' AND json_extract(ss.detail_json, '$.mismatch') = 1)`)
	}
	return strings.Join(parts, " AND "), args
}
