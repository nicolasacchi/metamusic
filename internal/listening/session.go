package listening

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"
)

// Session summarises the human's recent listening (C112.3): human and device listen events with
// client_at >= Since, grouped per track, newest last play first, at most the requested number of tracks.
// M4a-B2 freezes the types (prompts.Sources names them); M4a-A1 implements Service.Session.
type Session struct {
	Since  time.Time      `json:"since"`
	Until  time.Time      `json:"until"`
	Tracks []SessionTrack `json:"tracks" nullable:"false"`
	Totals SessionTotals  `json:"totals"`
}

// SessionTrack is one track of a Session.
type SessionTrack struct {
	TrackID      string    `json:"track_id"`
	Title        string    `json:"title"`
	Plays        int       `json:"plays"`
	Completed    int       `json:"completed"`
	Skipped      int       `json:"skipped"`
	SecondsHeard int       `json:"seconds_heard" doc:"sum of segment to_ms - from_ms, in seconds, rounded"`
	LastPlayedAt time.Time `json:"last_played_at"`
	Stars        *int      `json:"stars" nullable:"true" doc:"the human's rating, null when unrated"`
	OpenComments int       `json:"open_comments"`
}

// SessionTotals are the sums over the listed tracks.
type SessionTotals struct {
	Tracks       int `json:"tracks"`
	Plays        int `json:"plays"`
	SecondsHeard int `json:"seconds_heard"`
}

// SessionLimit is the default number of tracks of a Session.
const SessionLimit = 20

// Session reads the human's listening since a time (C112.3). Only human and device events count (agent events
// are the agent's own plays); diagnostics are not plays; limit <= 0 means SessionLimit.
func (s *Service) Session(ctx context.Context, since time.Time, limit int) (Session, error) {
	if limit <= 0 {
		limit = SessionLimit
	}
	until := s.now().UTC()
	out := Session{Since: since.UTC(), Until: until, Tracks: []SessionTrack{}}
	rows, err := s.st.R.QueryContext(ctx, `SELECT e.track_id, t.title,
		SUM(e.kind = 'start'), SUM(e.kind = 'complete'), SUM(e.kind = 'skip'),
		COALESCE(SUM(CASE WHEN e.kind = 'segment' THEN e.to_ms - e.from_ms END), 0),
		MAX(e.client_at),
		(SELECT r.stars FROM ratings r WHERE r.track_id = e.track_id AND r.source = 'human'),
		(SELECT COUNT(*) FROM comments c WHERE c.track_id = e.track_id AND c.hidden_at IS NULL AND c.state = 'open')
		FROM listen_events e JOIN tracks t ON t.id = e.track_id
		WHERE e.principal_kind IN ('human', 'device') AND e.client_at >= ?
		  AND e.kind IN ('start', 'segment', 'seek', 'complete', 'skip')
		GROUP BY e.track_id ORDER BY MAX(e.client_at) DESC, e.track_id LIMIT ?`,
		since.UTC().Format(time.RFC3339), limit)
	if err != nil {
		return Session{}, fmt.Errorf("listening: session: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			x     SessionTrack
			ms    int64
			last  string
			stars sql.NullInt64
		)
		if err := rows.Scan(&x.TrackID, &x.Title, &x.Plays, &x.Completed, &x.Skipped, &ms, &last, &stars, &x.OpenComments); err != nil {
			return Session{}, fmt.Errorf("listening: session: %w", err)
		}
		x.SecondsHeard = int(math.Round(float64(ms) / 1000))
		x.LastPlayedAt, _ = time.Parse(time.RFC3339, last)
		if stars.Valid {
			v := int(stars.Int64)
			x.Stars = &v
		}
		out.Tracks = append(out.Tracks, x)
		out.Totals.Plays += x.Plays
		out.Totals.SecondsHeard += x.SecondsHeard
	}
	if err := rows.Err(); err != nil {
		return Session{}, fmt.Errorf("listening: session: %w", err)
	}
	out.Totals.Tracks = len(out.Tracks)
	return out, nil
}
