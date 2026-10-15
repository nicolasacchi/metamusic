package api

import (
	"context"
	"log/slog"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/redact"
)

func (d Deps) clock() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// coverLinks signs the 512 and 1024 px covers of a track (C110.1). nil while no cover service is wired or when
// the cover cannot be produced: the audio links never depend on it.
func (d Deps) coverLinks(ctx context.Context, trackID string, ttl, bucket time.Duration) *CoverLinksOrNull {
	if d.Covers == nil {
		return nil
	}
	var urls [2]string
	var exp time.Time
	for i, size := range [2]int{512, 1024} {
		_, key, err := d.Covers.EnsureTrack(ctx, trackID, size)
		if err != nil {
			slog.Warn("cover unavailable", "err", redact.Error(err))
			return nil
		}
		l := d.Signer.Sign(key, "png", ttl, bucket)
		urls[i] = l.URL
		if i == 0 || l.Expires.Before(exp) {
			exp = l.Expires
		}
	}
	return &CoverLinksOrNull{URL512: urls[0], URL1024: urls[1], ExpiresAt: exp.UTC().Format(time.RFC3339)}
}

func registerMedia(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_media_links", Method: "GET", Path: "/api/tracks/{id}/media",
		Summary:     "Get signed media links",
		Description: "Signed, expiring URLs for the lossless FLAC master and the MP3 rendition of a track. For a lossy reference track flac is null and mp3 is the master itself (lossy true). Humans get 12 h links, agents 15 min.",
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
			ml := d.Signer.Sign(m.MP3.SHA256, "mp3", ttl, bucket)
			out := &mediaLinksOut{}
			b := &out.Body
			b.TrackID, b.TTLS, b.Lossy = m.TrackID, int64(ttl.Seconds()), m.Lossy
			exp := ml.Expires
			if m.Flac != nil {
				fl := d.Signer.Sign(m.Flac.SHA256, "flac", ttl, bucket)
				if fl.Expires.Before(exp) {
					exp = fl.Expires
				}
				b.Flac = &FlacLinkOrNull{URL: fl.URL, ContentType: m.Flac.ContentType, Bytes: m.Flac.Bytes, SHA256: m.Flac.SHA256,
					SampleRate: m.Flac.SampleRate, BitsPerSample: m.Flac.BitsPerSample}
			}
			b.ExpiresAt = exp.UTC().Format(time.RFC3339)
			b.Cover = d.coverLinks(ctx, in.ID, ttl, bucket)
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
