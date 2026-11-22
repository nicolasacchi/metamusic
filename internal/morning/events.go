package morning

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Event kinds on the wire (C381.1).
const (
	EventMarker       = "marker"
	EventStrongMarker = "strong_marker"
	EventSkip         = "skip"
	EventComplete     = "complete"
	EventPlay         = "play"
	EventRate         = "rate"
)

// Rejection codes of one event.
const (
	RejectUnknownChapter = "UNKNOWN_CHAPTER"
	RejectOutOfRange     = "OUT_OF_RANGE"
	RejectInvalid        = "INVALID_EVENT"
)

// MaxEvents is the most events one call carries.
const MaxEvents = 200

// maxLatencyMS bounds the headset latency a client may declare.
const maxLatencyMS = 5000

// sideSlackMS is how far past its side's end an event position may be (a seek that overshoots by a tick).
const sideSlackMS = 2000

// EventIn is one raw headset or rating event of a crate.
type EventIn struct {
	ID        string // client UUIDv7, the idempotency key
	Side      int    // 0-based
	Chapter   int    // 1-based global chapter number
	Kind      string
	Stars     *int // rate only: 1..5, nil clears
	SideMS    int64
	LatencyMS int64
	ClientAt  time.Time
}

// EventRejection names one refused event.
type EventRejection struct {
	ID   string `json:"id"`
	Code string `json:"code"`
}

// EventsResult is the answer of SubmitEvents.
type EventsResult struct {
	Accepted   int
	Duplicates int
	Rejected   []EventRejection
	Revealed   bool // the crate is revealed now (by this call or earlier)
}

// personOwner admits only the Owner or the Owner's device: the crate's events and its reveal write the Owner's own judgments, so an
// agent (an Owner delegate) never submits them (D-M6N-8, C381.2).
func personOwner(p auth.Principal) error {
	if err := ownerSide(p); err != nil {
		return err
	}
	if !p.IsPerson() {
		return problem.E(403, "HUMAN_ONLY", "only the operator rates and reveals a morning crate", nil)
	}
	return nil
}

func notReady() *problem.Error {
	return problem.E(409, "MORNING_CRATE_NOT_READY", "the morning crate has no sides to rate", nil)
}

func (s *Service) needJudgment() error {
	if s.o.Judgments == nil || s.o.Listening == nil || s.o.Playlists == nil {
		return problem.E(503, "UNAVAILABLE", "morning crate judgments are not available", map[string]any{"component": "morning"})
	}
	return nil
}

// chapterRow is one stored chapter with what mapping needs.
type chapterRow struct {
	N          int
	Side       int
	AudioStart int64 // frames
	End        int64 // frames
	TrackID    string
	Anchor     bool
	DurationS  float64
}

type crateState struct {
	ID        string
	State     string
	CreatedAt time.Time
	Revealed  bool
	Chapters  map[int]chapterRow
	SideMS    map[int]int64
}

