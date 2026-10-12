package library

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// Stats is the library overview.
type Stats struct {
	Tracks    TrackStats     `json:"tracks"`
	Backlog   BacklogStats   `json:"backlog"`
	Recipes   RecipeStats    `json:"recipes"`
	Loudness  LoudnessStats  `json:"loudness"`
	Bandwidth BandwidthStats `json:"bandwidth"`
	Storage   StorageStats   `json:"storage"`
	Imports   ImportStats    `json:"imports"`
}

// TrackStats counts tracks.
type TrackStats struct {
	Total     int            `json:"total"`
	ByStatus  map[string]int `json:"by_status"`
	ByOrigin  map[string]int `json:"by_origin"`
	DurationS float64        `json:"duration_s_total"`
	WithScore int            `json:"with_score"`
	Timed     int            `json:"timed"`
	Truncated int            `json:"truncated"`
}

// CollectionCount is one collection with its draft count.
type CollectionCount struct {
	Name   string `json:"name"`
	Drafts int    `json:"drafts"`
}

// BacklogStats counts drafts.
type BacklogStats struct {
	Drafts      int               `json:"drafts" doc:"Drafts still in the backlog"`
	Fulfilled   int               `json:"fulfilled"`
	Groups      int               `json:"groups" doc:"Distinct groups among backlog drafts"`
	Collections []CollectionCount `json:"collections" nullable:"false"`
}

// RecipeStats counts recipes.
type RecipeStats struct {
	Total   int `json:"total"`
	Sources int `json:"sources"`
}

// LoudnessStats summarises integrated loudness across tracks.
type LoudnessStats struct {
	N                     int      `json:"n"`
	IntegratedLUFSMin     *float64 `json:"integrated_lufs_min"`
	IntegratedLUFSMax     *float64 `json:"integrated_lufs_max"`
	SpreadLU              *float64 `json:"spread_lu"`
	ListenFlacRecommended bool     `json:"listen_flac_recommended" doc:"Spread above 2 LU"`
}

// BandwidthStats summarises measured bandwidth.
type BandwidthStats struct {
	N        int  `json:"n"`
	MinHz    *int `json:"min_hz"`
	MedianHz *int `json:"median_hz"`
	MaxHz    *int `json:"max_hz"`
}

// StorageStats sums stored bytes.
type StorageStats struct {
	MastersBytes int64 `json:"masters_bytes"`
	DerivedBytes int64 `json:"derived_bytes"`
}

// ImportStats summarises import runs.
type ImportStats struct {
	Runs      int     `json:"runs"`
	LastRunAt *string `json:"last_run_at"`
	LastState *string `json:"last_state"`
}

func (s *Service) countBy(ctx context.Context, q string) (map[string]int, error) {
	rows, err := s.st.R.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		m[k] = n
	}
	return m, rows.Err()
}

