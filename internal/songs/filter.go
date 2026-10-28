package songs

import (
	"fmt"
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
	if f.None {
		c = append(c, "0 = 1")
	}
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
	l2Clauses(f, add)
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

// subjectTake is the SQL expression of a Song's subject take (C125.3): its Pick take, else its newest eligible take
// (D-L1-14's representative without the draft fallback). NULL for a Song with no take.
const subjectTake = `COALESCE((SELECT p.take_id FROM song_picks p WHERE p.song_id = s.id),
	(SELECT t.id FROM take_songs ts JOIN tracks t ON t.id = ts.track_id WHERE ts.song_id = s.id AND t.status <> 'discarded'
	 ORDER BY t.recorded_at DESC, t.created_at DESC, t.id DESC LIMIT 1))`

// normTag is the filter-side tag normalisation (lower case, single spaces); stored names already satisfy the full rule.
func normTag(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ValidateL2 checks the L2 predicates of f (C125.3): the star bounds, the exclusive pair and the tag count. A nil error means
// FilterSQL can render them.
func (f ListFilter) ValidateL2() error {
	bad := func(field, msg string) error {
		return problem.E(http.StatusUnprocessableEntity, "VALIDATION_FAILED", msg, map[string]any{"field": field})
	}
	if f.MinStars != nil && (*f.MinStars < 1 || *f.MinStars > 5) {
		return bad("min_stars", "must be 1..5")
	}
	if f.MinStars != nil && f.Unrated != nil && *f.Unrated {
		return bad("unrated", "cannot be combined with min_stars")
	}
	if len(f.Tags) > 5 {
		return bad("tag", "at most 5 tags")
	}
	if f.MinDurationS != nil && *f.MinDurationS < 0 {
		return bad("min_duration_s", "must not be negative")
	}
	if f.MaxDurationS != nil && *f.MaxDurationS < 0 {
		return bad("max_duration_s", "must not be negative")
	}
	return nil
}

// l2Clauses renders the L2 predicates of C125.3. The take predicates (duration, stars, tags, lossy, recipe_known) are one
// EXISTS over the Song's subject take, so a Song with no take never matches any of them.
func l2Clauses(f ListFilter, add func(string, ...any)) {
	if f.PickDecided != nil {
		neg := "NOT "
		if *f.PickDecided {
			neg = ""
		}
		add(neg + "EXISTS (SELECT 1 FROM song_picks p WHERE p.song_id = s.id AND p.source IN ('pinned','ladder'))")
	}
	if f.Unheard != nil {
		neg := ""
		if *f.Unheard {
			neg = "NOT "
		}
		add(neg + `EXISTS (SELECT 1 FROM take_songs ts JOIN listen_events le ON le.track_id = ts.track_id
			WHERE ts.song_id = s.id AND le.kind = 'start' AND le.principal_kind IN ('human','device'))`)
	}
	var cond []string
	var args []any
	var joins string
	if f.MinDurationS != nil {
		cond, args = append(cond, "t.duration_s >= ?"), append(args, *f.MinDurationS)
	}
	if f.MaxDurationS != nil {
		cond, args = append(cond, "t.duration_s <= ?"), append(args, *f.MaxDurationS)
	}
	if f.MinStars != nil || f.Unrated != nil {
		joins += " LEFT JOIN ratings hs ON hs.track_id = t.id AND hs.source = 'human'"
	}
	if f.MinStars != nil {
		cond, args = append(cond, "hs.stars >= ?"), append(args, *f.MinStars)
	}
	if f.Unrated != nil {
		if *f.Unrated {
			cond = append(cond, "hs.stars IS NULL")
		} else {
			cond = append(cond, "hs.stars IS NOT NULL")
		}
	}
	for _, tag := range f.Tags {
		cond = append(cond, "EXISTS (SELECT 1 FROM track_tags tt JOIN tags g ON g.id = tt.tag_id WHERE tt.track_id = t.id AND g.name = ? AND tt.removed_at IS NULL)")
		args = append(args, normTag(tag))
	}
	if f.Lossy != nil {
		cond, args = append(cond, "t.lossy = ?"), append(args, b2i(*f.Lossy))
	}
	if f.RecipeKnown != nil {
		joins += " JOIN recipes r ON r.id = t.recipe_id"
		cond, args = append(cond, "(r.engine <> 'unknown') = ?"), append(args, b2i(*f.RecipeKnown))
	}
	if len(cond) > 0 {
		add(fmt.Sprintf(`EXISTS (SELECT 1 FROM tracks t%s WHERE t.id = %s AND %s)`, joins, subjectTake, strings.Join(cond, " AND ")), args...)
	}
	if f.InRecord != nil {
		neg := "NOT "
		if *f.InRecord {
			neg = ""
		}
		add(neg + `EXISTS (SELECT 1 FROM record_slots rs JOIN records rc ON rc.id = rs.record_id
			WHERE rs.song_id = s.id AND rs.removed_at IS NULL AND rc.hidden_at IS NULL)`)
	}
	if f.CrateID != "" {
		// A member that was retired is followed to the Song it was merged into (C125.3); retired Songs never match themselves.
		add(`s.id IN (WITH RECURSIVE m(id, hop) AS (
			SELECT ci.song_id, 0 FROM crate_items ci WHERE ci.crate_id = ? AND ci.song_id IS NOT NULL AND ci.removed_at IS NULL
			UNION SELECT so.retired_into, m.hop + 1 FROM m JOIN songs so ON so.id = m.id WHERE so.retired_into IS NOT NULL AND m.hop < 16)
			SELECT id FROM m)`, f.CrateID)
	}
}
