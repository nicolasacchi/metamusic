package crates

import (
	"context"
	"errors"
	"fmt"
)

// errNoCounter is returned when a smart Crate is counted by a Service built without a Counter.
var errNoCounter = errors.New("crates: no counter wired")

// count is the live count of c: a manual Crate's live members, a smart Crate's matches now through the same filter structs
// the list operations use (D-L2-13). Counts are never stored.
func (s *Service) count(ctx context.Context, c Crate) (int, error) {
	if c.Kind == KindManual {
		var n int
		if err := s.st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM crate_items WHERE crate_id = ? AND removed_at IS NULL`, c.ID).Scan(&n); err != nil {
			return 0, fmt.Errorf("crates: count items: %w", err)
		}
		return n, nil
	}
	if s.lib == nil {
		return 0, errNoCounter
	}
	f, err := ParseFilter(c.Subject, c.Filter)
	if err != nil {
		return 0, err
	}
	if c.Subject == SubjectSong {
		return s.lib.CountSongs(ctx, f.Songs())
	}
	return s.lib.CountTracks(ctx, f.Tracks())
}

// Warnings lists what is off about a smart Crate's saved filter: ids it names that no longer exist. Such a filter matches
// nothing; it is never an error.
func (s *Service) Warnings(ctx context.Context, c Crate) ([]string, error) {
	out := []string{}
	if c.Kind != KindSmart {
		return out, nil
	}
	f, err := ParseFilter(c.Subject, c.Filter)
	if err != nil {
		return append(out, "the saved filter no longer parses"), nil //nolint:nilerr // a warning, not a failure: the Crate stays readable
	}
	check := func(field, table, id string) error {
		if id == "" {
			return nil
		}
		var n int
		if err := s.st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE id = ?`, id).Scan(&n); err != nil { //nolint:gosec // table is a fixed literal
			return fmt.Errorf("crates: probe %s: %w", table, err)
		}
		if n == 0 {
			out = append(out, field+" names a "+table[:len(table)-1]+" that no longer exists; the filter matches nothing")
		}
		return nil
	}
	if c.Subject == SubjectSong {
		if err := check("muse_id", "muses", f.Songs().MuseID); err != nil {
			return nil, err
		}
		if err := check("session_id", "sessions", f.Songs().SessionID); err != nil {
			return nil, err
		}
	} else if err := check("session_id", "sessions", f.Tracks().SessionID); err != nil {
		return nil, err
	}
	return out, nil
}
