package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/export"
	"github.com/nicolasacchi/metamusic/internal/exports"
)

// exportErr maps export errors to the problem model.
func exportErr(err error) error {
	var ee *exports.Error
	if errors.As(err, &ee) {
		return Err(ee.Status, ee.Code, ee.Msg, ee.Details)
	}
	return plErr(err)
}

// selectionOf turns the request body of export_jsonl into an export selection.
func selectionOf(in *exportJSONLIn) (export.Selection, error) {
	b := in.Body
	sel := export.Selection{Kind: b.Selection}
	bad := func(field, msg string) error {
		return Err(http.StatusUnprocessableEntity, "VALIDATION_FAILED", msg, map[string]any{"field": field})
	}
	switch b.Selection {
	case "source":
		if b.SourcePath == "" {
			return sel, bad("source_path", "source_path is required for selection source")
		}
		if b.Tracks != nil || b.Drafts != nil {
			return sel, bad("selection", "tracks and drafts do not apply to selection source")
		}
		sel.SourcePath, sel.ForBatch = b.SourcePath, b.ForBatch // tracks and drafts always have unique ids
	case "tracks":
		if b.SourcePath != "" || b.Drafts != nil {
			return sel, bad("selection", "source_path and drafts do not apply to selection tracks")
		}
		if t := b.Tracks; t != nil {
			sel.Tracks = export.TrackFilter{Status: t.Status, MinStars: t.MinStars, Tags: t.Tags, PlaylistID: t.PlaylistID, TrackIDs: t.TrackIDs}
		}
	case "drafts":
		if b.SourcePath != "" || b.Tracks != nil {
			return sel, bad("selection", "source_path and tracks do not apply to selection drafts")
		}
		if x := b.Drafts; x != nil {
			sel.Drafts = export.DraftFilter{Status: x.Status, Collection: x.Collection, Origin: x.Origin, GroupKey: x.GroupKey, DraftIDs: x.DraftIDs}
		}
	}
	return sel, nil
}

func registerExports(a huma.API, d Deps) {
	Register(a, created(huma.Operation{
		OperationID: "export_playlist_zip", Method: "POST", Path: "/api/playlists/{id}/exports",
		Summary:     "Export a playlist as a ZIP",
		Description: "Builds a ZIP of the playlist's MP3 files with ID3 tags (at most 200 tracks, 2 GiB) and returns a signed download link.",
	}), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/playlists/:id", Idempotency: "create", Rate: "heavy"},
		func(ctx context.Context, in *exportPlaylistIn) (*exportOut, error) {
			if d.Exports == nil {
				return nil, unavailable("exports")
			}
			x, err := d.Exports.PlaylistZIP(ctx, auth.FromContext(ctx), in.ID)
			if err != nil {
				return nil, exportErr(err)
			}
			return &exportOut{Body: x}, nil
		})

	Register(a, huma.Operation{
		OperationID: "list_export_sources", Method: "GET", Path: "/api/exports/sources",
		Summary:     "List exportable source files",
		Description: "The imported songs.jsonl paths that export_jsonl can reproduce byte for byte, with line and recipe counts.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/export"},
		func(ctx context.Context, _ *empty) (*exportSourcesOut, error) {
			if d.Exports == nil {
				return nil, unavailable("exports")
			}
			items, err := d.Exports.Sources(ctx)
			if err != nil {
				return nil, exportErr(err)
			}
			out := &exportSourcesOut{}
			out.Body.Items = items
			return out, nil
		})

	Register(a, withBody(created(huma.Operation{
		OperationID: "export_jsonl", Method: "POST", Path: "/api/exports/jsonl",
		Summary:     "Export songs.jsonl",
		Description: "Exports an imported source file, a track selection or drafts as a songs.jsonl that yue2 batch accepts, with the validator result and a signed download link. Small files are also returned inline.",
	}), bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "tool", UI: "/export", Idempotency: "create", Rate: "heavy"},
		func(ctx context.Context, in *exportJSONLIn) (*exportOut, error) {
			if d.Exports == nil {
				return nil, unavailable("exports")
			}
			sel, err := selectionOf(in)
			if err != nil {
				return nil, err
			}
			x, err := d.Exports.JSONL(ctx, auth.FromContext(ctx), sel, in.Body.Filename)
			if err != nil {
				return nil, exportErr(err)
			}
			return &exportOut{Body: x}, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_export", Method: "GET", Path: "/api/exports/{id}",
		Summary:     "Get an export",
		Description: "An earlier export with a fresh download link; 410 once the file was swept (create a new export).",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/export"},
		func(ctx context.Context, in *exportIDIn) (*exportOut, error) {
			if d.Exports == nil {
				return nil, unavailable("exports")
			}
			x, err := d.Exports.Get(ctx, auth.FromContext(ctx), in.ID)
			if err != nil {
				return nil, exportErr(err)
			}
			return &exportOut{Body: x}, nil
		})
}
