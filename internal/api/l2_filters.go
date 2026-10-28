package api

import (
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/songs"
)

// The L2 list predicates (C125.3, C130.1) are accepted, mapped onto the Go filter structs of the list operations and
// refused with 501 NOT_IMPLEMENTED until lane K evaluates them in songs/filter.go and library/tracks_l2.go. Lane K deletes
// the `if ... { return ..., notImplemented(...) }` guards below and nothing else here: the mapping is final, so the
// operation's schema and these functions never change after the freeze.

// l2SongFilter copies the L2 predicates of in onto f. list_songs and search_sounds share it.
func l2SongFilter(in SongFilterIn, f songs.ListFilter) (songs.ListFilter, error) {
	f.PickDecided, f.Unheard = in.PickDecided.ptr(), in.Unheard.ptr()
	f.MinDurationS, f.MaxDurationS = in.MinDurationS.ptr(), in.MaxDurationS.ptr()
	f.MinStars, f.Unrated = in.MinStars.ptr(), in.Unrated.ptr()
	f.Tags, f.Lossy, f.RecipeKnown, f.InRecord, f.CrateID = in.Tag, in.Lossy.ptr(), in.RecipeKnown.ptr(), in.InRecord.ptr(), in.CrateID
	if f.PickDecided != nil || f.Unheard != nil || f.MinDurationS != nil || f.MaxDurationS != nil || f.MinStars != nil || f.Unrated != nil ||
		len(f.Tags) > 0 || f.Lossy != nil || f.RecipeKnown != nil || f.InRecord != nil || f.CrateID != "" {
		return f, notImplemented(taskK) // STUB(L2-K)
	}
	return f, nil
}

// l2TrackParams copies the L2 predicates of in onto p (search_tracks).
func l2TrackParams(in searchTracksIn, p library.SearchParams) (library.SearchParams, error) {
	p.Unheard, p.InRecord, p.Sounds, p.CrateID = in.Unheard.ptr(), in.InRecord.ptr(), in.Sound, in.CrateID
	if p.Unheard != nil || p.InRecord != nil || len(p.Sounds) > 0 || p.CrateID != "" {
		return p, notImplemented(taskK) // STUB(L2-K)
	}
	return p, nil
}
