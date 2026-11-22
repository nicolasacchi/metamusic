package morning

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/listening"
	"github.com/nicolasacchi/metamusic/internal/playlists"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Comment source and bodies of headset events (C381.3).
const (
	commentSource      = "headset"
	bodyMarker         = "Headset marker"
	bodyStrongMarker   = "Headset keep"
	listenSource       = "web"
	playlistNameFormat = "Morning crate 2006-01-02"
)

// Reveal makes the crate's takes known: it stamps the reveal, maps every raw event to the Owner's comments, ratings and listen
// events, and creates the shared playlist, all in one transaction. Revealing a revealed crate changes nothing. Only the Owner or the
// Owner's device may reveal; an agent is refused with 403 HUMAN_ONLY.
func (s *Service) Reveal(ctx context.Context, p auth.Principal, id string) (Crate, error) {
	if err := personOwner(p); err != nil {
		return Crate{}, err
	}
	if err := s.needJudgment(); err != nil {
		return Crate{}, err
	}
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		c, err := loadCrate(ctx, tx, id)
		if err != nil {
			return err
		}
		if c.State != StateReady {
			return notReady()
		}
		if c.Revealed {
			return nil
		}
		_, err = s.revealTx(ctx, tx, p, c)
		return err
	})
	if err != nil {
		return Crate{}, err
	}
	return s.Get(ctx, p, id)
}

// revealTx stamps the reveal and runs the mapping and the playlist in tx. It reports false when the crate was already revealed.
func (s *Service) revealTx(ctx context.Context, tx *sql.Tx, p auth.Principal, c crateState) (bool, error) {
	res, err := tx.ExecContext(ctx, `UPDATE morning_crates SET revealed_at = ?, revealed_by = ? WHERE id = ? AND state = 'ready' AND revealed_at IS NULL`,
		store.FormatTime(s.now()), p.Key(), c.ID)
	if err != nil {
		return false, fmt.Errorf("morning: reveal: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	if err := s.mapEvents(ctx, tx, p, c); err != nil {
		return false, err
	}
	// the shared playlist exists from this transaction on and never before (D-M6N-OP-4): non-anchor takes in chapter order
	var tracks []string
	for n := 1; n <= len(c.Chapters); n++ {
		if ch, ok := c.Chapters[n]; ok && !ch.Anchor {
			tracks = append(tracks, ch.TrackID)
		}
	}
	if len(tracks) == 0 {
		return true, nil
	}
	name := c.CreatedAt.In(s.o.Loc).Format(playlistNameFormat)
	pid, err := s.o.Playlists.CreateTx(ctx, tx, auth.System("morning"), playlists.NewPlaylist{Name: name, TrackIDs: tracks})
	if err != nil {
		return false, fmt.Errorf("morning: reveal playlist: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE morning_crates SET playlist_id = ? WHERE id = ?`, pid, c.ID); err != nil {
		return false, fmt.Errorf("morning: reveal playlist: %w", err)
	}
	return true, nil
}

type rawEvent struct {
	ID        string
	Chapter   int
	Kind      string
	Stars     *int
	SideMS    int64
	LatencyMS int64
	ClientAt  string
	Received  string
}

// ownerRating is the Owner's rating row of a take as it stood before this mapping run wrote anything.
type ownerRating struct {
	exists  bool
	updated time.Time
	verdict *string
}

// mapEvents maps every unmapped event of the crate, oldest first, to the Owner's judgments (C381.3). A blind rating is superseded
// when the Owner's own rating of the take is newer than the event; an anchor's stars are never written. Each event is marked
// mapped exactly once.
func (s *Service) mapEvents(ctx context.Context, tx *sql.Tx, p auth.Principal, c crateState) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, chapter, kind, stars, side_ms, latency_ms, client_at, received_at
		FROM morning_crate_events WHERE crate_id = ? AND mapped_at IS NULL ORDER BY received_at, id`, c.ID)
	if err != nil {
		return fmt.Errorf("morning: unmapped events: %w", err)
	}
	var evs []rawEvent
	for rows.Next() {
		var e rawEvent
		var stars sql.NullInt64
		if err := rows.Scan(&e.ID, &e.Chapter, &e.Kind, &stars, &e.SideMS, &e.LatencyMS, &e.ClientAt, &e.Received); err != nil {
			_ = rows.Close()
			return err
		}
		if stars.Valid {
			v := int(stars.Int64)
			e.Stars = &v
		}
		evs = append(evs, e)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	snap := map[string]ownerRating{}
	mapped := store.FormatTime(s.now())
	for _, e := range evs {
		ch, ok := c.Chapters[e.Chapter]
		if !ok {
			return fmt.Errorf("morning: event %s names no chapter", e.ID)
		}
		var comment, rated, listen any
		superseded := 0
		switch e.Kind {
		case EventRate:
			if ch.Anchor {
				break // calibration only
			}
			rec, err := s.snapshot(ctx, tx, snap, ch.TrackID)
			if err != nil {
				return err
			}
			recv, err := store.ParseInstant(e.Received)
			if err != nil {
				return err
			}
			switch {
			case rec.exists && rec.updated.After(recv):
				superseded = 1
			case e.Stars == nil && !rec.exists:
				// clearing a rating nobody wrote: nothing to do
			default:
				if err := s.o.Judgments.RateTx(ctx, tx, p, ch.TrackID, e.Stars, rec.verdict); err != nil {
					return fmt.Errorf("morning: map rating: %w", err)
				}
				rated = ch.TrackID
			}
		case EventMarker, EventStrongMarker:
			sec := float64(trackMS(ch, e, true)) / 1000
			in := judgments.NewComment{Body: bodyMarker, Kind: "marker", AtSecond: &sec}
			if e.Kind == EventStrongMarker {
				in.Body, in.Kind = bodyStrongMarker, "keep"
			}
			id, err := s.o.Judgments.AddCommentTx(ctx, tx, p, commentSource, ch.TrackID, in)
			if err != nil {
				return fmt.Errorf("morning: map comment: %w", err)
			}
			comment = id
		default: // skip, complete, play
			le, err := s.listenEvent(c, ch, e)
			if err != nil {
				return err
			}
			acc, _, _, err := s.o.Listening.LogTx(ctx, tx, p, listenSource, []listening.Event{le})
			if err != nil {
				return fmt.Errorf("morning: map listen event: %w", err)
			}
			if acc > 0 {
				listen = le.ID
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE morning_crate_events SET comment_id = ?, rating_track_id = ?, listen_event_id = ?, superseded = ?, mapped_at = ?
			WHERE id = ? AND mapped_at IS NULL`, comment, rated, listen, superseded, mapped, e.ID); err != nil {
			return fmt.Errorf("morning: mark event mapped: %w", err)
		}
	}
	return nil
}

