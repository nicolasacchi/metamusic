package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
)

func registerLibrary(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "search_tracks", Method: "GET", Path: "/api/tracks",
		Summary:     "Search tracks",
		Description: "Lists library tracks with full-text search (q), status, style, duration, date, score, truncation, rating (min_stars, unrated), tag, open-comment, recipe_known, lossy, song_id, is_pick, session_id, unheard, in_record, sound and crate_id filters. Each row carries song_id, song_title, song_angle and is_pick. Discarded tracks are hidden unless status asks for them. Pages are capped in size; follow next_cursor.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/library"},
		func(ctx context.Context, in *searchTracksIn) (*trackPageOut, error) {
			if d.Library == nil {
				return nil, unavailable("library")
			}
			params, err := l2TrackParams(*in, library.SearchParams{
				Q: in.Q, Status: in.Status, Style: in.Style, MinDurationS: in.MinDurationS.ptr(), MaxDurationS: in.MaxDurationS.ptr(),
				RecordedFrom: in.RecordedFrom, RecordedTo: in.RecordedTo, HasScore: in.HasScore.ptr(), Truncated: in.Truncated.ptr(),
				RootTrackID: in.RootTrackID, MinStars: in.MinStars.ptr(), Unrated: in.Unrated.ptr() != nil && *in.Unrated.ptr(),
				Tags: in.Tag, HasOpenComments: in.HasOpenComments.ptr(), RecipeKnown: in.RecipeKnown.ptr(), Lossy: in.Lossy.ptr(),
				SongID: in.SongID, IsPick: in.IsPick.ptr(), SessionID: in.SessionID, Sort: in.Sort, Limit: in.Limit, Cursor: in.Cursor,
			})
			if err != nil {
				return nil, err // the L2 predicates answer 501 until lane K
			}
			page, err := d.Library.SearchTracks(ctx, params)
			if err != nil {
				return nil, libErr(err)
			}
			return &trackPageOut{Body: page}, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_library_facets", Method: "GET", Path: "/api/library/facets",
		Summary:     "Get library facets",
		Description: "Top style terms, status counts and duration and recording-date ranges, for building filters.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/library"},
		func(ctx context.Context, _ *empty) (*facetsOut, error) {
			if d.Library == nil {
				return nil, unavailable("library")
			}
			f, err := d.Library.Facets(ctx)
			if err != nil {
				return nil, libErr(err)
			}
			return &facetsOut{Body: f}, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_track", Method: "GET", Path: "/api/tracks/{id}",
		Summary:     "Get a track",
		Description: "Full track detail: audio, recipe, lineage, score, lyric sections (timed only when a score aligns with them) and analyses.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/tracks/:id"},
		func(ctx context.Context, in *trackIDIn) (*trackOut, error) {
			if d.Library == nil {
				return nil, unavailable("library")
			}
			t, err := d.Library.Track(ctx, in.ID)
			if err != nil {
				return nil, libErr(err)
			}
			maskTrack(auth.FromContext(ctx), &t)
			return &trackOut{Body: t}, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_track_peaks", Method: "GET", Path: "/api/tracks/{id}/peaks",
		Summary:     "Get waveform peaks",
		Description: "Waveform peaks of a track (audiowaveform JSON v2 levels): overview (about 2048 columns) or detail (20 per second).",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/tracks/:id"},
		func(ctx context.Context, in *trackPeaksIn) (*peaksOut, error) {
			if d.Library == nil {
				return nil, unavailable("library")
			}
			p, err := d.Library.Peaks(ctx, in.ID, in.Level)
			if err != nil {
				return nil, libErr(err)
			}
			return &peaksOut{Body: p}, nil
		})
}
