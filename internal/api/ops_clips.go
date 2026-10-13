package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/media/clip"
)

func registerClips(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_clip", Method: "GET", Path: "/api/tracks/{id}/clip",
		Summary: "Get an audio clip of a track",
		Description: "A signed link to a 30-45 s window of the master as 24-bit/48 kHz FLAC (shorter tracks: the whole track). " +
			"match_lufs attenuates the clip toward a target loudness; clips are never boosted. " +
			"Identical inputs give identical bytes, so the key identifies the clip.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "none:clip links reach the SPA inside duel responses", Rate: "heavy"},
		func(ctx context.Context, in *getClipIn) (*clipLinkOut, error) {
			if d.Clips == nil {
				return nil, unavailable("clips")
			}
			l, err := d.Clips.Link(ctx, auth.FromContext(ctx), in.ID, clip.Query{FromS: in.FromS.ptr(), ToS: in.ToS.ptr(), MatchLUFS: in.MatchLUFS.ptr()})
			if err != nil {
				return nil, m2bErr(err, "M2b-M1")
			}
			return &clipLinkOut{Body: ClipLink{
				TrackID: l.TrackID, URL: l.URL, Key: l.Key, ContentType: l.ContentType, ExpiresAt: rfc3339(l.ExpiresAt), TTLS: l.TTLS,
				Bytes: l.Bytes, SampleRate: l.SampleRate, BitsPerSample: l.BitsPerSample, Channels: l.Channels,
				FromS: l.FromS, ToS: l.ToS, DurationS: l.DurationS, GainDB: l.GainDB,
				WindowLUFS: l.WindowLUFS, WindowTPDBTP: l.WindowTPDBTP, TargetLUFS: l.TargetLUFS, Match: l.Match,
			}}, nil
		})
}
