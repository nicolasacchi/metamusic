package crates

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"

	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/songs"
)

func conflict(field string) error {
	return problem.E(http.StatusConflict, "FILTER_CONFLICT", "the request sets a field the Crate's saved filter sets to another value", map[string]any{"field": field})
}

// mergeScalar merges one optional scalar: set in only one side is kept, equal values are kept, different values conflict.
func mergeScalar[T comparable](field string, a, b *T) (*T, error) {
	switch {
	case a == nil:
		return b, nil
	case b == nil || *a == *b:
		return a, nil
	}
	return nil, conflict(field)
}

// mergeString is mergeScalar for a string where "" is unset.
func mergeString(field string, a, b string) (string, error) {
	switch {
	case a == "":
		return b, nil
	case b == "" || a == b:
		return a, nil
	}
	return "", conflict(field)
}

// mergeList intersects two lists (AND semantics); an empty side is unset. none reports an empty intersection of two set lists.
func mergeList[T comparable](a, b []T) (out []T, none bool) {
	switch {
	case len(a) == 0:
		return b, false
	case len(b) == 0:
		return a, false
	}
	for _, x := range a {
		if slices.Contains(b, x) && !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out, len(out) == 0
}

// mergeSounds intersects the value lists of facets both sides select (OR inside a facet) and keeps the facets only one side
// selects (AND across facets).
func mergeSounds(a, b map[string][]string) (out map[string][]string, none bool) {
	if len(a) == 0 {
		return b, false
	}
	if len(b) == 0 {
		return a, false
	}
	out = map[string][]string{}
	for facet, av := range a {
		if bv, ok := b[facet]; ok {
			vals, empty := mergeList(av, bv)
			if empty {
				none = true
			}
			out[facet] = vals
		} else {
			out[facet] = av
		}
	}
	for facet, bv := range b {
		if _, ok := a[facet]; !ok {
			out[facet] = bv
		}
	}
	return out, none
}

func flattenSounds(m map[string][]string) []string {
	var out []string
	facets := make([]string, 0, len(m))
	for f := range m {
		facets = append(facets, f)
	}
	sort.Strings(facets)
	for _, f := range facets {
		for _, v := range m[f] {
			out = append(out, f+":"+v)
		}
	}
	return out
}

// crateFor loads the Crate an expansion names and checks its subject.
func (s *Service) crateFor(ctx context.Context, id string, subject Subject) (Crate, error) {
	c, err := s.load(ctx, s.st.R, id)
	if err != nil {
		return Crate{}, err
	}
	if c.Subject != subject {
		return Crate{}, problem.E(http.StatusUnprocessableEntity, "SUBJECT_MISMATCH", "the Crate holds "+string(c.Subject)+"s, not "+string(subject)+"s", map[string]any{"subject": string(c.Subject)})
	}
	return c, nil
}

// ExpandSongs merges a Crate into a Song list filter: a membership join for a manual Crate, the stored filter (AND, with
// FILTER_CONFLICT on a clash, list fields intersected) for a smart one. A hidden Crate is usable.
func (s *Service) ExpandSongs(ctx context.Context, crateID string, base songs.ListFilter) (songs.ListFilter, error) {
	c, err := s.crateFor(ctx, crateID, SubjectSong)
	if err != nil {
		return base, err
	}
	if c.Kind == KindManual {
		base.CrateID = crateID
		return base, nil
	}
	pf, err := ParseFilter(SubjectSong, c.Filter)
	if err != nil {
		return base, fmt.Errorf("crates: stored filter of %s: %w", crateID, err)
	}
	return mergeSongs(base, pf.Songs())
}

func mergeSongs(base, f songs.ListFilter) (out songs.ListFilter, err error) {
	out = base
	out.CrateID = ""
	if out.MuseID, err = mergeString("muse_id", base.MuseID, f.MuseID); err != nil {
		return base, err
	}
	out.NoMuse = base.NoMuse || f.NoMuse
	if out.Angle, err = mergeString("angle", base.Angle, f.Angle); err != nil {
		return base, err
	}
	var none bool
	out.States, none = mergeList(base.States, f.States)
	out.None = out.None || none
	if out.SessionID, err = mergeString("session_id", base.SessionID, f.SessionID); err != nil {
		return base, err
	}
	out.Sounds, none = mergeSounds(base.Sounds, f.Sounds)
	out.None = out.None || none
	out.Tags, none = mergeList(base.Tags, f.Tags)
	out.None = out.None || none
	if out.Q, err = mergeString("q", base.Q, f.Q); err != nil {
		return base, err
	}
	for _, m := range []struct {
		field string
		dst   **bool
		a, b  *bool
	}{
		{"queued", &out.Queued, base.Queued, f.Queued}, {"tempo_mismatch", &out.TempoMismatch, base.TempoMismatch, f.TempoMismatch},
		{"has_pick", &out.HasPick, base.HasPick, f.HasPick}, {"has_open_proposals", &out.HasOpenProposals, base.HasOpenProposals, f.HasOpenProposals},
		{"pick_decided", &out.PickDecided, base.PickDecided, f.PickDecided}, {"unheard", &out.Unheard, base.Unheard, f.Unheard},
		{"unrated", &out.Unrated, base.Unrated, f.Unrated}, {"lossy", &out.Lossy, base.Lossy, f.Lossy},
		{"recipe_known", &out.RecipeKnown, base.RecipeKnown, f.RecipeKnown}, {"in_record", &out.InRecord, base.InRecord, f.InRecord},
	} {
		if *m.dst, err = mergeScalar(m.field, m.a, m.b); err != nil {
			return base, err
		}
	}
	if out.MinDurationS, err = mergeScalar("min_duration_s", base.MinDurationS, f.MinDurationS); err != nil {
		return base, err
	}
	if out.MaxDurationS, err = mergeScalar("max_duration_s", base.MaxDurationS, f.MaxDurationS); err != nil {
		return base, err
	}
	if out.MinStars, err = mergeScalar("min_stars", base.MinStars, f.MinStars); err != nil {
		return base, err
	}
	return out, out.ValidateL2()
}

// ExpandTracks is ExpandSongs for Take searches.
func (s *Service) ExpandTracks(ctx context.Context, crateID string, base library.SearchParams) (library.SearchParams, error) {
	c, err := s.crateFor(ctx, crateID, SubjectTake)
	if err != nil {
		return base, err
	}
	if c.Kind == KindManual {
		base.CrateID = crateID
		return base, nil
	}
	pf, err := ParseFilter(SubjectTake, c.Filter)
	if err != nil {
		return base, fmt.Errorf("crates: stored filter of %s: %w", crateID, err)
	}
	return mergeTracks(base, pf.Tracks())
}

func mergeTracks(base, f library.SearchParams) (out library.SearchParams, err error) {
	out = base
	out.CrateID = ""
	if out.Q, err = mergeString("q", base.Q, f.Q); err != nil {
		return base, err
	}
	var none bool
	for _, m := range []struct {
		dst  *[]string
		a, b []string
	}{{&out.Status, base.Status, f.Status}, {&out.Style, base.Style, f.Style}, {&out.Tags, base.Tags, f.Tags}, {&out.Origin, base.Origin, f.Origin}} {
		*m.dst, none = mergeList(m.a, m.b)
		out.None = out.None || none
	}
	// sound selections are facet:value pairs; intersect per facet like the Song filter does
	am, _ := songs.ParseSoundFilters(base.Sounds)
	bm, _ := songs.ParseSoundFilters(f.Sounds)
	merged, none := mergeSounds(am, bm)
	out.Sounds, out.None = flattenSounds(merged), out.None || none
	if out.RecordedFrom, err = mergeString("recorded_from", base.RecordedFrom, f.RecordedFrom); err != nil {
		return base, err
	}
	if out.RecordedTo, err = mergeString("recorded_to", base.RecordedTo, f.RecordedTo); err != nil {
		return base, err
	}
	if out.SessionID, err = mergeString("session_id", base.SessionID, f.SessionID); err != nil {
		return base, err
	}
	out.Unrated = base.Unrated || f.Unrated
	for _, m := range []struct {
		field string
		dst   **bool
		a, b  *bool
	}{
		{"has_score", &out.HasScore, base.HasScore, f.HasScore}, {"truncated", &out.Truncated, base.Truncated, f.Truncated},
		{"has_open_comments", &out.HasOpenComments, base.HasOpenComments, f.HasOpenComments}, {"recipe_known", &out.RecipeKnown, base.RecipeKnown, f.RecipeKnown},
		{"lossy", &out.Lossy, base.Lossy, f.Lossy}, {"is_pick", &out.IsPick, base.IsPick, f.IsPick},
		{"unheard", &out.Unheard, base.Unheard, f.Unheard}, {"in_record", &out.InRecord, base.InRecord, f.InRecord},
	} {
		if *m.dst, err = mergeScalar(m.field, m.a, m.b); err != nil {
			return base, err
		}
	}
	if out.MinDurationS, err = mergeScalar("min_duration_s", base.MinDurationS, f.MinDurationS); err != nil {
		return base, err
	}
	if out.MaxDurationS, err = mergeScalar("max_duration_s", base.MaxDurationS, f.MaxDurationS); err != nil {
		return base, err
	}
	if out.MinStars, err = mergeScalar("min_stars", base.MinStars, f.MinStars); err != nil {
		return base, err
	}
	if out.MinStars != nil && out.Unrated {
		return base, problem.E(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "unrated cannot be combined with min_stars", map[string]any{"field": "unrated"})
	}
	return out, nil
}
