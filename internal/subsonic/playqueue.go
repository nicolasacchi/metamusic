package subsonic

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// maxQueue is the most ids a saved play queue holds (C109).
const maxQueue = 1000

func (s *server) registerPlayQueue() {
	s.add("getPlayQueue", false, (*call).getPlayQueue)
	s.add("savePlayQueue", true, (*call).savePlayQueue)
}

// getPlayQueue answers the singleton queue; with none saved the element is present and empty.
func (c *call) getPlayQueue() error {
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
	pq := &PlayQueue{Username: cl.Username, Changed: iso("1970-01-01T00:00:00Z")}
	c.resp.PlayQueue = pq
	var raw, changed, by string
	var current sql.NullString
	var pos int64
	err = st.R.QueryRowContext(c.ctx, `SELECT track_ids_json, current_track_id, position_ms, changed_at, changed_by FROM subsonic_play_queue WHERE id = 1`).
		Scan(&raw, &current, &pos, &changed, &by)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("subsonic: play queue: %w", err)
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return fmt.Errorf("subsonic: stored play queue: %w", err)
	}
	songs, err := cat.songsByIDs(c.ctx, ids)
	if err != nil {
		return err
	}
	pq.Entry = toChildren(songs, mp3)
	pq.Changed, pq.ChangedBy, pq.Position = iso(changed), by, pos
	for _, r := range songs {
		if r.ID == current.String {
			pq.Current = current.String
		}
	}
	return nil
}

// savePlayQueue replaces the one queue row: unknown ids are dropped, a current track outside the queue is forgotten.
func (c *call) savePlayQueue() error {
	st, err := c.st()
	if err != nil {
		return err
	}
	cat, err := c.cat()
	if err != nil {
		return err
	}
	in := c.all("id")
	if len(in) > maxQueue {
		return fail(errGeneric, "the play queue holds at most "+strconv.Itoa(maxQueue)+" songs")
	}
	pos := int64(0)
	if v := c.str("position"); v != "" {
		if pos, err = strconv.ParseInt(v, 10, 64); err != nil || pos < 0 {
			return fail(errGeneric, "invalid position")
		}
	}
	var ids []string
	for _, id := range in {
		if kind, raw := parseID(id); kind == kindSong {
			ids = append(ids, raw)
		}
	}
	songs, err := cat.songsByIDs(c.ctx, ids)
	if err != nil {
		return err
	}
	kept := make([]string, 0, len(songs))
	var current any
	for _, r := range songs {
		kept = append(kept, r.ID)
		if r.ID == c.str("current") {
			current = r.ID
		}
	}
	js, _ := json.Marshal(kept)
	by := c.principal.Client
	if by == "" {
		by = "subsonic"
	}
	_, err = st.W.ExecContext(c.ctx, `INSERT INTO subsonic_play_queue(id, track_ids_json, current_track_id, position_ms, changed_at, changed_by)
 VALUES(1,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET track_ids_json=excluded.track_ids_json, current_track_id=excluded.current_track_id,
 position_ms=excluded.position_ms, changed_at=excluded.changed_at, changed_by=excluded.changed_by`,
		string(js), current, pos, store.FormatTime(c.s.now()), by)
	if err != nil {
		return fmt.Errorf("subsonic: save play queue: %w", err)
	}
	return nil
}
