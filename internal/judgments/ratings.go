package judgments

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
)

// inv builds the validation error the API renders as 422 VALIDATION_FAILED.
func inv(field, format string, a ...any) error {
	return &library.ValidationError{Field: field, Msg: fmt.Sprintf(format, a...)}
}

func (s *Service) stamp() string { return s.now().UTC().Format(time.RFC3339) }

// requireTrack returns library.ErrNotFound for an unknown track id.
func (s *Service) requireTrack(ctx context.Context, trackID string) error {
	ok, err := s.lib.TrackExists(ctx, trackID)
	if err != nil {
		return fmt.Errorf("judgments: track lookup: %w", err)
	}
	if !ok {
		return library.ErrNotFound
	}
	return nil
}

// Rate sets or clears the caller's rating. The source comes from the principal
// only: a human or device rates as the operator, anything else as the critic.
func (s *Service) Rate(ctx context.Context, p auth.Principal, trackID string, stars *int, verdict *string) (TrackRating, error) {
	if stars != nil && (*stars < 1 || *stars > 5) {
		return TrackRating{}, inv("stars", "must be 1..5 or null")
	}
	var v any
	if verdict != nil {
		t := strings.TrimSpace(*verdict)
		if strings.ContainsRune(t, 0) {
			return TrackRating{}, inv("verdict", "must not contain NUL")
		}
		if utf8.RuneCountInString(t) > 280 {
			return TrackRating{}, inv("verdict", "at most 280 characters")
		}
		if t != "" {
			v = t
		}
	}
	var st any
	if stars != nil {
		st = *stars
	}
	if err := s.requireTrack(ctx, trackID); err != nil {
		return TrackRating{}, err
	}
	source := "agent"
	if p.ActsAsHuman() {
		source = "human"
	}
	now := s.stamp()
	if _, err := s.st.W.ExecContext(ctx, `INSERT INTO ratings(track_id,source,stars,verdict,principal_kind,principal_id,principal_name,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT(track_id,source) DO UPDATE SET stars=excluded.stars, verdict=excluded.verdict,
		principal_kind=excluded.principal_kind, principal_id=excluded.principal_id,
		principal_name=excluded.principal_name, updated_at=excluded.updated_at`,
		trackID, source, st, v, string(p.Kind), p.Key(), p.Name, now, now); err != nil {
		return TrackRating{}, fmt.Errorf("judgments: rate: %w", err)
	}
	return s.Ratings(ctx, trackID)
}

// Ratings returns both rating rows of a track.
func (s *Service) Ratings(ctx context.Context, trackID string) (TrackRating, error) {
	return s.lib.Ratings(ctx, trackID)
}

// HumanStars returns the human stars of the rated tracks among trackIDs.
func (s *Service) HumanStars(ctx context.Context, trackIDs []string) (map[string]int, error) {
	out := map[string]int{}
	const chunk = 500
	for i := 0; i < len(trackIDs); i += chunk {
		part := trackIDs[i:min(i+chunk, len(trackIDs))]
		args := make([]any, len(part))
		ph := make([]byte, 0, 2*len(part))
		for j, id := range part {
			args[j] = id
			if j > 0 {
				ph = append(ph, ',')
			}
			ph = append(ph, '?')
		}
		rows, err := s.st.R.QueryContext(ctx, `SELECT track_id, stars FROM ratings WHERE source = 'human' AND stars IS NOT NULL AND track_id IN (`+string(ph)+`)`, args...) //nolint:gosec // G202: only "?" placeholders are concatenated
		if err != nil {
			return nil, fmt.Errorf("judgments: human stars: %w", err)
		}
		for rows.Next() {
			var id string
			var n int
			if err := rows.Scan(&id, &n); err != nil {
				_ = rows.Close()
				return nil, err
			}
			out[id] = n
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// UpdateTrack changes a track's title and/or status. The slug and the frozen
// master are untouched; the title change reaches search through the FTS trigger.
func (s *Service) UpdateTrack(ctx context.Context, p auth.Principal, trackID string, title, status *string) error {
	if title == nil && status == nil {
		return inv("body", "give a title and/or a status")
	}
	set, args := []string{}, []any{}
	if title != nil {
		t := strings.TrimSpace(*title)
		if n := utf8.RuneCountInString(t); n < 1 || n > 200 {
			return inv("title", "1 to 200 characters")
		}
		for _, r := range t {
			if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
				return inv("title", "must not contain control characters")
			}
		}
		set, args = append(set, "title = ?"), append(args, t)
	}
	if status != nil {
		switch *status {
		case "draft", "to_relisten", "final", "discarded":
		default:
			return inv("status", "must be draft, to_relisten, final or discarded")
		}
		set, args = append(set, "status = ?", "status_set_by = ?"), append(args, *status, p.Key())
	}
	res, err := s.st.W.ExecContext(ctx, `UPDATE tracks SET `+strings.Join(set, ", ")+` WHERE id = ?`, append(args, trackID)...) //nolint:gosec // G202: fixed column list
	if err != nil {
		return fmt.Errorf("judgments: update track: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return library.ErrNotFound
	}
	return nil
}
