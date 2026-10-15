package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/store"
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

	// M2a (C38)
	Judgments      JudgmentStats  `json:"judgments"`
	Playlists      int            `json:"playlists"`
	DraftsByOrigin map[string]int `json:"drafts_by_origin"`
	Backups        BackupStats    `json:"backups"`

	// M3 (C81.1)
	Generation GenerationStats `json:"generation"`
	Breakdown  Breakdown       `json:"breakdown"`

	// L1 (C98)
	LibraryModel *LibraryModel `json:"library_model" doc:"Songs, Muses and Sessions; null on a pre-L1 schema"`
}

// LibraryModel counts the live, non-hidden rows of the L1 model (C98). Retired and hidden Songs, archived
// drafts' Songs, hidden Muses and hidden Sessions are not counted; Pending is the length of the provenance queue.
type LibraryModel struct {
	Songs           SongStateCounts `json:"songs"`
	Muses           int             `json:"muses"`
	Sessions        int             `json:"sessions"`
	TaxonomyVersion int             `json:"taxonomy_version" doc:"Newest Sound taxonomy version; 0 when none exists yet"`
	Pending         int             `json:"pending" doc:"Entries waiting in the provenance queue"`
}

// SongStateCounts is the number of Songs per state (C90.4).
type SongStateCounts struct {
	Total    int `json:"total"`
	Draft    int `json:"draft"`
	Rendered int `json:"rendered"`
	Judged   int `json:"judged"`
	Picked   int `json:"picked"`
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
	st := Stats{Generation: GenerationStats{JobsByState: map[string]int{}, SpendMonthUSD: "0", OutstandingUSD: "0"},
		Breakdown:      Breakdown{ByStyleTerm: []BreakdownTerm{}, BySeed: []BreakdownSeed{}},
		DraftsByOrigin: map[string]int{}, Judgments: JudgmentStats{StarsHist: map[string]int{"1": 0, "2": 0, "3": 0, "4": 0, "5": 0}}}
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
	if err := s.judgmentStats(ctx, &st); err != nil {
		return st, err
	}
	if err := s.generationStats(ctx, &st); err != nil {
		return st, err
	}
	if st.LibraryModel, err = s.libraryModel(ctx); err != nil {
		return st, err
	}
	return st, nil
}

// libraryModel counts the L1 model; nil on a schema without Songs.
func (s *Service) libraryModel(ctx context.Context) (*LibraryModel, error) {
	has, err := s.hasTableDB(ctx, "songs")
	if err != nil || !has {
		return nil, err
	}
	m := &LibraryModel{}
	if err := s.st.R.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(c = 'draft'),0), COALESCE(SUM(c = 'rendered'),0),
		COALESCE(SUM(c = 'judged'),0), COALESCE(SUM(c = 'picked'),0)
		FROM (SELECT `+store.SongStateCase+` AS c FROM (`+store.SongStateSQL+`) st JOIN songs g ON g.id = st.song_id
		      WHERE g.hidden_at IS NULL AND g.retired_at IS NULL AND st.archived = 0)`).
		Scan(&m.Songs.Total, &m.Songs.Draft, &m.Songs.Rendered, &m.Songs.Judged, &m.Songs.Picked); err != nil {
		return nil, fmt.Errorf("library: stats songs: %w", err)
	}
	if s.museCount != nil { // Muse tables are read only by internal/muses (C99.2): the count arrives through a seam
		if m.Muses, err = s.museCount(ctx); err != nil {
			return nil, fmt.Errorf("library: stats muses: %w", err)
		}
	}
	if err := s.st.R.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM sessions WHERE hidden_at IS NULL),
		COALESCE((SELECT MAX(version) FROM sound_taxonomy_versions), 0),
		(SELECT COUNT(*) FROM provenance_dirty)`).Scan(&m.Sessions, &m.TaxonomyVersion, &m.Pending); err != nil {
		return nil, fmt.Errorf("library: stats model: %w", err)
	}
	return m, nil
}

// monthBoundsOf is the calendar month of now in the process time zone.
func monthBoundsOf(now time.Time) (from, to time.Time) {
	l := now.In(time.Local)
	m0 := time.Date(l.Year(), l.Month(), 1, 0, 0, 0, 0, time.Local)
	return m0, m0.AddDate(0, 1, 0)
}

