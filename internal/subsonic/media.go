package subsonic

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/clean"
	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/view"
)

// Cache-Control of the media endpoints (C108.8): streams and downloads are revalidated, covers live a day.
const (
	cacheStream = "private, no-cache"
	cacheCover  = "private, max-age=86400"
)

func (s *server) registerMedia() {
	s.add("stream", false, (*call).stream)
	s.add("download", false, (*call).download)
	s.add("getCoverArt", false, (*call).coverArt)
}

// songParam resolves the id parameter to an existing track (any status: a discarded take stays playable by id).
func (c *call) songParam(name string) (songRow, error) { return c.songByID(name, c.str(name)) }

// songByID is songParam for an id already read (name only words the missing-parameter error).
func (c *call) songByID(name, raw string) (songRow, error) {
	cat, err := c.cat()
	if err != nil {
		return songRow{}, err
	}
	if raw == "" {
		return songRow{}, fail(errMissingParam, "required parameter is missing: "+name)
	}
	if kind, _ := parseID(raw); kind != kindSong {
		return songRow{}, fail(errNotFound, "song not found")
	}
	row, ok, err := cat.song(c.ctx, raw)
	if err != nil {
		return songRow{}, err
	}
	if !ok {
		return songRow{}, fail(errNotFound, "song not found")
	}
	return row, nil
}

// wantMP3 applies the stream format rules of C109 to a lossless track: raw, flac and original answer the master;
// mp3 and any other format name answer the MP3 rendition; without a format a maxBitRate of 1 to 320 means MP3, else
// the client's default format decides.
func (c *call) wantMP3() (bool, error) {
	switch strings.ToLower(c.str("format")) {
	case "raw", "flac", "original":
		return false, nil
	case "":
		if n, err := strconv.Atoi(c.str("maxBitRate")); err == nil && n >= 1 && n <= 320 {
			return true, nil
		}
		return c.mp3Default()
	}
	return true, nil
}

func (c *call) resolver() (mediaResolver, error) {
	if c.s.resolve == nil {
		return nil, fail(errGeneric, "service unavailable")
	}
	return c.s.resolve, nil
}

// notFound maps the "no such object" errors of the media sources to protocol error 70.
func notFound(err error) bool {
	return errors.Is(err, library.ErrNotFound) || errors.Is(err, serve.ErrNotFound) || errors.Is(err, library.ErrMediaUnavailable)
}

// listenerAudio serves a Listener client's stream or download (C308.6, D-U1-17): the client never receives the master,
// only mp3_320_clean (default, format=mp3, a maxBitRate of 1 to 320) or flac_clean (raw, flac, original). A discarded
// take is not found. The rendition is built on demand; a busy builder answers 503 with Retry-After.
func (c *call) listenerAudio(row songRow, download bool) error {
	cat, err := c.cat()
	if err != nil {
		return err
	}
	if err := library.Visible(c.ctx, cat.db, view.FromContext(c.ctx), row.ID); err != nil {
		return c.mediaErr(err)
	}
	if c.s.rends == nil {
		return fail(errGeneric, "service unavailable")
	}
	want, err := c.wantMP3()
	if err != nil {
		return err
	}
	kind := clean.KindFLAC
	if want {
		kind = clean.KindMP3
	}
	r, err := c.s.rends.Ensure(c.ctx, row.ID, kind)
	var busy *clean.ErrBusy
	switch {
	case errors.As(err, &busy):
		return &apiError{code: errGeneric, msg: "renditions are busy, retry shortly", status: http.StatusServiceUnavailable, retryAfter: busy.RetryAfter}
	case errors.Is(err, library.ErrNotFound) || errors.Is(err, library.ErrMediaUnavailable):
		return fail(errNotFound, "media not available")
	case err != nil:
		c.s.logger().Error("subsonic: listener rendition unavailable", "err", redact.Error(err))
		return fail(errGeneric, "service unavailable")
	}
	obj := r.Object()
	if download {
		obj.DownloadName = r.Filename
	} else {
		c.s.play.streamed.put(playKey(c.principal.ID, row.ID), r.Ext)
	}
	c.binary = true
	serve.ServeObject(c.w, c.r, c.s.o.Data, obj, cacheStream)
	return nil
}

