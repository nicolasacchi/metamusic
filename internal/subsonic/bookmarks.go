package subsonic

import (
	"database/sql"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func (s *server) registerBookmarks() {
	s.add("getBookmarks", false, (*call).getBookmarks)
	s.add("createBookmark", true, (*call).createBookmark)
	s.add("deleteBookmark", true, (*call).deleteBookmark)
}

// st returns the store behind the bookmark and play queue tables.
func (c *call) st() (*store.Store, error) {
	if c.s.o.Store == nil {
		return nil, fail(errGeneric, "service unavailable")
	}
	return c.s.o.Store, nil
}

func (c *call) getBookmarks() error {
	st, err := c.st()
	if err != nil {
		return err
	}
	cat, err := c.cat()
	if err != nil {
		return err
	}
	cl, err := c.client()
	if err != nil {
		return err
	}
	mp3, err := c.mp3Default()
	if err != nil {
		return err
	}
	rows, err := st.R.QueryContext(c.ctx, `SELECT track_id, position_ms, comment, created_at, updated_at FROM subsonic_bookmarks
 WHERE deleted_at IS NULL ORDER BY updated_at DESC, track_id`)
	if err != nil {
		return fmt.Errorf("subsonic: bookmarks: %w", err)
	}
	type bm struct {
		track            string
		pos              int64
		comment          sql.NullString
		created, changed string
	}
	var all []bm
	for rows.Next() {
		var b bm
		if err := rows.Scan(&b.track, &b.pos, &b.comment, &b.created, &b.changed); err != nil {
			_ = rows.Close()
			return err
		}
		all = append(all, b)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	ids := make([]string, len(all))
	for i, b := range all {
		ids[i] = b.track
	}
	songs, err := cat.songsByIDs(c.ctx, ids)
	if err != nil {
		return err
	}
	by := make(map[string]songRow, len(songs))
	for _, r := range songs {
		by[r.ID] = r
	}
	out := &Bookmarks{Bookmark: []Bookmark{}}
	for _, b := range all {
		r, ok := by[b.track]
		if !ok {
			continue
		}
		out.Bookmark = append(out.Bookmark, Bookmark{Position: b.pos, Username: cl.Username, Comment: b.comment.String,
			Created: iso(b.created), Changed: iso(b.changed), Entry: toChild(r, mp3)})
	}
	c.resp.Bookmarks = out
	return nil
}

// createBookmark stores the resume position of a song (one per track). A non-empty comment that differs from the
// bookmark's previous comment also creates one timestamped comment (D-M4a-12); comment-less bookmarks stay
// resume positions only.
func (c *call) createBookmark() error {
	row, err := c.songParam("id")
	if err != nil {
		return err
	}
	raw := c.str("position")
	if raw == "" {
		return fail(errMissingParam, "required parameter is missing: position")
	}
	pos, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || pos < 0 {
		return fail(errGeneric, "invalid position")
	}
	st, err := c.st()
	if err != nil {
		return err
	}
	comment := strings.TrimSpace(c.str("comment"))
	var commentID any
	if comment != "" {
		var prev sql.NullString
		switch err := st.R.QueryRowContext(c.ctx, `SELECT comment FROM subsonic_bookmarks WHERE track_id = ?`, row.ID).Scan(&prev); {
		case err == sql.ErrNoRows:
		case err != nil:
			return fmt.Errorf("subsonic: bookmark: %w", err)
		}
		if !prev.Valid || prev.String != comment {
			jd, err := c.judger()
			if err != nil {
				return err
			}
			at := math.Min(float64(pos)/1000, row.DurationS)
			cm, err := jd.AddComment(c.ctx, c.principal, "subsonic", row.ID, judgments.NewComment{Body: comment, AtSecond: &at, Kind: "note"})
			if err != nil {
				return mapService(err, "bookmark not allowed")
			}
			commentID = cm.ID
		}
	}
	var stored any
	if comment != "" {
		stored = comment
	}
	now := store.FormatTime(c.s.now())
	_, err = st.W.ExecContext(c.ctx, `INSERT INTO subsonic_bookmarks(track_id, position_ms, comment, comment_id, created_at, updated_at, updated_by, deleted_at)
 VALUES(?,?,?,?,?,?,?,NULL) ON CONFLICT(track_id) DO UPDATE SET position_ms=excluded.position_ms, comment=excluded.comment,
 comment_id=COALESCE(excluded.comment_id, subsonic_bookmarks.comment_id), updated_at=excluded.updated_at, updated_by=excluded.updated_by,
 deleted_at=NULL`, row.ID, pos, stored, commentID, now, now, c.principal.Key())
	if err != nil {
		return fmt.Errorf("subsonic: create bookmark: %w", err)
	}
	return nil
}

// deleteBookmark hides the bookmark; the comment it created stays (comments are never deleted).
func (c *call) deleteBookmark() error {
	row, err := c.songParam("id")
	if err != nil {
		return err
	}
	st, err := c.st()
	if err != nil {
		return err
	}
	now := store.FormatTime(c.s.now())
	if _, err := st.W.ExecContext(c.ctx, `UPDATE subsonic_bookmarks SET deleted_at=?, updated_at=?, updated_by=? WHERE track_id=? AND deleted_at IS NULL`,
		now, now, c.principal.Key(), row.ID); err != nil {
		return fmt.Errorf("subsonic: delete bookmark: %w", err)
	}
	return nil
}
