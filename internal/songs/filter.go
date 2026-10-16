package songs

import (
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Facets are the seven Sound facets a Song can be filtered by (C93): the five taxonomy facets plus the computed
// tempo_band and engine (D-L1-15).
var Facets = []string{"genre_family", "era", "tempo_band", "vocal", "mood", "language", "engine"}

// ParseSoundFilters turns repeated "facet:value" selections into a ListFilter.Sounds map (OR inside a facet, AND
// across facets). An unknown facet or an empty facet/value is 422 UNKNOWN_FACET; the message never echoes the value.
func ParseSoundFilters(sel []string) (map[string][]string, error) {
	if len(sel) == 0 {
		return nil, nil
	}
	out := map[string][]string{}
	for _, s := range sel {
		facet, value, ok := strings.Cut(s, ":")
		facet, value = strings.TrimSpace(facet), strings.TrimSpace(value)
		if !ok || value == "" || !slices.Contains(Facets, facet) {
			return nil, problem.E(http.StatusUnprocessableEntity, "UNKNOWN_FACET", "sound selections are facet:value with a known facet", map[string]any{"facets": Facets})
		}
		if !slices.Contains(out[facet], value) {
			out[facet] = append(out[facet], value)
		}
	}
	return out, nil
}

// stateSub is the uncorrelated Song state table every state-derived clause selects from (C90.4).
const stateSub = `(` + store.SongStateSQL + `) st`

// likeEscape escapes LIKE wildcards so a title substring is literal.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

// FilterSQL renders a ListFilter as a self-contained WHERE fragment over `songs s` with its arguments: it joins nothing,
// every state-derived clause is an uncorrelated subquery, so sounds can reuse it for facet counts (excludeFacet leaves one
// facet's selection out, the standard faceted semantics). Retired Songs never match; hidden and archived Songs match
// only with IncludeHidden / IncludeArchived. Limit and Cursor are not part of it.
func FilterSQL(f ListFilter, excludeFacet string) (where string, args []any) {
	var c []string
	add := func(clause string, a ...any) {
		c = append(c, clause)
		args = append(args, a...)
	}
	c = append(c, "s.retired_at IS NULL")
	if !f.IncludeHidden {
		c = append(c, "s.hidden_at IS NULL")
	}
	if !f.IncludeArchived {
		c = append(c, "s.id NOT IN (SELECT st.song_id FROM "+stateSub+" WHERE st.archived = 1)")
	}
	if f.MuseID != "" {
		add("s.muse_id = ?", f.MuseID)
	}
	if f.NoMuse {
		c = append(c, "s.muse_id IS NULL")
	}
	if f.Angle != "" {
		add("s.angle = ?", f.Angle)
	}
	if len(f.States) > 0 {
		var vals []any
		for _, s := range f.States {
			vals = append(vals, string(s))
		}
		add("s.id IN (SELECT st.song_id FROM "+stateSub+" WHERE "+store.SongStateCase+" IN ("+placeholders(len(vals))+"))", vals...)
	}
	if f.Queued != nil {
		neg := ""
		if !*f.Queued {
			neg = "NOT "
		}
		c = append(c, "s.id "+neg+"IN (SELECT st.song_id FROM "+stateSub+" WHERE st.queued_jobs > 0)")
	}
	if f.SessionID != "" {
		add(`s.id IN (SELECT ts.song_id FROM session_members m JOIN take_songs ts ON ts.track_id = m.member_id
			WHERE m.session_id = ? AND m.member_kind = 'track'
			UNION SELECT s2.id FROM session_members m JOIN songs s2 ON s2.draft_id = m.member_id
			WHERE m.session_id = ? AND m.member_kind = 'draft')`, f.SessionID, f.SessionID)
	}
	facets := make([]string, 0, len(f.Sounds))
	for facet := range f.Sounds {
		facets = append(facets, facet)
	}
	sort.Strings(facets)
	for _, facet := range facets {
		vals := f.Sounds[facet]
		if facet == excludeFacet || len(vals) == 0 {
			continue
		}
		if !slices.Contains(Facets, facet) {
			c = append(c, "0 = 1")
			continue
		}
		a := []any{facet}
		for _, v := range vals {
			a = append(a, v)
		}
		add("EXISTS (SELECT 1 FROM song_sounds ss WHERE ss.song_id = s.id AND ss.facet = ? AND ss.value IN ("+placeholders(len(vals))+"))", a...)
	}
	if f.TempoMismatch != nil {
		neg := "NOT "
		if *f.TempoMismatch {
			neg = ""
		}
		c = append(c, neg+"EXISTS (SELECT 1 FROM song_sounds ss WHERE ss.song_id = s.id AND ss.facet = 'tempo_band' AND json_extract(ss.detail_json, '$.mismatch') IN (1, 'true'))")
	}
	if f.HasPick != nil {
		neg := "NOT "
		if *f.HasPick {
			neg = ""
		}
		c = append(c, neg+"EXISTS (SELECT 1 FROM song_picks p WHERE p.song_id = s.id AND p.take_id IS NOT NULL)")
	}
	if f.HasOpenProposals != nil {
		neg := "NOT "
		if *f.HasOpenProposals {
			neg = ""
		}
		c = append(c, neg+"EXISTS (SELECT 1 FROM pick_proposals pp WHERE pp.song_id = s.id AND pp.state = 'open')")
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		add(`s.title LIKE ? ESCAPE '\'`, "%"+likeEscape(q)+"%")
	}
	if f.DraftID != "" {
		add(`s.id = COALESCE((SELECT id FROM songs WHERE draft_id = ? AND retired_at IS NULL),
			(SELECT r.song_id FROM recipe_drafts d JOIN tracks t ON t.id = d.fulfilled_by_track_id JOIN song_roots r ON r.root_track_id = t.root_track_id WHERE d.id = ?))`, f.DraftID, f.DraftID)
	}
	if f.TrackID != "" {
		add("s.id = (SELECT song_id FROM take_songs WHERE track_id = ?)", f.TrackID)
	}
	return strings.Join(c, " AND "), args
}
