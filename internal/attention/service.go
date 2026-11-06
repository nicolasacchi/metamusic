package attention

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/anchor"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/listening"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Options builds the attention service.
type Options struct {
	Store *store.Store
}

// Service reads heat strips and hotspots of a take (C256). It only reads: the heat is computed on every call from
// listen_events and the take's timed sections, and nothing here writes a row.
type Service struct{ st *store.Store }

// New builds the service.
func New(o Options) *Service { return &Service{st: o.Store} }

// Heat returns the heat strip of one take, hotspots labelled with the timed section at their start. Only the events of
// human and device principals count (agents never listen); diagnostic kinds are not read at all. library.ErrNotFound
// for an unknown track.
func (s *Service) Heat(ctx context.Context, trackID string) (Heat, error) {
	t, err := library.Timing(ctx, s.st.R, trackID)
	if err != nil {
		return Heat{}, err
	}
	ev, err := s.events(ctx, trackID)
	if err != nil {
		return Heat{}, err
	}
	h := ComputeHeat(int64(t.DurationS*1000+0.5), ev, Bins)
	h.TrackID = trackID
	h.LabelHotspots(t)
	return h, nil
}

// Hotspots returns the (at most three) hotspots of one take, labelled; the /context bundle shows them under listening.
func (s *Service) Hotspots(ctx context.Context, trackID string) ([]Hotspot, error) {
	h, err := s.Heat(ctx, trackID)
	if err != nil {
		return nil, err
	}
	return h.Hotspots, nil
}

// Timing returns the best timed sections of a take (library.Timing); the /context bundle maps comment spots with it.
func (s *Service) Timing(ctx context.Context, trackID string) (anchor.Timing, error) {
	return library.Timing(ctx, s.st.R, trackID)
}

func (s *Service) events(ctx context.Context, trackID string) ([]listening.Event, error) {
	rows, err := s.st.R.QueryContext(ctx, `SELECT id, session_id, kind, from_ms, to_ms, detail, client_at FROM listen_events
		WHERE track_id = ? AND principal_kind IN ('human', 'device') AND kind IN ('start', 'segment', 'seek', 'complete', 'skip')
		ORDER BY client_at, id`, trackID)
	if err != nil {
		return nil, fmt.Errorf("attention: events: %w", err)
	}
	defer rows.Close()
	var out []listening.Event
	for rows.Next() {
		e := listening.Event{TrackID: trackID}
		var from, to sql.NullInt64
		var detail sql.NullString
		var at string
		if err := rows.Scan(&e.ID, &e.SessionID, &e.Kind, &from, &to, &detail, &at); err != nil {
			return nil, fmt.Errorf("attention: events: %w", err)
		}
		if from.Valid {
			e.FromMS = &from.Int64
		}
		if to.Valid {
			e.ToMS = &to.Int64
		}
		if detail.Valid {
			e.Detail = &detail.String
		}
		e.ClientAt, _ = time.Parse(time.RFC3339, at)
		out = append(out, listening.Unnormalise(e))
	}
	return out, rows.Err()
}

// InFlightPatches maps each comment id to the ids of the Song PRs that address it and still have a render that has not
// ended (a job of the patch in a state other than completed, failed, cancelled or expired), oldest patch first. Comments
// without such a patch are absent from the map. The /context bundle shows them as in_flight_patch_ids.
func (s *Service) InFlightPatches(ctx context.Context, commentIDs []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(commentIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(commentIDs))
	for i, id := range commentIDs {
		args[i] = id
	}
	//nolint:gosec // G202: only "?" placeholders are concatenated
	rows, err := s.st.R.QueryContext(ctx, `SELECT pc.comment_id, p.id FROM patch_comments pc JOIN patches p ON p.id = pc.patch_id
		WHERE pc.comment_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+`)
		AND EXISTS (SELECT 1 FROM jobs j WHERE j.patch_id = p.id AND j.state NOT IN ('completed', 'failed', 'cancelled', 'expired'))
		ORDER BY p.created_at, p.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("attention: in-flight patches: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c, p string
		if err := rows.Scan(&c, &p); err != nil {
			return nil, fmt.Errorf("attention: in-flight patches: %w", err)
		}
		out[c] = append(out[c], p)
	}
	return out, rows.Err()
}