func (c *call) stream() error {
	row, err := c.songParam("id")
	if err != nil {
		return err
	}
	if view.FromContext(c.ctx).Listener() {
		return c.listenerAudio(row, false)
	}
	res, err := c.resolver()
	if err != nil {
		return err
	}
	cat, err := c.cat()
	if err != nil {
		return err
	}
	master, mp3, err := cat.mediaSHAs(c.ctx, row.ID)
	if err != nil {
		return err
	}
	// A lossy master is the MP3 itself and is never transcoded (D-M4a-25); a lossless track answers one of its two files.
	ext, sha := "flac", master
	if row.Lossy {
		ext = "mp3"
	} else {
		want, err := c.wantMP3()
		if err != nil {
			return err
		}
		if want {
			if !mp3.Valid {
				return fail(errNotFound, "media not available")
			}
			ext, sha = "mp3", mp3.String
		}
	}
	obj, err := res.Resolve(c.ctx, sha, ext)
	if notFound(err) {
		return fail(errNotFound, "media not available")
	}
	if err != nil {
		return err
	}
	c.s.play.streamed.put(playKey(c.principal.ID, row.ID), ext)
	c.binary = true
	serve.ServeObject(c.w, c.r, c.s.o.Data, obj, cacheStream)
	return nil
}

func (c *call) download() error {
	row, err := c.songParam("id")
	if err != nil {
		return err
	}
	if view.FromContext(c.ctx).Listener() {
		return c.listenerAudio(row, true)
	}
	var obj serve.Object
	if row.Lossy {
		// The master is an MP3 and is never rewritten: serve it unchanged under its own name (D-M4a-25).
		res, err := c.resolver()
		if err != nil {
			return err
		}
		cat, err := c.cat()
		if err != nil {
			return err
		}
		master, _, err := cat.mediaSHAs(c.ctx, row.ID)
		if err != nil {
			return err
		}
		if obj, err = res.Resolve(c.ctx, master, "mp3"); err == nil {
			obj.DownloadName = row.Slug + "__" + strconv.FormatInt(row.Seed, 10) + ".mp3"
		}
		if err != nil {
			return c.mediaErr(err)
		}
	} else {
		if c.s.dls == nil {
			return fail(errGeneric, "service unavailable")
		}
		d, err := c.s.dls.Ensure(c.ctx, row.ID)
		if err != nil {
			return c.mediaErr(err)
		}
		if obj = d.Object; obj.DownloadName == "" {
			obj.DownloadName = d.Filename
		}
	}
	c.binary = true
	serve.ServeObject(c.w, c.r, c.s.o.Data, obj, cacheStream)
	return nil
}

func (c *call) mediaErr(err error) error {
	if notFound(err) {
		return fail(errNotFound, "media not available")
	}
	return err
}

func (c *call) coverArt() error {
	cat, err := c.cat()
	if err != nil {
		return err
	}
	if c.s.covers == nil {
		return fail(errGeneric, "service unavailable")
	}
	kind, raw := parseID(c.str("id"))
	size := cover.ClampSize(c.intParam("size", 0, 1<<20))
	var obj serve.Object
	switch kind {
	case kindBad:
		return c.badID()
	case kindAlbum:
		if ok, err := cat.albumExists(c.ctx, raw); err != nil {
			return err
		} else if !ok {
			return fail(errNotFound, "cover art not found")
		}
		obj, _, err = c.s.covers.EnsureAlbum(c.ctx, raw, size)
	case kindRecord, kindAuto:
		obj, err = c.albumCover(c.str("id"), size)
	case kindTrack, kindSong:
		if _, ok, err := cat.song(c.ctx, raw); err != nil {
			return err
		} else if !ok {
			return fail(errNotFound, "cover art not found")
		}
		obj, _, err = c.s.covers.EnsureTrack(c.ctx, raw, size)
	default:
		return fail(errNotFound, "cover art not found")
	}
	if err != nil {
		return c.mediaErr(err)
	}
	c.binary = true
	serve.ServeObject(c.w, c.r, c.s.o.Data, obj, cacheCover)
	return nil
}
