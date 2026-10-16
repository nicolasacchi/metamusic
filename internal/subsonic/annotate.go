package subsonic

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/playlists"
)

func (s *server) registerAnnotate() {
	s.add("star", true, func(c *call) error { return c.setStarred(true) })
	s.add("unstar", true, func(c *call) error { return c.setStarred(false) })
	s.add("setRating", true, (*call).setRating)
}

// mapService maps a service error to the protocol (C109): a missing row is error 70; any validation failure
// (*library.ValidationError, a 422 *judgments.Error or *playlists.Error) is error 0 with the fixed text msg; anything
// else is returned unchanged and answered as "internal error".
func mapService(err error, msg string) error {
	if err == nil {
		return nil
	}
	var ve *library.ValidationError
	var je *judgments.Error
	var pe *playlists.Error
	switch {
	case errors.Is(err, library.ErrNotFound):
		return fail(errNotFound, "item not found")
	case errors.As(err, &ve):
		return fail(errGeneric, msg)
	case errors.As(err, &je) && je.Status == http.StatusUnprocessableEntity:
		return fail(errGeneric, msg)
	case errors.As(err, &pe) && pe.Code == "UNKNOWN_TRACK":
		return fail(errNotFound, "song not found")
	case errors.As(err, &pe) && (pe.Status == http.StatusUnprocessableEntity || pe.Status == http.StatusConflict):
		return fail(errGeneric, msg)
	}
	return err
}

func (c *call) judger() (judger, error) {
	if c.s.judg == nil {
		return nil, fail(errGeneric, "service unavailable")
	}
	return c.s.judg, nil
}

// setStarred implements star and unstar (D-M4a-10): star makes a song final, unstar turns a final song into
// to_relisten; every other state is left alone. Album and artist ids are accepted and ignored.
func (c *call) setStarred(star bool) error {
	cat, err := c.cat()
	if err != nil {
		return err
	}
	jd, err := c.judger()
	if err != nil {
		return err
	}
	if !c.params.Has("id") && !c.params.Has("albumId") && !c.params.Has("artistId") {
		return fail(errMissingParam, "required parameter is missing: id")
	}
	for _, id := range c.all("id") {
		kind, raw := parseID(id)
		if kind != kindSong {
			continue // an album or artist id in `id`: nothing to change
		}
		row, ok, err := cat.song(c.ctx, raw)
		if err != nil {
			return err
		}
		if !ok {
			return fail(errNotFound, "song not found")
		}
		to := "final"
		switch {
		case star && row.Status == "final", !star && row.Status != "final":
			continue
		case !star:
			to = "to_relisten"
		}
		if err := jd.UpdateTrack(c.ctx, c.principal, raw, nil, &to); err != nil {
			return mapService(err, "status change not allowed")
		}
	}
	return nil
}

// setRating rates a song as the operator. M2a's upsert overwrites the verdict with whatever is passed, so the
// existing human verdict is read first and passed back: a Subsonic rating never erases written feedback.
func (c *call) setRating() error {
	row, err := c.songParam("id")
	if err != nil {
		return err
	}
	raw := c.str("rating")
	if raw == "" {
		return fail(errMissingParam, "required parameter is missing: rating")
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 || n > 5 {
		return fail(errGeneric, "rating must be between 0 and 5")
	}
	jd, err := c.judger()
	if err != nil {
		return err
	}
	cur, err := jd.Ratings(c.ctx, row.ID)
	if err != nil {
		return mapService(err, "rating not allowed")
	}
	var verdict *string
	if cur.Human != nil {
		verdict = cur.Human.Verdict
	}
	var stars *int
	if n > 0 {
		stars = &n
	} else if cur.Human == nil {
		return nil // clearing a rating that does not exist writes nothing
	}
	_, err = jd.Rate(c.ctx, c.principal, row.ID, stars, verdict)
	return mapService(err, "rating not allowed")
}
