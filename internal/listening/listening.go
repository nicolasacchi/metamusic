// Package listening records listen events (contract C36). M2a-B4a freezes the
// signatures; M2a-B8 implements them.
package listening

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Listening is the summary shown on track detail and in the context bundle.
type Listening = library.ListeningStats

// Event is one listen event.
type Event struct {
	ID, TrackID, SessionID, Kind string
	FromMS, ToMS                 *int64
	Format, DeviceID, Detail     *string
	ClientAt                     time.Time
}

// Rejected names an event that failed validation.
type Rejected struct {
	ID   string `json:"id"`
	Code string `json:"code"`
}

// Service implements C36.
type Service struct {
	st  *store.Store
	now func() time.Time
}

// New builds the listening service; nil now means time.Now.
func New(st *store.Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{st: st, now: now}
}

var (
	kinds = []string{"start", "segment", "seek", "complete", "skip", "autoplay_blocked", "stall_end", "advance", "error"}
	// diagKinds are client playback diagnostics: stored for diagnosing real-device sessions, never counted
	// as plays or completions.
	diagKinds = []string{"autoplay_blocked", "stall_end", "advance", "error"}
	detailRE  = regexp.MustCompile(`^[a-z0-9_:=.,-]{1,64}$`)
	sources   = []string{"web", "api", "mcp", "stdio"}
)

const (
	rangeSlackMS = 2000
	maxPast      = 30 * 24 * time.Hour
	maxFuture    = 5 * time.Minute
)

// validate returns the rejection code of one event, or "". dur is the track
// duration in ms (ok false: unknown track).
func (s *Service) validate(e Event, dur int64, ok bool) string {
	if u, err := uuid.Parse(e.ID); err != nil || !strings.EqualFold(u.String(), e.ID) {
		return "bad_id"
	}
	if !slices.Contains(kinds, e.Kind) {
		return "bad_kind"
	}
	if !ok {
		return "unknown_track"
	}
	if n := len(e.SessionID); n < 8 || n > 64 {
		return "bad_session"
	}
	if e.Detail != nil && (!slices.Contains(diagKinds, e.Kind) || !detailRE.MatchString(*e.Detail)) {
		return "bad_detail"
	}
	if e.Format != nil && *e.Format != "flac" && *e.Format != "mp3" {
		return "bad_format"
	}
	if (e.Kind == "segment" || e.Kind == "seek") && (e.FromMS == nil || e.ToMS == nil) {
		return "bad_range"
	}
	for _, v := range []*int64{e.FromMS, e.ToMS} {
		if v != nil && (*v < 0 || *v > dur+rangeSlackMS) {
			return "bad_range"
		}
	}
	if e.FromMS != nil && e.ToMS != nil && *e.ToMS < *e.FromMS {
		return "bad_range"
	}
	now := s.now()
	if e.ClientAt.IsZero() || e.ClientAt.Before(now.Add(-maxPast)) || e.ClientAt.After(now.Add(maxFuture)) {
		return "bad_client_at"
	}
	return ""
}

// Log stores events idempotently; src is the transport label (web|api|mcp|stdio).
// A zero ClientAt means the caller could not parse the timestamp.
func (s *Service) Log(ctx context.Context, p auth.Principal, src string, evs []Event) (accepted, duplicates int, rejected []Rejected, err error) {
	rejected = []Rejected{}
	if !slices.Contains(sources, src) {
		return 0, 0, nil, fmt.Errorf("listening: unknown source %q", src)
	}
	kind := string(p.Kind)
	if p.Kind != auth.KindHuman && p.Kind != auth.KindDevice {
		kind = string(auth.KindAgent)
	}
	durs := map[string]*int64{}
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	received := s.now().UTC().Format(time.RFC3339)
	for _, e := range evs {
		d, seen := durs[e.TrackID]
		if !seen {
			var sec float64
			switch err := tx.QueryRowContext(ctx, `SELECT duration_s FROM tracks WHERE id = ?`, e.TrackID).Scan(&sec); {
			case err == nil:
				ms := int64(math.Round(sec * 1000))
				d = &ms
			case !errors.Is(err, sql.ErrNoRows):
				return 0, 0, nil, fmt.Errorf("listening: track: %w", err)
			}
			durs[e.TrackID] = d
		}
		var dur int64
		if d != nil {
			dur = *d
		}
		if code := s.validate(e, dur, d != nil); code != "" {
			rejected = append(rejected, Rejected{ID: e.ID, Code: code})
			continue
		}
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO listen_events
(id, track_id, session_id, kind, from_ms, to_ms, format, device_id, detail, source, principal_kind, principal_id, client_at, received_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, strings.ToLower(e.ID), e.TrackID, e.SessionID, e.Kind, e.FromMS, e.ToMS, e.Format, e.DeviceID, e.Detail, src, kind, p.Key(),
			e.ClientAt.UTC().Format(time.RFC3339), received)
		if err != nil {
			return 0, 0, nil, fmt.Errorf("listening: insert: %w", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			accepted++
		} else {
			duplicates++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, nil, fmt.Errorf("listening: commit: %w", err)
	}
	return accepted, duplicates, rejected, nil
}

// Stats summarises human listening of one track: only human and device events count.
func (s *Service) Stats(ctx context.Context, trackID string) (Listening, error) {
	var starts, completes int
	var last sql.NullString
	if err := s.st.R.QueryRowContext(ctx, `SELECT COALESCE(SUM(kind = 'start'), 0), COALESCE(SUM(kind = 'complete'), 0), MAX(CASE WHEN kind = 'start' THEN client_at END)
		FROM listen_events WHERE track_id = ? AND principal_kind IN ('human', 'device')`, trackID).Scan(&starts, &completes, &last); err != nil {
		return Listening{}, fmt.Errorf("listening: stats: %w", err)
	}
	out := Listening{Plays: starts}
	if last.Valid {
		out.LastPlayedAt = &last.String
	}
	if starts > 0 {
		r := min(float64(completes)/float64(starts), 1)
		out.CompletionRate = &r
	}
	return out, nil
}
