package api

import (
	"context"

	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/songs"
)

// The L2 list predicates (C125.3, C130.1) are accepted, mapped onto the Go filter structs of the list operations and
// evaluated in songs/filter.go and library/tracks_l2.go. crate_id is resolved by the Crates service before the list runs
// (expandSongCrate, expandTrackCrate): a manual Crate becomes a membership join, a smart one its merged saved filter.

// l2SongFilter copies the L2 predicates of in onto f and validates them. list_songs and search_sounds share it.
func l2SongFilter(in SongFilterIn, f songs.ListFilter) (songs.ListFilter, error) {
	f.PickDecided, f.Unheard = in.PickDecided.ptr(), in.Unheard.ptr()
	f.MinDurationS, f.MaxDurationS = in.MinDurationS.ptr(), in.MaxDurationS.ptr()
	f.MinStars, f.Unrated = in.MinStars.ptr(), in.Unrated.ptr()
	f.Tags, f.Lossy, f.RecipeKnown, f.InRecord, f.CrateID = in.Tag, in.Lossy.ptr(), in.RecipeKnown.ptr(), in.InRecord.ptr(), in.CrateID
	return f, f.ValidateL2()
}

// l2TrackParams copies the L2 predicates of in onto p (search_tracks).
func l2TrackParams(in searchTracksIn, p library.SearchParams) (library.SearchParams, error) {
	p.Unheard, p.InRecord, p.Sounds, p.CrateID = in.Unheard.ptr(), in.InRecord.ptr(), in.Sound, in.CrateID
	return p, nil
}

// expandSongCrate resolves f.CrateID through the Crates service; a filter without crate_id is returned unchanged.
func expandSongCrate(ctx context.Context, d Deps, f songs.ListFilter) (songs.ListFilter, error) {
	if f.CrateID == "" {
		return f, nil
	}
	if d.Crates == nil {
		return f, unavailable("crates")
	}
	out, err := d.Crates.ExpandSongs(ctx, f.CrateID, f)
	return out, m2bErr(err, taskK)
}

// expandTrackCrate is expandSongCrate for search_tracks.
func expandTrackCrate(ctx context.Context, d Deps, p library.SearchParams) (library.SearchParams, error) {
	if p.CrateID == "" {
		return p, nil
	}
	if d.Crates == nil {
		return p, unavailable("crates")
	}
	out, err := d.Crates.ExpandTracks(ctx, p.CrateID, p)
	return out, m2bErr(err, taskK)
}