// Stats computes the overview in a handful of aggregate queries.
func (s *Service) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	r := s.st.R
	var err error
	if st.Tracks.ByStatus, err = s.countBy(ctx, `SELECT status, COUNT(*) FROM tracks GROUP BY status`); err != nil {
		return st, err
	}
	if st.Tracks.ByOrigin, err = s.countBy(ctx, `SELECT origin, COUNT(*) FROM tracks GROUP BY origin`); err != nil {
		return st, err
	}
	if err := r.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(duration_s),0),
		COALESCE(SUM(EXISTS(SELECT 1 FROM scores s WHERE s.track_id = t.id)),0),
		COALESCE(SUM(EXISTS(SELECT 1 FROM sections c WHERE c.track_id = t.id AND c.source = 'plan')),0),
		COALESCE(SUM(COALESCE(truncated_abc,0) = 1 OR COALESCE(truncated_semantic,0) = 1),0),
		COALESCE(SUM(master_bytes),0) FROM tracks t`).
		Scan(&st.Tracks.Total, &st.Tracks.DurationS, &st.Tracks.WithScore, &st.Tracks.Timed, &st.Tracks.Truncated, &st.Storage.MastersBytes); err != nil {
		return st, fmt.Errorf("library: stats tracks: %w", err)
	}
	if err := r.QueryRowContext(ctx, `SELECT COALESCE(SUM(bytes),0) FROM (SELECT DISTINCT sha256, bytes FROM renditions)`).Scan(&st.Storage.DerivedBytes); err != nil {
		return st, err
	}
	if err := r.QueryRowContext(ctx, `SELECT COALESCE(SUM(status='backlog'),0), COALESCE(SUM(status='fulfilled'),0),
		COUNT(DISTINCT CASE WHEN status='backlog' THEN COALESCE(group_key, request_id) END) FROM recipe_drafts`).
		Scan(&st.Backlog.Drafts, &st.Backlog.Fulfilled, &st.Backlog.Groups); err != nil {
		return st, fmt.Errorf("library: stats drafts: %w", err)
	}
	st.Backlog.Collections = []CollectionCount{}
	rows, err := r.QueryContext(ctx, `SELECT name, COUNT(*) FROM draft_collections GROUP BY name ORDER BY name`)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var c CollectionCount
		if err := rows.Scan(&c.Name, &c.Drafts); err != nil {
			_ = rows.Close()
			return st, err
		}
		st.Backlog.Collections = append(st.Backlog.Collections, c)
	}
	if err := rows.Close(); err != nil {
		return st, err
	}
	if err := r.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM recipes), (SELECT COUNT(*) FROM recipe_sources)`).Scan(&st.Recipes.Total, &st.Recipes.Sources); err != nil {
		return st, err
	}
	lufs, err := s.latestMetric(ctx, "loudness", "integrated_lufs")
	if err != nil {
		return st, err
	}
	st.Loudness.N = len(lufs)
	if len(lufs) > 0 {
		sort.Float64s(lufs)
		lo, hi := lufs[0], lufs[len(lufs)-1]
		spread := hi - lo
		st.Loudness.IntegratedLUFSMin, st.Loudness.IntegratedLUFSMax, st.Loudness.SpreadLU = &lo, &hi, &spread
		st.Loudness.ListenFlacRecommended = spread > 2.0
	}
	bw, err := s.latestMetric(ctx, "spectrum", "bandwidth_hz")
	if err != nil {
		return st, err
	}
	st.Bandwidth.N = len(bw)
	if len(bw) > 0 {
		sort.Float64s(bw)
		lo, med, hi := int(bw[0]), int(bw[len(bw)/2]), int(bw[len(bw)-1])
		st.Bandwidth.MinHz, st.Bandwidth.MedianHz, st.Bandwidth.MaxHz = &lo, &med, &hi
	}
	var last, state sql.NullString
	if err := r.QueryRowContext(ctx, `SELECT COUNT(*) FROM import_runs`).Scan(&st.Imports.Runs); err != nil {
		return st, err
	}
	switch err := r.QueryRowContext(ctx, `SELECT started_at, state FROM import_runs ORDER BY started_at DESC, id DESC LIMIT 1`).Scan(&last, &state); {
	case err == nil:
		st.Imports.LastRunAt, st.Imports.LastState = ptrStr(last), ptrStr(state)
	case err != sql.ErrNoRows:
		return st, err
	}
	return st, nil
}

// latestMetric returns the non-null numeric metric of each track's newest analysis row.
func (s *Service) latestMetric(ctx context.Context, analyzer, key string) ([]float64, error) {
	rows, err := s.st.R.QueryContext(ctx, `SELECT json_extract((SELECT a.metrics_json FROM analyses a WHERE a.track_id = t.id AND a.analyzer = ?
		ORDER BY a.created_at DESC, a.version DESC LIMIT 1), '$.'||?) FROM tracks t`, analyzer, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []float64
	for rows.Next() {
		var v sql.NullFloat64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		if v.Valid {
			out = append(out, v.Float64)
		}
	}
	return out, rows.Err()
}
