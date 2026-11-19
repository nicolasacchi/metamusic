package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/media/clean"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/view"
)

// isListener reports whether the caller is a Listener (or a Listener's device or client): such a caller never receives
// a master, only the clean renditions (D-U1-17, C308.7).
func isListener(ctx context.Context) bool { return view.FromContext(ctx).Listener() }

// renditionErr maps a clean.Service failure: a busy builder is 503 RENDITION_BUSY with Retry-After, a rendition that
// could not be built or failed its allow-list check is 503 RENDITION_UNAVAILABLE (fail closed: the master is never
// the fallback), everything else goes through libErr.
func renditionErr(err error) error {
	var busy *clean.ErrBusy
	var un *clean.ErrUnavailable
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		busy = &clean.ErrBusy{}
	} else {
		_ = errors.As(err, &busy)
	}
	switch {
	case busy != nil:
		secs := max(int(busy.RetryAfter/time.Second), 1)
		return huma.ErrorWithHeaders(Err(http.StatusServiceUnavailable, "RENDITION_BUSY", "audio renditions are being built, retry shortly", map[string]any{"retry_after_s": secs}),
			http.Header{"Retry-After": []string{strconv.Itoa(secs)}})
	case errors.As(err, &un):
		slog.Error("clean rendition unavailable", "err", redact.Error(err))
		return Err(http.StatusServiceUnavailable, "RENDITION_UNAVAILABLE", "the audio rendition is not available", nil)
	}
	return libErr(err)
}

// listenerRendition returns the clean rendition a Listener gets for a kind; a missing service fails closed.
func (d Deps) listenerRendition(ctx context.Context, trackID, kind string) (clean.Rendition, error) {
	if d.Renditions == nil {
		return clean.Rendition{}, unavailable("renditions")
	}
	r, err := d.Renditions.Ensure(ctx, trackID, kind)
	if err != nil {
		return clean.Rendition{}, renditionErr(err)
	}
	return r, nil
}

// listenerMediaLinks fills the get_media_links body for a Listener: the two clean renditions only (no master, no
// sha256 field, no stems, whose files are not stripped), the lyrics.lrc link without its hash. A lossy master has no
// FLAC, as for everyone; its mp3 is the clean copy of the master.
func (d Deps) listenerMediaLinks(ctx context.Context, id string) (*mediaLinksOut, error) {
	m, err := d.Library.Media(ctx, id)
	if err != nil {
		return nil, libErr(err)
	}
	ttl, bucket := serve.TTLFor(auth.FromContext(ctx))
	out := &mediaLinksOut{}
	b := &out.Body
	b.TrackID, b.TTLS, b.Lossy = m.TrackID, int64(ttl.Seconds()), m.Lossy
	mp3, err := d.listenerRendition(ctx, id, clean.KindMP3)
	if err != nil {
		return nil, err
	}
	ml := d.Signer.Sign(mp3.SHA256, "mp3", ttl, bucket)
	exp := ml.Expires
	b.MP3 = MP3Link{URL: ml.URL, ContentType: mp3.ContentType, Bytes: mp3.Bytes, BitrateKbps: m.MP3.BitrateKbps}
	if m.Flac != nil {
		fl, err := d.listenerRendition(ctx, id, clean.KindFLAC)
		if err != nil {
			return nil, err
		}
		l := d.Signer.Sign(fl.SHA256, "flac", ttl, bucket)
		if l.Expires.Before(exp) {
			exp = l.Expires
		}
		b.Flac = &FlacLinkOrNull{URL: l.URL, ContentType: fl.ContentType, Bytes: fl.Bytes, SampleRate: m.Flac.SampleRate, BitsPerSample: m.Flac.BitsPerSample}
	}
	lrc, err := d.Library.LRC(ctx, id)
	if err != nil {
		return nil, libErr(err)
	}
	if lrc != nil {
		l := d.Signer.Sign(lrc.SHA256, "lrc", ttl, bucket)
		if l.Expires.Before(exp) {
			exp = l.Expires
		}
		b.LRC = &LRCLink{URL: l.URL, Bytes: lrc.Bytes}
	}
	b.ExpiresAt = exp.UTC().Format(time.RFC3339)
	b.Cover = d.coverLinks(ctx, id, ttl, bucket)
	return out, nil
}

// listenerDownloadLink is get_download_link for a Listener: the clean FLAC (the MP3 for a lossy master) under a
// filename built from the title alone, never the seed. The tags the response lists are the ones the file carries:
// the title. The URL serves inline (the same signed object streams in the player), so the filename is advisory.
func (d Deps) listenerDownloadLink(ctx context.Context, id string) (*downloadLinkOut, error) {
	r, err := d.listenerRendition(ctx, id, clean.KindFLAC)
	if err != nil {
		return nil, err
	}
	ttl, bucket := serve.TTLFor(auth.FromContext(ctx))
	l := d.Signer.Sign(r.SHA256, r.Ext, ttl, bucket)
	out := &downloadLinkOut{}
	b := &out.Body
	b.TrackID, b.URL, b.Filename, b.Bytes, b.SHA256 = id, l.URL, r.Filename, r.Bytes, r.SHA256
	b.ExpiresAt, b.TTLS = l.Expires.UTC().Format(time.RFC3339), int64(ttl.Seconds())
	b.Tags = DownloadTags{Title: r.Title}
	return out, nil
}
