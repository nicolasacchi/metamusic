package songview

import (
	"context"
	"fmt"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// coverageSQL groups the live, non-hidden, non-archived Songs of the given Muses by Muse over the shared state SQL
// (C90.4); archived drafts are left out so the counts equal list_songs' defaults.
func coverageSQL(n int) string {
	return `SELECT s.muse_id, COUNT(*),
		COALESCE(SUM(` + store.SongStateCase + ` = 'draft'), 0), COALESCE(SUM(` + store.SongStateCase + ` = 'rendered'), 0),
		COALESCE(SUM(` + store.SongStateCase + ` = 'judged'), 0), COALESCE(SUM(` + store.SongStateCase + ` = 'picked'), 0),
		COALESCE(SUM(st.queued_jobs), 0)
		FROM songs s JOIN (` + store.SongStateSQL + `) st ON st.song_id = s.id
		WHERE s.muse_id IN (` + strings.TrimSuffix(strings.Repeat("?,", n), ",") + `) AND s.retired_at IS NULL AND s.hidden_at IS NULL AND st.archived = 0
		GROUP BY s.muse_id`
}

func (s *Service) coverage(ctx context.Context, ids []string) (map[string]Coverage, error) {
	out := make(map[string]Coverage, len(ids))
	const chunk = 400
	for i := 0; i < len(ids); i += chunk {
		part := ids[i:min(i+chunk, len(ids))]
		args := make([]any, len(part))
		for j, id := range part {
			args[j] = id
		}
		rows, err := s.st.R.QueryContext(ctx, coverageSQL(len(part)), args...)
		if err != nil {
			return nil, fmt.Errorf("songview: coverage: %w", err)
		}
		for rows.Next() {
			var id string
			var c Coverage
			if err := rows.Scan(&id, &c.Songs, &c.Draft, &c.Rendered, &c.Judged, &c.Picked, &c.QueuedJobs); err != nil {
				_ = rows.Close()
				return nil, err
			}
			out[id] = c
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// MuseSummaries adds the coverage counts to a page of Muses with one grouped query.
func (s *Service) MuseSummaries(ctx context.Context, ms []muses.Muse) ([]MuseSummary, error) {
	ids := make([]string, len(ms))
	for i, m := range ms {
		ids[i] = m.ID
	}
	cov := map[string]Coverage{}
	if len(ids) > 0 {
		var err error
		if cov, err = s.coverage(ctx, ids); err != nil {
			return nil, err
		}
	}
	out := make([]MuseSummary, len(ms))
	for i, m := range ms {
		out[i] = MuseSummary{Muse: m, Coverage: cov[m.ID]}
	}
	return out, nil
}

// MuseCoverage is the coverage of one Muse.
func (s *Service) MuseCoverage(ctx context.Context, museID string) (Coverage, error) {
	cov, err := s.coverage(ctx, []string{museID})
	return cov[museID], err
}

// AngleCount is the Songs of a Muse per angle (nil = no angle).
type AngleCount struct {
	Angle *string
	Songs int
}

// MuseAngles counts the live, non-hidden, non-archived Songs of a Muse per angle, most Songs first, then by name,
// the angle-less group last among equals.
func (s *Service) MuseAngles(ctx context.Context, museID string) ([]AngleCount, error) {
	q := "" + //nolint:gosec // G202: fragments are fixed strings built in this function
		`SELECT s.angle, COUNT(*) FROM songs s JOIN (` + store.SongStateSQL + `) st ON st.song_id = s.id
		WHERE s.muse_id = ? AND s.retired_at IS NULL AND s.hidden_at IS NULL AND st.archived = 0
		GROUP BY s.angle ORDER BY COUNT(*) DESC, s.angle IS NULL, s.angle`
	rows, err := s.st.R.QueryContext(ctx, q, museID)
	if err != nil {
		return nil, fmt.Errorf("songview: angles: %w", err)
	}
	defer rows.Close()
	out := []AngleCount{}
	for rows.Next() {
		var a AngleCount
		var angle *string
		if err := rows.Scan(&angle, &a.Songs); err != nil {
			return nil, err
		}
		a.Angle = angle
		out = append(out, a)
	}
	return out, rows.Err()
}