// loadCrate reads a crate's state, chapters and side lengths through q (a transaction, so the answer is the one the write sees).
func loadCrate(ctx context.Context, q Querier, id string) (crateState, error) {
	c := crateState{ID: id, Chapters: map[int]chapterRow{}, SideMS: map[int]int64{}}
	var created string
	var revealed sql.NullString
	err := q.QueryRowContext(ctx, `SELECT state, created_at, revealed_at FROM morning_crates WHERE id = ?`, id).Scan(&c.State, &created, &revealed)
	if errors.Is(err, sql.ErrNoRows) {
		return c, notFound(id)
	}
	if err != nil {
		return c, fmt.Errorf("morning: load crate: %w", err)
	}
	if c.CreatedAt, err = store.ParseInstant(created); err != nil {
		return c, err
	}
	c.Revealed = revealed.Valid
	if c.State != StateReady {
		return c, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT i.n, i.side_index, i.audio_start_frame, i.end_frame, i.track_id, i.anchor, t.duration_s
		FROM morning_crate_items i JOIN tracks t ON t.id = i.track_id WHERE i.crate_id = ? ORDER BY i.n`, id)
	if err != nil {
		return c, fmt.Errorf("morning: load chapters: %w", err)
	}
	for rows.Next() {
		var r chapterRow
		var anchor int
		if err := rows.Scan(&r.N, &r.Side, &r.AudioStart, &r.End, &r.TrackID, &anchor, &r.DurationS); err != nil {
			_ = rows.Close()
			return c, err
		}
		r.Anchor = anchor == 1
		c.Chapters[r.N] = r
	}
	if err := rows.Close(); err != nil {
		return c, err
	}
	srows, err := q.QueryContext(ctx, `SELECT side_index, samples FROM morning_crate_sides WHERE crate_id = ?`, id)
	if err != nil {
		return c, fmt.Errorf("morning: load sides: %w", err)
	}
	defer srows.Close()
	for srows.Next() {
		var i int
		var n int64
		if err := srows.Scan(&i, &n); err != nil {
			return c, err
		}
		c.SideMS[i] = frameMS(n)
	}
	return c, srows.Err()
}

// validate returns the rejection code of one event, or "".
func (c crateState) validate(e EventIn) string {
	if u, err := uuid.Parse(e.ID); err != nil || !strings.EqualFold(u.String(), e.ID) {
		return RejectInvalid
	}
	switch e.Kind {
	case EventMarker, EventStrongMarker, EventSkip, EventComplete, EventPlay:
		if e.Stars != nil {
			return RejectInvalid
		}
	case EventRate:
		if e.Stars != nil && (*e.Stars < 1 || *e.Stars > 5) {
			return RejectInvalid
		}
	default:
		return RejectInvalid
	}
	if e.ClientAt.IsZero() {
		return RejectInvalid
	}
	ch, ok := c.Chapters[e.Chapter]
	if !ok || ch.Side != e.Side {
		return RejectUnknownChapter
	}
	if e.SideMS < 0 || e.SideMS > c.SideMS[e.Side]+sideSlackMS || e.LatencyMS < 0 || e.LatencyMS > maxLatencyMS {
		return RejectOutOfRange
	}
	return ""
}

// SubmitEvents stores raw events of a ready crate in one transaction (C381.1). Before the reveal they touch no comment, rating or
// listen event; the batch that gives every non-anchor chapter blind stars reveals the crate in the same transaction; after the
// reveal each event is mapped in the transaction that stores it.
func (s *Service) SubmitEvents(ctx context.Context, p auth.Principal, crateID string, evs []EventIn) (EventsResult, error) {
	if err := personOwner(p); err != nil {
		return EventsResult{}, err
	}
	if err := s.needJudgment(); err != nil {
		return EventsResult{}, err
	}
	if len(evs) > MaxEvents {
		return EventsResult{}, problem.E(422, "VALIDATION", fmt.Sprintf("at most %d events per call", MaxEvents), map[string]any{"field": "events"})
	}
	res := EventsResult{Rejected: []EventRejection{}}
	var created []string // comment and playlist ids of the mapping, noted for the audit row after the commit
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		created = nil
		res = EventsResult{Rejected: []EventRejection{}}
		c, err := loadCrate(ctx, tx, crateID)
		if err != nil {
			return err
		}
		if c.State != StateReady {
			return notReady()
		}
		received := store.FormatTime(s.now())
		for _, e := range evs {
			if code := c.validate(e); code != "" {
				res.Rejected = append(res.Rejected, EventRejection{ID: e.ID, Code: code})
				continue
			}
			var stars any
			if e.Stars != nil {
				stars = *e.Stars
			}
			r, err := tx.ExecContext(ctx, `INSERT INTO morning_crate_events(id, crate_id, side_index, chapter, kind, stars, side_ms, latency_ms, client_at, received_at, received_by)
				VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, strings.ToLower(e.ID), crateID, e.Side, e.Chapter, e.Kind, stars, e.SideMS, e.LatencyMS,
				store.FormatTime(e.ClientAt), received, p.Key())
			if err != nil {
				return fmt.Errorf("morning: insert event: %w", err)
			}
			if n, _ := r.RowsAffected(); n > 0 {
				res.Accepted++
			} else {
				res.Duplicates++
			}
		}
		res.Revealed = c.Revealed
		switch {
		case res.Accepted == 0:
		case c.Revealed:
			return s.mapEvents(ctx, tx, p, c, &created)
		default:
			all, err := allRated(ctx, tx, c)
			if err != nil {
				return err
			}
			if all {
				if _, err := s.revealTx(ctx, tx, p, c, &created); err != nil {
					return err
				}
				res.Revealed = true
			}
		}
		return nil
	})
	if err == nil {
		audit.NoteCreated(ctx, created...)
	}
	return res, err
}

// allRated reports whether every non-anchor chapter's latest blind rating is a star count (and there is at least one such chapter).
func allRated(ctx context.Context, q Querier, c crateState) (bool, error) {
	need := 0
	for _, ch := range c.Chapters {
		if !ch.Anchor {
			need++
		}
	}
	if need == 0 {
		return false, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT chapter, stars FROM morning_crate_events WHERE crate_id = ? AND kind = 'rate' ORDER BY received_at, id`, c.ID)
	if err != nil {
		return false, fmt.Errorf("morning: ratings: %w", err)
	}
	defer rows.Close()
	latest := map[int]bool{}
	for rows.Next() {
		var ch int
		var stars sql.NullInt64
		if err := rows.Scan(&ch, &stars); err != nil {
			return false, err
		}
		latest[ch] = stars.Valid
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	for n, ch := range c.Chapters {
		if !ch.Anchor && !latest[n] {
			return false, nil
		}
	}
	return true, nil
}
