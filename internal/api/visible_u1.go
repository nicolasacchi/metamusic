package api

import (
	"context"
	"net/http"

	"github.com/nicolasacchi/metamusic/internal/view"
)

// visible is the by-id gate of a handler that reads a track (C308.2): 404 NOT_FOUND, indistinguishable from a missing
// id, when the caller hides discarded takes and an id is discarded. An empty id is skipped (the handler's own validation
// reports it). TestTrackHandlersCallVisible pins that every track-reading handler goes through it.
func (d Deps) visible(ctx context.Context, ids ...string) error {
	if d.Library == nil {
		return nil
	}
	for _, id := range ids {
		if id == "" {
			continue
		}
		if err := d.Library.Visible(ctx, id); err != nil {
			return libErr(err)
		}
	}
	return nil
}

// listenerFilterNotAllowed refuses, for a Listener, a filter that is a boolean oracle on hidden data (C308.3b): 422
// FILTER_NOT_ALLOWED naming the filter. set lists the filters the request carries as name -> present.
func listenerFilterNotAllowed(ctx context.Context, set map[string]bool) error {
	if !view.FromContext(ctx).Listener() {
		return nil
	}
	for _, name := range view.ListenerBannedFilters {
		if set[name] {
			return Err(http.StatusUnprocessableEntity, "FILTER_NOT_ALLOWED", "this filter is not available to your role", map[string]any{"filter": name})
		}
	}
	return nil
}

// trackFiltersSet names the search_tracks filters the request carries.
func trackFiltersSet(in *searchTracksIn) map[string]bool {
	return map[string]bool{
		"has_open_comments": in.HasOpenComments.IsSet, "session_id": in.SessionID != "", "origin": len(in.Origin) > 0, "engine": len(in.Engine) > 0,
		"root_track_id": in.RootTrackID != "", "lyrics_of": in.LyricsOf != "", "unheard": in.Unheard.IsSet,
	}
}

// songFiltersSet names the Song filters (list_songs, search_sounds, get_song_atlas) the request carries.
func songFiltersSet(in SongFilterIn) map[string]bool {
	return map[string]bool{
		"muse_id": in.MuseID != "" || in.NoMuse, "angle": in.Angle != "", "session_id": in.SessionID != "", "queued": in.Queued.IsSet,
		"has_open_proposals": in.HasOpenProposals.IsSet, "draft_id": in.DraftID != "", "unheard": in.Unheard.IsSet,
	}
}
