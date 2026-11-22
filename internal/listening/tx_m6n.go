package listening

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/view"
)

// LogTx is Log inside tx (M6N C381.4): the same validation and rows, committed with the caller's transaction.
func (s *Service) LogTx(ctx context.Context, tx *sql.Tx, p auth.Principal, src string, evs []Event) (accepted, duplicates int, rejected []Rejected, err error) {
	rejected = []Rejected{}
	if !slices.Contains(sources, src) {
		return 0, 0, nil, fmt.Errorf("listening: unknown source %q", src)
	}
	kind := string(p.Kind)
	if !p.IsPerson() {
		kind = string(auth.KindAgent)
	}
	durs := map[string]*int64{}
	hideDiscarded := view.FromContext(ctx).NotDiscarded("") // a discarded take is an unknown track for a Listener (C308.2)
	received := s.now().UTC().Format(time.RFC3339)
	for _, e := range evs {
		d, seen := durs[e.TrackID]
		if !seen {
			var sec float64
			switch err := tx.QueryRowContext(ctx, `SELECT duration_s FROM tracks WHERE id = ?`+hideDiscarded, e.TrackID).Scan(&sec); {
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
		from, to, detail := e.FromMS, e.ToMS, e.Detail
		if e.Kind == "seek" && from != nil && to != nil && *to < *from {
			from, to = to, from
			d := RewindDetail
			detail = &d
		}
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO listen_events
(id, track_id, session_id, kind, from_ms, to_ms, format, device_id, detail, source, principal_kind, principal_id, client_at, received_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, strings.ToLower(e.ID), e.TrackID, e.SessionID, e.Kind, from, to, e.Format, e.DeviceID, detail, src, kind, p.Key(),
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
	return accepted, duplicates, rejected, nil
}
