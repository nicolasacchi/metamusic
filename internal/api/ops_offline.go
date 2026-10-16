package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/playlists"
)

// registerOfflineM4a registers get_offline_set (C111, C113).
func registerOfflineM4a(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_offline_set", Method: "GET", Path: "/api/offline-set",
		Summary:     "Get the offline set",
		Description: "The protected offline-set playlist with per-item media facts (FLAC and MP3 sizes and hashes), so a device can plan downloads without further calls. Edit it with update_playlist, sending the revision from this response.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/offline"},
		func(ctx context.Context, _ *empty) (*offlineSetOut, error) {
			if d.Playlists == nil {
				return nil, unavailable("playlists")
			}
			pl, err := d.Playlists.Get(ctx, playlists.OfflineSetID)
			if err != nil {
				return nil, plErr(err)
			}
			ids := make([]string, len(pl.Items))
			for i, it := range pl.Items {
				ids[i] = it.Track.ID
			}
			facts, err := d.Playlists.OfflineFacts(ctx, ids)
			if err != nil {
				return nil, err
			}
			set := OfflineSet{PlaylistID: pl.ID, Name: pl.Name, Revision: pl.Revision, UpdatedAt: pl.UpdatedAt,
				LimitItems: playlists.OfflineMaxItems, Items: []OfflineItem{}}
			for i, id := range ids {
				f, ok := facts[id]
				if !ok {
					continue // cannot happen (items are checked tracks); never invent facts
				}
				it := OfflineItem{Position: i + 1, TrackID: id, Title: f.Title, Album: f.Album, DurationS: f.DurationS, Status: f.Status, Lossy: f.Lossy}
				if f.Flac != nil {
					it.Flac = &OfflineFlacOrNull{SHA256: f.Flac.SHA256, Bytes: f.Flac.Bytes, SampleRate: f.Flac.SampleRate, BitsPerSample: f.Flac.BitsPerSample}
					set.Totals.FlacBytes += f.Flac.Bytes
				}
				if f.MP3 != nil {
					it.MP3 = &OfflineMP3OrNull{SHA256: f.MP3.SHA256, Bytes: f.MP3.Bytes}
					set.Totals.MP3Bytes += f.MP3.Bytes
				}
				set.Items = append(set.Items, it)
			}
			set.Totals.Items = len(set.Items)
			return &offlineSetOut{Body: set}, nil
		})
}
