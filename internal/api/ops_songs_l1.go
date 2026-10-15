package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/songview"
)

// registerSongsL1 registers the Song operations (C96).
func registerSongsL1(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_songs", Method: "GET", Path: "/api/songs",
		Summary:     "List Songs",
		Description: "Songs (one idea each, however many takes were rendered) filtered by Muse, angle, state, session, Sound facets, draft or take; draft_id and track_id return the single Song of that draft or take.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "none:agent-face (the Sound browser lists Songs through search_sounds)"},
		func(ctx context.Context, in *listSongsIn) (*songsOut, error) {
			if d.Songs == nil {
				return nil, unavailable("songs")
			}
			f, err := songFilter(in.songFilterIn)
			if err != nil {
				return nil, m2bErr(err, "L1-B5")
			}
			f.Limit, f.Cursor = in.Limit, in.Cursor
			p, err := d.Songs.List(ctx, f)
			if err != nil {
				return nil, m2bErr(err, "L1-B5")
			}
			out := &songsOut{}
			out.Body.Items = make([]SongSummary, len(p.Items))
			for i, it := range p.Items {
				out.Body.Items[i] = songSummaryWire(it)
			}
			out.Body.NextCursor, out.Body.Total = p.NextCursor, p.Total
			return out, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_song", Method: "GET", Path: "/api/songs/{id}",
		Summary:     "Get a Song",
		Description: "One Song with its Muse, takes, current Pick and proposals, recent Pick events, Sound facets and sessions. With by=draft or by=track the id is a draft or take id. A retired Song answers 200 with retired_into.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/songs/:id"},
		func(ctx context.Context, in *getSongIn) (*songViewOut, error) {
			if d.SongView == nil {
				return nil, unavailable("songview")
			}
			p := auth.FromContext(ctx)
			v, err := d.SongView.Get(ctx, p, in.ID, songview.GetOptions{By: in.By, TakeLimit: in.TakeLimit, IncludeEvents: in.IncludeEvents})
			if err != nil {
				return nil, m2bErr(err, "L1-B5")
			}
			return &songViewOut{Body: songViewWire(p, v)}, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "update_song", Method: "PATCH", Path: "/api/songs/{id}",
		Summary:     "Update a Song",
		Description: "Renames a Song, sets or clears its angle and Muse, or hides it, under optimistic locking: send the revision you read. A cleared angle is never refilled by the backfill.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/songs/:id"},
		func(ctx context.Context, in *updateSongIn) (*songCoreOut, error) {
			if d.Songs == nil {
				return nil, unavailable("songs")
			}
			oc := func(o OptString) songs.OptString { return songs.OptString{Set: o.Set, Value: o.Value} }
			if _, err := d.Songs.Update(ctx, auth.FromContext(ctx), in.ID, songs.Patch{Revision: in.Body.Revision, Title: in.Body.Title,
				Angle: oc(in.Body.Angle), MuseID: oc(in.Body.MuseID), Hidden: in.Body.Hidden}); err != nil {
				return nil, m2bErr(err, "L1-B5")
			}
			c, err := d.Songs.Core(ctx, in.ID)
			if err != nil {
				return nil, m2bErr(err, "L1-B5")
			}
			return &songCoreOut{Body: songCoreWire(c)}, nil
		})
}