// snapshot reads the Owner's rating row of a take once per mapping run, before the run writes to it. Only the Owner or the Owner's
// device reaches the reveal (personOwner), whose rater is the Owner's.
func (s *Service) snapshot(ctx context.Context, tx *sql.Tx, snap map[string]ownerRating, track string) (ownerRating, error) {
	if r, ok := snap[track]; ok {
		return r, nil
	}
	var r ownerRating
	var updated string
	var verdict sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT updated_at, verdict FROM owner_ratings WHERE track_id = ?`, track).Scan(&updated, &verdict)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return r, fmt.Errorf("morning: owner rating: %w", err)
	default:
		t, err := store.ParseInstant(updated)
		if err != nil {
			return r, err
		}
		r.exists, r.updated = true, t
		if verdict.Valid {
			v := verdict.String
			r.verdict = &v
		}
	}
	snap[track] = r
	return r, nil
}

// trackMS is the position in the take of an event: side_ms less the take's start in the side, less (for a marker) the headset latency,
// clamped to the take.
func trackMS(ch chapterRow, e rawEvent, withLatency bool) int64 {
	at := e.SideMS - frameMS(ch.AudioStart)
	if withLatency {
		at -= e.LatencyMS
	}
	limit := frameMS(ch.End) - frameMS(ch.AudioStart)
	if d := int64(ch.DurationS * 1000); d < limit {
		limit = d
	}
	return max(0, min(at, limit))
}

// listenEvent maps a skip, complete or play event to a listen event (a play is a start).
func (s *Service) listenEvent(c crateState, ch chapterRow, e rawEvent) (listening.Event, error) {
	at, err := store.ParseInstant(e.ClientAt)
	if err != nil {
		return listening.Event{}, err
	}
	if recv, err := store.ParseInstant(e.Received); err == nil && (at.After(recv.Add(time.Minute)) || at.Before(recv.Add(-29*24*time.Hour))) {
		at = recv // a clock far from the server's would be rejected as stale or future; the arrival time is the honest one
	}
	le := listening.Event{ID: e.ID, TrackID: ch.TrackID, SessionID: "morning-" + strings.ReplaceAll(c.ID, "-", "")[:8], Kind: e.Kind, ClientAt: at}
	if e.Kind == EventPlay {
		le.Kind = "start"
	}
	if e.Kind == EventSkip {
		from := trackMS(ch, e, false)
		le.FromMS = &from
	}
	return le, nil
}
