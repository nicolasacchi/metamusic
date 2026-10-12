package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
)

// TrackSummary is one row of the library list.
type TrackSummary struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Status         string   `json:"status"`
	DurationS      float64  `json:"duration_s"`
	RecordedAt     string   `json:"recorded_at" doc:"RFC 3339 UTC"`
	CreatedAt      string   `json:"created_at" doc:"RFC 3339 UTC"`
	RequestID      string   `json:"request_id"`
	Seed           int64    `json:"seed"`
	StyleTerms     []string `json:"style_terms" nullable:"false"`
	BPMDeclared    *int     `json:"bpm_declared" doc:"BPM declared in the style text; null when absent"`
	HasScore       bool     `json:"has_score"`
	Timed          bool     `json:"timed" doc:"Section times are known (aligned score)"`
	Truncated      *bool    `json:"truncated" doc:"abc or semantic stream truncated; null when unknown"`
	IntegratedLUFS *float64 `json:"integrated_lufs"`
	BandwidthHz    *int     `json:"bandwidth_hz"`
	RootTrackID    string   `json:"root_track_id"`
	ParentTrackID  *string  `json:"parent_track_id"`
	ChildrenCount  int      `json:"children_count"`
}

// TrackPage is one page of search results.
type TrackPage struct {
	Items      []TrackSummary `json:"items" nullable:"false"`
	Total      int            `json:"total"`
	NextCursor *string        `json:"next_cursor"`
}

// SearchParams are the filters of SearchTracks.
type SearchParams struct {
	Q                          string
	Status                     []string
	Style                      []string
	MinDurationS, MaxDurationS *float64
	RecordedFrom, RecordedTo   string
	HasScore, Truncated        *bool
	RootTrackID                string
	Sort                       string
	Limit                      int
	Cursor                     string
}

var trackStatuses = []string{"draft", "to_relisten", "final", "discarded"}

var trackSorts = []string{"recorded_desc", "recorded_asc", "duration_desc", "duration_asc", "title_asc", "relevance"}

// parseWhen accepts an RFC 3339 date-time or a plain date. A date as an upper
// bound covers the whole day.
func parseWhen(field, v string, upper bool) (string, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return ts(t), nil
	}
	t, err := time.Parse("2006-01-02", v)
	if err != nil {
		return "", invalid(field, "want an RFC 3339 date or date-time")
	}
	if upper {
		t = t.AddDate(0, 0, 1)
	}
	return ts(t), nil
}

const summarySelect = `SELECT t.id, t.title, t.status, t.duration_s, t.recorded_at, t.created_at, r.request_id, r.seed, r.style, t.bpm_declared,
  EXISTS(SELECT 1 FROM scores s WHERE s.track_id = t.id),
  EXISTS(SELECT 1 FROM sections c WHERE c.track_id = t.id AND c.source = 'plan'),
  CASE WHEN t.truncated_abc IS NULL AND t.truncated_semantic IS NULL THEN NULL
       WHEN COALESCE(t.truncated_abc,0) = 1 OR COALESCE(t.truncated_semantic,0) = 1 THEN 1 ELSE 0 END,
  (SELECT json_extract(a.metrics_json, '$.integrated_lufs') FROM analyses a WHERE a.track_id = t.id AND a.analyzer = 'loudness' ORDER BY a.created_at DESC, a.version DESC LIMIT 1),
  (SELECT json_extract(a.metrics_json, '$.bandwidth_hz') FROM analyses a WHERE a.track_id = t.id AND a.analyzer = 'spectrum' ORDER BY a.created_at DESC, a.version DESC LIMIT 1),
  t.root_track_id, t.parent_track_id,
  (SELECT COUNT(*) FROM tracks c WHERE c.parent_track_id = t.id)`