// generationStats fills the generation block and the Phase-3 breakdown (C81.1). Reference tracks (stub
// recipes, engine unknown) are excluded from both lists: their seed 0 is a placeholder (D-M3-34).
func (s *Service) generationStats(ctx context.Context, st *Stats) error {
	r := s.st.R
	var err error
	if st.Generation.JobsByState, err = s.countBy(ctx, `SELECT state, COUNT(*) FROM jobs GROUP BY state`); err != nil {
		return fmt.Errorf("library: stats jobs: %w", err)
	}
	from, to := monthBoundsOf(s.now())
	m0, m1 := store.FormatTime(from), store.FormatTime(to)
	if err := r.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracks WHERE origin = 'job' AND created_at >= ? AND created_at < ?`,
		ts(from), ts(to)).Scan(&st.Generation.TakesMonth); err != nil {
		return fmt.Errorf("library: stats takes: %w", err)
	}
	var settled, outstanding int64
	if err := r.QueryRowContext(ctx, `SELECT COALESCE(SUM(micro_usd), 0) FROM ledger
		WHERE (kind = 'settle' AND at >= ?1 AND at < ?2) OR (kind = 'bill' AND window_start >= ?1 AND window_start < ?2)`, m0, m1).Scan(&settled); err != nil {
		return fmt.Errorf("library: stats spend: %w", err)
	}
	if err := r.QueryRowContext(ctx, `SELECT COALESCE(SUM(v), 0) FROM (SELECT MAX(0, SUM(CASE kind WHEN 'reserve' THEN micro_usd WHEN 'settle' THEN -micro_usd
		WHEN 'release' THEN -micro_usd ELSE 0 END)) AS v FROM ledger WHERE batch_id IS NOT NULL GROUP BY batch_id)`).Scan(&outstanding); err != nil {
		return fmt.Errorf("library: stats outstanding: %w", err)
	}
	st.Generation.SpendMonthMicroUSD, st.Generation.SpendMonthUSD = settled, money.MicroUSD(settled).USD()
	st.Generation.OutstandingMicroUSD, st.Generation.OutstandingUSD = outstanding, money.MicroUSD(outstanding).USD()

	terms, err := r.QueryContext(ctx, `SELECT x.term, COUNT(DISTINCT t.id), COUNT(h.stars), AVG(h.stars) FROM track_style_terms x
		JOIN tracks t ON t.id = x.track_id JOIN recipes rc ON rc.id = t.recipe_id AND rc.engine <> 'unknown'
		LEFT JOIN ratings h ON h.track_id = t.id AND h.source = 'human' AND h.stars IS NOT NULL
		GROUP BY x.term ORDER BY COUNT(DISTINCT t.id) DESC, x.term LIMIT 30`)
	if err != nil {
		return fmt.Errorf("library: stats terms: %w", err)
	}
	for terms.Next() {
		var b BreakdownTerm
		var mean sql.NullFloat64
		if err := terms.Scan(&b.Term, &b.Tracks, &b.Rated, &mean); err != nil {
			_ = terms.Close()
			return err
		}
		b.MeanStars = meanOf(mean)
		st.Breakdown.ByStyleTerm = append(st.Breakdown.ByStyleTerm, b)
	}
	if err := terms.Close(); err != nil {
		return err
	}
	seeds, err := r.QueryContext(ctx, `SELECT rc.seed, COUNT(t.id), COUNT(h.stars), AVG(h.stars) FROM tracks t JOIN recipes rc ON rc.id = t.recipe_id AND rc.engine <> 'unknown'
		LEFT JOIN ratings h ON h.track_id = t.id AND h.source = 'human' AND h.stars IS NOT NULL
		GROUP BY rc.seed HAVING COUNT(t.id) >= 2 ORDER BY COUNT(t.id) DESC, rc.seed LIMIT 30`)
	if err != nil {
		return fmt.Errorf("library: stats seeds: %w", err)
	}
	defer seeds.Close()
	for seeds.Next() {
		var b BreakdownSeed
		var mean sql.NullFloat64
		if err := seeds.Scan(&b.Seed, &b.Tracks, &b.Rated, &mean); err != nil {
			return err
		}
		b.MeanStars = meanOf(mean)
		st.Breakdown.BySeed = append(st.Breakdown.BySeed, b)
	}
	return seeds.Err()
}

// meanOf rounds a mean of stars to two decimals; nil when nothing was rated.
func meanOf(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	v := math.Round(n.Float64*100) / 100
	return &v
}

// judgmentStats fills the M2a counters of Stats (C38).
func (s *Service) judgmentStats(ctx context.Context, st *Stats) error {
	r := s.st.R
	if err := r.QueryRowContext(ctx, `SELECT COALESCE(SUM(source = 'human' AND stars IS NOT NULL), 0), COALESCE(SUM(source = 'agent' AND stars IS NOT NULL), 0) FROM ratings`).
		Scan(&st.Judgments.RatedHuman, &st.Judgments.RatedCritic); err != nil {
		return fmt.Errorf("library: stats ratings: %w", err)
	}
	hist, err := s.countBy(ctx, `SELECT CAST(stars AS TEXT), COUNT(*) FROM ratings WHERE source = 'human' AND stars IS NOT NULL GROUP BY stars`)
	if err != nil {
		return err
	}
	for k, n := range hist {
		st.Judgments.StarsHist[k] = n
	}
	if err := r.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(state = 'open' AND kind = 'fix'), 0) FROM comments WHERE hidden_at IS NULL`).
		Scan(&st.Judgments.Comments, &st.Judgments.OpenFix); err != nil {
		return fmt.Errorf("library: stats comments: %w", err)
	}
	if err := r.QueryRowContext(ctx, `SELECT COUNT(DISTINCT tag_id) FROM track_tags WHERE removed_at IS NULL`).Scan(&st.Judgments.Tags); err != nil {
		return fmt.Errorf("library: stats tags: %w", err)
	}
	if err := r.QueryRowContext(ctx, `SELECT COUNT(*) FROM playlists WHERE hidden_at IS NULL`).Scan(&st.Playlists); err != nil {
		return fmt.Errorf("library: stats playlists: %w", err)
	}
	if st.DraftsByOrigin, err = s.countBy(ctx, `SELECT origin, COUNT(*) FROM recipe_drafts GROUP BY origin`); err != nil {
		return err
	}
	var done, state sql.NullString
	switch err := r.QueryRowContext(ctx, `SELECT state FROM backup_runs ORDER BY started_at DESC, id DESC LIMIT 1`).Scan(&state); {
	case err == nil:
		st.Backups.LastState = ptrStr(state)
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("library: stats backups: %w", err)
	}
	switch err := r.QueryRowContext(ctx, `SELECT finished_at FROM backup_runs WHERE state = 'completed' ORDER BY finished_at DESC, id DESC LIMIT 1`).Scan(&done); {
	case err == nil:
		st.Backups.LastCompletedAt = ptrStr(done)
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("library: stats backups: %w", err)
	}
	return nil
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
