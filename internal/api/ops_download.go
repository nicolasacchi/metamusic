package api

import (
	"context"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
)

// registerDownloadM4a registers get_download_link (C110.2, C113).
func registerDownloadM4a(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_download_link", Method: "GET", Path: "/api/tracks/{id}/download",
		Summary:     "Get a download link with current tags",
		Description: "A signed, expiring link to a FLAC copy of the track carrying its current title, album, track number and tags (audio frames unchanged). A lossy track answers a link to its MP3 master unchanged. Humans get 12 h links, agents 15 min.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/tracks/:id", Rate: "heavy"},
		func(ctx context.Context, in *trackIDIn) (*downloadLinkOut, error) {
			if d.Downloads == nil {
				return nil, unavailable("downloads")
			}
			if d.Signer == nil {
				return nil, unavailable("signer")
			}
			dl, err := d.Downloads.Ensure(ctx, in.ID)
			if err != nil {
				return nil, libErr(err)
			}
			tags := dl.Tags
			ext := "flac"
			if dl.Object.ContentType == "audio/mpeg" { // a lossy master: the download is the master itself
				ext = "mp3"
				if tags, err = d.Downloads.TagsOf(ctx, in.ID); err != nil {
					return nil, libErr(err)
				}
			}
			ttl, bucket := serve.TTLFor(auth.FromContext(ctx))
			l := d.Signer.Sign(dl.Key, ext, ttl, bucket)
			out := &downloadLinkOut{}
			b := &out.Body
			b.TrackID, b.URL, b.Filename, b.Bytes, b.SHA256 = in.ID, l.URL, dl.Filename, dl.Bytes, dl.SHA256
			b.ExpiresAt, b.TTLS = l.Expires.UTC().Format(time.RFC3339), int64(ttl.Seconds())
			b.Tags = DownloadTags{Title: tags.Title, Album: tags.Album, TrackNumber: tags.TrackNumber, TrackTotal: tags.TrackTotal, Date: tags.Date}
			if tags.Genre != "" {
				g := tags.Genre
				b.Tags.Genre = &g
			}
			return out, nil
		})
}