// SearchTracks lists tracks with FTS and filters. User text never reaches
// FTS5 syntax.
func (s *Service) SearchTracks(ctx context.Context, p SearchParams) (TrackPage, error) {
	off, err := decodeCursor(p.Cursor)
	if err != nil {
		return TrackPage{}, err
	}
	for _, st := range p.Status {
		if !oneOf(st, trackStatuses...) {
			return TrackPage{}, invalid("status", "unknown status %q", st)
		}
	}
	if len(p.Style) > 5 {
		return TrackPage{}, invalid("style", "at most 5 terms")
	}
	if p.Sort != "" && !oneOf(p.Sort, trackSorts...) {
		return TrackPage{}, invalid("sort", "unknown sort %q", p.Sort)
	}
	fts := ftsQuery(p.Q)
	sort := p.Sort
	if sort == "" || (sort == "relevance" && fts == "") {
		sort = "recorded_desc"
		if fts != "" {
			sort = "relevance"
		}
	}
	limit := clampLimit(p.Limit, 50, 100)

	from := ` FROM tracks t JOIN recipes r ON r.id = t.recipe_id`
	var args []any
	if fts != "" {
		from += ` JOIN (SELECT track_id, bm25(tracks_fts, 0, 10, 2, 2, 1) AS rank FROM tracks_fts WHERE tracks_fts MATCH ?) f ON f.track_id = t.id`
		args = append(args, fts)
	}
	where := ` WHERE 1=1`
	if len(p.Status) > 0 {
		where += ` AND t.status IN (` + placeholders(len(p.Status)) + `)`
		for _, st := range p.Status {
			args = append(args, st)
		}
	}
	for _, term := range p.Style {
		where += ` AND EXISTS (SELECT 1 FROM track_style_terms x WHERE x.track_id = t.id AND x.term = ?)`
		args = append(args, normTerm(term))
	}
	if p.MinDurationS != nil {
		where += ` AND t.duration_s >= ?`
		args = append(args, *p.MinDurationS)
	}
	if p.MaxDurationS != nil {
		where += ` AND t.duration_s <= ?`
		args = append(args, *p.MaxDurationS)
	}
	if p.RecordedFrom != "" {
		v, err := parseWhen("recorded_from", p.RecordedFrom, false)
		if err != nil {
			return TrackPage{}, err
		}
		where += ` AND t.recorded_at >= ?`
		args = append(args, v)
	}
	if p.RecordedTo != "" {
		v, err := parseWhen("recorded_to", p.RecordedTo, true)
		if err != nil {
			return TrackPage{}, err
		}
		if len(p.RecordedTo) == len("2006-01-02") {
			where += ` AND t.recorded_at < ?`
		} else {
			where += ` AND t.recorded_at <= ?`
		}
		args = append(args, v)
	}
	if p.HasScore != nil {
		if *p.HasScore {
			where += ` AND EXISTS (SELECT 1 FROM scores s WHERE s.track_id = t.id)`
		} else {
			where += ` AND NOT EXISTS (SELECT 1 FROM scores s WHERE s.track_id = t.id)`
		}
	}
	if p.Truncated != nil {
		cond := `(COALESCE(t.truncated_abc,0) = 1 OR COALESCE(t.truncated_semantic,0) = 1)`
		if !*p.Truncated {
			cond = `NOT ` + cond
		}
		where += ` AND ` + cond
	}
	if p.RootTrackID != "" {
		where += ` AND t.root_track_id = ?`
		args = append(args, p.RootTrackID)
	}
	order := map[string]string{
		"recorded_desc": `t.recorded_at DESC, t.id DESC`,
		"recorded_asc":  `t.recorded_at ASC, t.id ASC`,
		"duration_desc": `t.duration_s DESC, t.id DESC`,
		"duration_asc":  `t.duration_s ASC, t.id ASC`,
		"title_asc":     `t.title COLLATE NOCASE ASC, t.id ASC`,
		"relevance":     `f.rank ASC, t.id ASC`,
	}[sort]

	var total int
	if err := s.st.R.QueryRowContext(ctx, `SELECT COUNT(*)`+from+where, args...).Scan(&total); err != nil {
		return TrackPage{}, fmt.Errorf("library: count tracks: %w", err)
	}
	rows, err := s.st.R.QueryContext(ctx, summarySelect+from+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`, append(args, limit, off)...)
	if err != nil {
		return TrackPage{}, fmt.Errorf("library: search tracks: %w", err)
	}
	defer rows.Close()
	page := TrackPage{Items: []TrackSummary{}, Total: total}
	var pg pager
	more := false
	for rows.Next() {
		sum, err := scanSummary(rows)
		if err != nil {
			return TrackPage{}, err
		}
		if !pg.add(sizeOf(sum)) {
			more = true
			break
		}
		page.Items = append(page.Items, sum)
	}
	if err := rows.Err(); err != nil {
		return TrackPage{}, err
	}
	next := off + len(page.Items)
	if more || next < total {
		c := encodeCursor(next)
		page.NextCursor = &c
	}
	return page, nil
}

func placeholders(n int) string {
	b := make([]byte, 0, 2*n)
	for i := 0; i < n; i++ {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '?')
	}
	return string(b)
}

func normTerm(t string) string {
	terms, _ := yue2.StyleTerms(t)
	if len(terms) == 1 {
		return terms[0]
	}
	return t
}

func scanSummary(rows *sql.Rows) (TrackSummary, error) {
	var (
		t                   TrackSummary
		style               string
		bpm, bw             sql.NullInt64
		trunc               sql.NullInt64
		lufs                sql.NullFloat64
		parent              sql.NullString
		hasScore, timedFlag bool
	)
	if err := rows.Scan(&t.ID, &t.Title, &t.Status, &t.DurationS, &t.RecordedAt, &t.CreatedAt, &t.RequestID, &t.Seed, &style, &bpm,
		&hasScore, &timedFlag, &trunc, &lufs, &bw, &t.RootTrackID, &parent, &t.ChildrenCount); err != nil {
		return t, fmt.Errorf("library: scan track: %w", err)
	}
	t.HasScore, t.Timed = hasScore, timedFlag
	terms, _ := yue2.StyleTerms(style)
	if terms == nil {
		terms = []string{}
	}
	t.StyleTerms = terms
	if bpm.Valid {
		v := int(bpm.Int64)
		t.BPMDeclared = &v
	}
	if trunc.Valid {
		v := trunc.Int64 == 1
		t.Truncated = &v
	}
	if lufs.Valid {
		t.IntegratedLUFS = &lufs.Float64
	}
	if bw.Valid {
		v := int(bw.Int64)
		t.BandwidthHz = &v
	}
	if parent.Valid {
		t.ParentTrackID = &parent.String
	}
	return t, nil
}

// TermCount is one style facet.
type TermCount struct {
	Term  string `json:"term"`
	Count int    `json:"count"`
}

// StatusCount is one status facet.
type StatusCount struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}

// Range is a min/max pair; both null on an empty library.
type Range struct {
	Min *float64 `json:"min"`
	Max *float64 `json:"max"`
}

// TimeRange is a recorded_at min/max pair.
type TimeRange struct {
	Min *string `json:"min"`
	Max *string `json:"max"`
}

// Facets feeds the library filters.
type Facets struct {
	StyleTerms []TermCount   `json:"style_terms" nullable:"false"`
	Statuses   []StatusCount `json:"statuses" nullable:"false"`
	DurationS  Range         `json:"duration_s"`
	RecordedAt TimeRange     `json:"recorded_at"`
}

// Facets returns the top 50 style terms, status counts and ranges.
func (s *Service) Facets(ctx context.Context) (Facets, error) {
	f := Facets{StyleTerms: []TermCount{}, Statuses: []StatusCount{}}
	rows, err := s.st.R.QueryContext(ctx, `SELECT term, COUNT(*) c FROM track_style_terms GROUP BY term ORDER BY c DESC, term ASC LIMIT 50`)
	if err != nil {
		return f, err
	}
	for rows.Next() {
		var tc TermCount
		if err := rows.Scan(&tc.Term, &tc.Count); err != nil {
			_ = rows.Close()
			return f, err
		}
		f.StyleTerms = append(f.StyleTerms, tc)
	}
	if err := rows.Close(); err != nil {
		return f, err
	}
	rows, err = s.st.R.QueryContext(ctx, `SELECT status, COUNT(*) FROM tracks GROUP BY status ORDER BY status`)
	if err != nil {
		return f, err
	}
	for rows.Next() {
		var sc StatusCount
		if err := rows.Scan(&sc.Status, &sc.Count); err != nil {
			_ = rows.Close()
			return f, err
		}
		f.Statuses = append(f.Statuses, sc)
	}
	if err := rows.Close(); err != nil {
		return f, err
	}
	var dmin, dmax sql.NullFloat64
	var rmin, rmax sql.NullString
	if err := s.st.R.QueryRowContext(ctx, `SELECT MIN(duration_s), MAX(duration_s), MIN(recorded_at), MAX(recorded_at) FROM tracks`).Scan(&dmin, &dmax, &rmin, &rmax); err != nil {
		return f, err
	}
	if dmin.Valid {
		f.DurationS = Range{&dmin.Float64, &dmax.Float64}
		f.RecordedAt = TimeRange{&rmin.String, &rmax.String}
	}
	return f, nil
}

// ---- Track detail ----

// MP3Info describes the MP3 rendition inside TrackDetail.
type MP3Info struct {
	SHA256 string  `json:"sha256"`
	Bytes  int64   `json:"bytes"`
	GainDB float64 `json:"gain_db"`
}

// AudioInfo is the frozen master's stream data.
type AudioInfo struct {
	SampleRate    int      `json:"sample_rate"`
	BitsPerSample int      `json:"bits_per_sample"`
	Channels      int      `json:"channels"`
	TotalSamples  int64    `json:"total_samples"`
	MasterSHA256  string   `json:"master_sha256"`
	MasterBytes   int64    `json:"master_bytes"`
	AudioMD5      string   `json:"audio_md5"`
	MP3           *MP3Info `json:"mp3" doc:"MP3 rendition; null when missing"`
}

// RecipeInfo is the generating recipe.
type RecipeInfo struct {
	ID           string `json:"id"`
	Engine       string `json:"engine"`
	RequestID    string `json:"request_id"`
	Style        string `json:"style"`
	Lyrics       string `json:"lyrics"`
	Cot          string `json:"cot"`
	Seed         int64  `json:"seed"`
	Hash         string `json:"hash"`
	SourcesCount int    `json:"sources_count"`
}

// Sibling is another take of the same lineage root.
type Sibling struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Seed       int64  `json:"seed"`
	RecordedAt string `json:"recorded_at"`
}

// Lineage places a track in its family.
type Lineage struct {
	RootTrackID   string    `json:"root_track_id"`
	ParentTrackID *string   `json:"parent_track_id"`
	Generation    int       `json:"generation"`
	Operator      *string   `json:"operator"`
	Inferred      *bool     `json:"inferred"`
	Rule          *string   `json:"rule"`
	Siblings      []Sibling `json:"siblings" nullable:"false"`
}

// Truncation reports YuE2 stream truncation flags.
type Truncation struct {
	ABC      *bool `json:"abc"`
	Semantic *bool `json:"semantic"`
}

// ScoreInfo summarises score.abc.
type ScoreInfo struct {
	Bars          int      `json:"bars"`
	Meter         string   `json:"meter"`
	Q             *string  `json:"q"`
	BPM           *float64 `json:"bpm"`
	SecondsPerBar *float64 `json:"seconds_per_bar"`
	Aligned       bool     `json:"aligned"`
}

// SectionOut is one lyric or plan section; times only when known.
type SectionOut struct {
	Idx        int      `json:"idx"`
	Kind       string   `json:"kind"`
	Label      string   `json:"label"`
	Raw        string   `json:"raw"`
	Lines      []string `json:"lines" nullable:"false"`
	StartS     *float64 `json:"start_s"`
	EndS       *float64 `json:"end_s"`
	Source     string   `json:"source" doc:"lyrics | plan | measured | manual"`
	Confidence *float64 `json:"confidence"`
}

// LoudnessOut is the stored loudness analysis.
type LoudnessOut struct {
	analysis.Loudness
	ReplayGainTrackGainDB float64 `json:"replaygain_track_gain_db"`
	Version               string  `json:"version"`
}

// SpectrumOut is the stored bandwidth analysis.
type SpectrumOut struct {
	analysis.Spectrum
	Version string `json:"version"`
}

// AnalysisInfo groups analyses.
type AnalysisInfo struct {
	Loudness *LoudnessOut `json:"loudness" doc:"null when not analysed"`
	Spectrum *SpectrumOut `json:"spectrum" doc:"null when not analysed"`
}

// ImportInfo says how a track entered the library.
type ImportInfo struct {
	Rule  string  `json:"rule"`
	RunID *string `json:"run_id"`
}

// TrackDetail is the full track view.
type TrackDetail struct {
	ID          string       `json:"id"`
	Title       string       `json:"title"`
	Slug        string       `json:"slug"`
	Status      string       `json:"status"`
	StatusSetBy string       `json:"status_set_by"`
	Origin      string       `json:"origin"`
	RecordedAt  string       `json:"recorded_at"`
	CreatedAt   string       `json:"created_at"`
	DurationS   float64      `json:"duration_s"`
	Audio       AudioInfo    `json:"audio"`
	Recipe      RecipeInfo   `json:"recipe"`
	StyleTerms  []string     `json:"style_terms" nullable:"false"`
	BPMDeclared *int         `json:"bpm_declared"`
	Lineage     Lineage      `json:"lineage"`
	Truncated   *Truncation  `json:"truncated" doc:"null when result.json was not imported"`
	Score       *ScoreInfo   `json:"score" doc:"null when the track has no score"`
	Sections    []SectionOut `json:"sections" nullable:"false"`
	Timed       bool         `json:"timed"`
	Analysis    AnalysisInfo `json:"analysis"`
	Import      ImportInfo   `json:"import"`
}

func nb(n sql.NullInt64) *bool {
	if !n.Valid {
		return nil
	}
	v := n.Int64 == 1
	return &v
}

// sectionRank orders section sources: the most informative wins.
var sectionRank = map[string]int{"manual": 4, "measured": 3, "plan": 2, "lyrics": 1}

// Track returns the full detail of one track.
func (s *Service) Track(ctx context.Context, id string) (TrackDetail, error) {
	var d TrackDetail
	var (
		parent, runID sql.NullString
		bpm           sql.NullInt64
		tabc, tsem    sql.NullInt64
		recipeID      string
	)
	err := s.st.R.QueryRowContext(ctx, `SELECT t.id, t.title, t.slug, t.status, t.status_set_by, t.origin, t.recorded_at, t.created_at, t.duration_s,
		t.sample_rate, t.bits_per_sample, t.channels, t.total_samples, t.master_sha256, t.master_bytes, t.audio_md5,
		t.bpm_declared, t.root_track_id, t.parent_track_id, t.generation, t.truncated_abc, t.truncated_semantic, t.import_rule, t.import_run_id,
		r.id, r.engine, r.request_id, r.style, r.lyrics, r.cot, r.seed, r.hash,
		(SELECT COUNT(*) FROM recipe_sources rs WHERE rs.recipe_id = r.id)
		FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE t.id = ?`, id).
		Scan(&d.ID, &d.Title, &d.Slug, &d.Status, &d.StatusSetBy, &d.Origin, &d.RecordedAt, &d.CreatedAt, &d.DurationS,
			&d.Audio.SampleRate, &d.Audio.BitsPerSample, &d.Audio.Channels, &d.Audio.TotalSamples, &d.Audio.MasterSHA256, &d.Audio.MasterBytes, &d.Audio.AudioMD5,
			&bpm, &d.Lineage.RootTrackID, &parent, &d.Lineage.Generation, &tabc, &tsem, &d.Import.Rule, &runID,
			&recipeID, &d.Recipe.Engine, &d.Recipe.RequestID, &d.Recipe.Style, &d.Recipe.Lyrics, &d.Recipe.Cot, &d.Recipe.Seed, &d.Recipe.Hash, &d.Recipe.SourcesCount)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	if err != nil {
		return d, fmt.Errorf("library: track: %w", err)
	}
	d.Recipe.ID = recipeID
	if bpm.Valid {
		v := int(bpm.Int64)
		d.BPMDeclared = &v
	}
	if parent.Valid {
		d.Lineage.ParentTrackID = &parent.String
	}
	if runID.Valid {
		d.Import.RunID = &runID.String
	}
	if tabc.Valid || tsem.Valid {
		d.Truncated = &Truncation{ABC: nb(tabc), Semantic: nb(tsem)}
	}
	d.StyleTerms, _ = yue2.StyleTerms(d.Recipe.Style)
	if d.StyleTerms == nil {
		d.StyleTerms = []string{}
	}
	if err := s.trackExtras(ctx, &d); err != nil {
		return d, err
	}
	return d, nil
}

func (s *Service) trackExtras(ctx context.Context, d *TrackDetail) error {
	q := s.st.R
	// mp3
	var mp3 MP3Info
	var params string
	switch err := q.QueryRowContext(ctx, `SELECT sha256, bytes, params_json FROM renditions WHERE track_id = ? AND kind = 'mp3_320'`, d.ID).Scan(&mp3.SHA256, &mp3.Bytes, &params); {
	case err == nil:
		var p struct {
			GainDB float64 `json:"gain_db"`
		}
		_ = json.Unmarshal([]byte(params), &p)
		mp3.GainDB = p.GainDB
		d.Audio.MP3 = &mp3
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	// lineage
	var op, rule sql.NullString
	var inf sql.NullInt64
	var pj sql.NullString
	switch err := q.QueryRowContext(ctx, `SELECT operator, inferred, params_json FROM mutations WHERE child_track_id = ?`, d.ID).Scan(&op, &inf, &pj); {
	case err == nil:
		d.Lineage.Operator, d.Lineage.Inferred = &op.String, nb(inf)
		if pj.Valid {
			var p struct {
				Rule string `json:"rule"`
			}
			if json.Unmarshal([]byte(pj.String), &p) == nil && p.Rule != "" {
				rule = sql.NullString{String: p.Rule, Valid: true}
				d.Lineage.Rule = &rule.String
			}
		}
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	d.Lineage.Siblings = []Sibling{}
	rows, err := q.QueryContext(ctx, `SELECT t.id, t.title, r.seed, t.recorded_at FROM tracks t JOIN recipes r ON r.id = t.recipe_id
		WHERE t.root_track_id = ? AND t.id <> ? ORDER BY t.recorded_at, t.id`, d.Lineage.RootTrackID, d.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var sb Sibling
		if err := rows.Scan(&sb.ID, &sb.Title, &sb.Seed, &sb.RecordedAt); err != nil {
			_ = rows.Close()
			return err
		}
		d.Lineage.Siblings = append(d.Lineage.Siblings, sb)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	// score
	var sc ScoreInfo
	var qv sql.NullString
	var bpm, spb sql.NullFloat64
	switch err := q.QueryRowContext(ctx, `SELECT bars, meter, q, bpm, seconds_per_bar, aligned FROM scores WHERE track_id = ?`, d.ID).Scan(&sc.Bars, &sc.Meter, &qv, &bpm, &spb, &sc.Aligned); {
	case err == nil:
		if qv.Valid {
			sc.Q = &qv.String
		}
		if bpm.Valid {
			sc.BPM = &bpm.Float64
		}
		if spb.Valid {
			sc.SecondsPerBar = &spb.Float64
		}
		d.Score = &sc
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	// sections: the highest-ranked source present
	d.Sections = []SectionOut{}
	rows, err = q.QueryContext(ctx, `SELECT source, idx, kind, label, raw_label, start_s, end_s, confidence, lines_json FROM sections WHERE track_id = ? ORDER BY idx`, d.ID)
	if err != nil {
		return err
	}
	bySource := map[string][]SectionOut{}
	for rows.Next() {
		var so SectionOut
		var st, en, conf sql.NullFloat64
		var lj sql.NullString
		if err := rows.Scan(&so.Source, &so.Idx, &so.Kind, &so.Label, &so.Raw, &st, &en, &conf, &lj); err != nil {
			_ = rows.Close()
			return err
		}
		if st.Valid {
			so.StartS, so.EndS = &st.Float64, &en.Float64
		}
		if conf.Valid {
			so.Confidence = &conf.Float64
		}
		so.Lines = []string{}
		if lj.Valid {
			_ = json.Unmarshal([]byte(lj.String), &so.Lines)
			if so.Lines == nil {
				so.Lines = []string{}
			}
		}
		bySource[so.Source] = append(bySource[so.Source], so)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	best := ""
	for src := range bySource {
		if sectionRank[src] > sectionRank[best] {
			best = src
		}
	}
	if best != "" {
		d.Sections = bySource[best]
		d.Timed = d.Sections[0].StartS != nil
		for _, so := range d.Sections {
			if so.StartS == nil {
				d.Timed = false
			}
		}
	}
	// analyses (latest version of each)
	rows, err = q.QueryContext(ctx, `SELECT analyzer, version, metrics_json FROM analyses WHERE track_id = ? ORDER BY created_at ASC, version ASC`, d.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var an, ver, mj string
		if err := rows.Scan(&an, &ver, &mj); err != nil {
			_ = rows.Close()
			return err
		}
		switch an {
		case "loudness":
			var l LoudnessOut
			if json.Unmarshal([]byte(mj), &l) == nil {
				if l.ShortTerm1Hz == nil {
					l.ShortTerm1Hz = []float64{}
				}
				l.Version = ver
				d.Analysis.Loudness = &l
			}
		case "spectrum":
			var sp SpectrumOut
			if json.Unmarshal([]byte(mj), &sp) == nil {
				sp.Version = ver
				d.Analysis.Spectrum = &sp
			}
		}
	}
	return rows.Close()
}

// PeaksLevel is one waveform level of a track.
type PeaksLevel struct {
	TrackID   string            `json:"track_id"`
	Level     string            `json:"level"`
	DurationS float64           `json:"duration_s"`
	Peaks     analysis.Waveform `json:"peaks"`
}

// Peaks returns the overview or detail waveform from the peaks rendition.
func (s *Service) Peaks(ctx context.Context, id, level string) (PeaksLevel, error) {
	if level == "" {
		level = "overview"
	}
	if !oneOf(level, "overview", "detail") {
		return PeaksLevel{}, invalid("level", "must be overview or detail")
	}
	var path string
	err := s.st.R.QueryRowContext(ctx, `SELECT r.path FROM renditions r WHERE r.track_id = ? AND r.kind = 'peaks'`, id).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		if err := s.st.R.QueryRowContext(ctx, `SELECT id FROM tracks WHERE id = ?`, id).Scan(&path); errors.Is(err, sql.ErrNoRows) {
			return PeaksLevel{}, ErrNotFound
		}
		return PeaksLevel{}, ErrMediaUnavailable
	}
	if err != nil {
		return PeaksLevel{}, err
	}
	if s.data == nil {
		return PeaksLevel{}, ErrMediaUnavailable
	}
	f, err := s.data.Open(path)
	if err != nil {
		return PeaksLevel{}, fmt.Errorf("library: open peaks: %w", err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, 8<<20))
	if err != nil {
		return PeaksLevel{}, fmt.Errorf("library: read peaks: %w", err)
	}
	var pk analysis.Peaks
	if err := json.Unmarshal(b, &pk); err != nil {
		return PeaksLevel{}, fmt.Errorf("library: parse peaks: %w", err)
	}
	out := PeaksLevel{TrackID: id, Level: level, DurationS: pk.DurationS, Peaks: pk.Overview}
	if level == "detail" {
		out.Peaks = pk.Detail
	}
	if out.Peaks.Data == nil {
		out.Peaks.Data = []int8{}
	}
	return out, nil
}

// FlacMedia describes the master as a media object.
type FlacMedia struct {
	SHA256        string `json:"sha256"`
	ContentType   string `json:"content_type"`
	Bytes         int64  `json:"bytes"`
	SampleRate    int    `json:"sample_rate"`
	BitsPerSample int    `json:"bits_per_sample"`
}

// MP3Media describes the MP3 rendition as a media object.
type MP3Media struct {
	SHA256      string `json:"sha256"`
	ContentType string `json:"content_type"`
	Bytes       int64  `json:"bytes"`
	BitrateKbps int    `json:"bitrate_kbps"`
}

// MediaSet is what get_media_links signs.
type MediaSet struct {
	TrackID string
	Flac    FlacMedia
	MP3     MP3Media
}

// Media returns the media addresses of a track.
func (s *Service) Media(ctx context.Context, trackID string) (MediaSet, error) {
	m := MediaSet{TrackID: trackID}
	err := s.st.R.QueryRowContext(ctx, `SELECT master_sha256, master_bytes, sample_rate, bits_per_sample FROM tracks WHERE id = ?`, trackID).
		Scan(&m.Flac.SHA256, &m.Flac.Bytes, &m.Flac.SampleRate, &m.Flac.BitsPerSample)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	if err != nil {
		return m, err
	}
	m.Flac.ContentType = "audio/flac"
	err = s.st.R.QueryRowContext(ctx, `SELECT sha256, bytes FROM renditions WHERE track_id = ? AND kind = 'mp3_320'`, trackID).Scan(&m.MP3.SHA256, &m.MP3.Bytes)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrMediaUnavailable
	}
	if err != nil {
		return m, err
	}
	m.MP3.ContentType, m.MP3.BitrateKbps = "audio/mpeg", 320
	return m, nil
}

// Resolve implements serve.Resolver: flac addresses masters, mp3 addresses
// the MP3 rendition.
func (s *Service) Resolve(ctx context.Context, sha, ext string) (serve.Object, error) {
	var (
		q  string
		ct string
	)
	switch ext {
	case "flac":
		q, ct = `SELECT master_path, created_at FROM tracks WHERE master_sha256 = ?`, "audio/flac"
	case "mp3":
		q, ct = `SELECT path, created_at FROM renditions WHERE kind = 'mp3_320' AND sha256 = ? LIMIT 1`, "audio/mpeg"
	default:
		return serve.Object{}, serve.ErrNotFound
	}
	var path, created string
	err := s.st.R.QueryRowContext(ctx, q, sha).Scan(&path, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return serve.Object{}, serve.ErrNotFound
	}
	if err != nil {
		return serve.Object{}, err
	}
	mt, _ := time.Parse(time.RFC3339, created)
	return serve.Object{RelPath: path, ContentType: ct, ETag: sha, ModTime: mt}, nil
}
