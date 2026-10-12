package api

import (
	"context"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
)

func (d Deps) clock() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func registerMedia(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_media_links", Method: "GET", Path: "/api/tracks/{id}/media",
		Summary:     "Get signed media links",
		Description: "Signed, expiring URLs for the lossless FLAC master and the MP3 rendition of a track. Humans get 12 h links, agents 15 min.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "/tracks/:id"},
		func(ctx context.Context, in *trackIDIn) (*mediaLinksOut, error) {
			if d.Library == nil {
				return nil, unavailable("library")
			}
			if d.Signer == nil {
				return nil, unavailable("signer")
			}
			m, err := d.Library.Media(ctx, in.ID)
			if err != nil {
				return nil, libErr(err)
			}
			ttl, bucket := serve.TTLFor(auth.FromContext(ctx))
			fl := d.Signer.Sign(m.Flac.SHA256, "flac", ttl, bucket)
			ml := d.Signer.Sign(m.MP3.SHA256, "mp3", ttl, bucket)
			out := &mediaLinksOut{}
			b := &out.Body
			b.TrackID, b.TTLS = m.TrackID, int64(ttl.Seconds())
			exp := fl.Expires
			if ml.Expires.Before(exp) {
				exp = ml.Expires
			}
			b.ExpiresAt = exp.UTC().Format(time.RFC3339)
			b.Flac = FlacLink{URL: fl.URL, ContentType: m.Flac.ContentType, Bytes: m.Flac.Bytes, SHA256: m.Flac.SHA256,
				SampleRate: m.Flac.SampleRate, BitsPerSample: m.Flac.BitsPerSample}
			b.MP3 = MP3Link{URL: ml.URL, ContentType: m.MP3.ContentType, Bytes: m.MP3.Bytes, SHA256: m.MP3.SHA256, BitrateKbps: m.MP3.BitrateKbps}
			return out, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_device_probe", Method: "GET", Path: "/api/device-probe",
		Summary:     "Get the device FLAC probe",
		Description: "A signed link to a 3 s 24-bit/48 kHz FLAC test file and the expectations a client checks to learn whether the device decodes and seeks FLAC.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/"},
		func(ctx context.Context, _ *empty) (*deviceProbeOut, error) {
			if d.Signer == nil {
				return nil, unavailable("signer")
			}
			if d.Probe == nil {
				return nil, unavailable("probe")
			}
			ttl, bucket := serve.TTLFor(auth.FromContext(ctx))
			l := d.Signer.Sign(d.Probe.ETag, "flac", ttl, bucket)
			out := &deviceProbeOut{}
			b := &out.Body
			b.Version, b.URL, b.ContentType = 1, l.URL, "audio/flac"
			b.SampleRate, b.BitsPerSample, b.Channels = 48000, 24, 2
			b.DurationS, b.SeekToS = 3.0, 1.5
			b.ExpiresAt = l.Expires.UTC().Format(time.RFC3339)
			return out, nil
		})
}
