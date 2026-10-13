package api

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/playlists"
)

// plErr maps playlist errors to the problem model.
func plErr(err error) error {
	var pe *playlists.Error
	if errors.As(err, &pe) {
		return Err(pe.Status, pe.Code, pe.Msg, pe.Details)
	}
	return libErr(err)
}

// maskPlaylist renders the principal-valued fields of a playlist for viewer (D-M2a-19).
func maskPlaylist(v auth.Principal, p *playlists.Playlist) {
	p.CreatedBy, p.UpdatedBy = KeyFor(v, p.CreatedBy), KeyFor(v, p.UpdatedBy)
}

func registerPlaylists(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_playlists", Method: "GET", Path: "/api/playlists",
		Summary:     "List playlists",
		Description: "Playlists with their track counts and total duration; hidden ones only with include_hidden.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/playlists"},
		func(ctx context.Context, in *includeHiddenIn) (*playlistsOut, error) {
			if d.Playlists == nil {
				return nil, unavailable("playlists")
			}
			items, err := d.Playlists.List(ctx, in.IncludeHidden)
			if err != nil {
				return nil, plErr(err)
			}
			out := &playlistsOut{}
			out.Body.Items = items
			return out, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_playlist", Method: "GET", Path: "/api/playlists/{id}",
		Summary:     "Get a playlist",
		Description: "One playlist with its tracks in order.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/playlists/:id"},
		func(ctx context.Context, in *playlistIDIn) (*playlistOut, error) {
			if d.Playlists == nil {
				return nil, unavailable("playlists")
			}
			pl, err := d.Playlists.Get(ctx, in.ID)
			if err != nil {
				return nil, plErr(err)
			}
			maskPlaylist(auth.FromContext(ctx), &pl)
			return &playlistOut{Body: pl}, nil
		})

	Register(a, withBody(created(huma.Operation{
		OperationID: "create_playlist", Method: "POST", Path: "/api/playlists",
		Summary:     "Create a playlist",
		Description: "Creates a playlist, optionally with an ordered list of track ids (at most 1000).",
	}), bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "/playlists", Idempotency: "create"},
		func(ctx context.Context, in *createPlaylistIn) (*playlistOut, error) {
			if d.Playlists == nil {
				return nil, unavailable("playlists")
			}
			p := auth.FromContext(ctx)
			pl, err := d.Playlists.Create(ctx, p, playlists.NewPlaylist{Name: in.Body.Name, Description: in.Body.Description, TrackIDs: in.Body.TrackIDs})
			if err != nil {
				return nil, plErr(err)
			}
			maskPlaylist(p, &pl)
			return &playlistOut{Body: pl}, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "update_playlist", Method: "PATCH", Path: "/api/playlists/{id}",
		Summary:     "Update a playlist",
		Description: "Renames, hides, replaces (track_ids) or extends (add_track_ids) a playlist under optimistic locking: send the revision you read.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/playlists/:id"},
		func(ctx context.Context, in *updatePlaylistIn) (*playlistOut, error) {
			if d.Playlists == nil {
				return nil, unavailable("playlists")
			}
			p := auth.FromContext(ctx)
			pl, err := d.Playlists.Update(ctx, p, in.ID, playlists.PlaylistPatch{
				Revision: in.Body.Revision, Name: in.Body.Name, Description: in.Body.Description,
				TrackIDs: in.Body.TrackIDs, AddTrackIDs: in.Body.AddTrackIDs, Hidden: in.Body.Hidden,
			})
			if err != nil {
				return nil, plErr(err)
			}
			maskPlaylist(p, &pl)
			return &playlistOut{Body: pl}, nil
		})
}
