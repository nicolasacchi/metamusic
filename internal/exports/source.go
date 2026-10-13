package exports

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/export"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/playlists"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// NewDBSource is the database-backed export.Source (SQL on the read pool).
func NewDBSource(st *store.Store, lib *library.Service, pl *playlists.Service) export.Source {
	return &dbSource{st: st, lib: lib, pl: pl}
}

type dbSource struct {
	st  *store.Store
	lib *library.Service
	pl  *playlists.Service
}

// jsonArray renders ids as a JSON array for json_each, which has no variable limit.
func jsonArray(ids []string) string {
	b, _ := json.Marshal(ids)
	return string(b)
}

func (d *dbSource) JSONLSourceRows(ctx context.Context, path string) ([]export.SourceRow, error) {
	rows, err := d.st.R.QueryContext(ctx, `SELECT rs.line_no, rs.raw, rs.import_run_id, rs.recipe_id, r.request_json
		FROM recipe_sources rs JOIN recipes r ON r.id = rs.recipe_id
		WHERE rs.raw_kind = 'jsonl_line' AND rs.source_path = ?
		ORDER BY rs.line_no, rs.import_run_id, rs.id`, path)
	if err != nil {
		return nil, fmt.Errorf("exports: source rows: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []export.SourceRow
	for rows.Next() {
		var (
			r    export.SourceRow
			reqJ string
		)
		if err := rows.Scan(&r.LineNo, &r.Raw, &r.ImportRunID, &r.RecipeID, &reqJ); err != nil {
			return nil, err
		}
		if hasABCPath(r.Raw) {
			var req yue2.Request
			if json.Unmarshal([]byte(reqJ), &req) == nil && req.ABC != nil {
				r.ABC = *req.ABC
			}
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, export.ErrUnknownSource
	}
	return out, nil
}

func hasABCPath(raw []byte) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	v, ok := m["abc_path"]
	return ok && string(v) != "null"
}

// trackIDs resolves a track filter to ids: the search semantics through the library, intersected with
// the playlist and explicit ids. nil means "no restriction other than the filter".
func (d *dbSource) trackIDs(ctx context.Context, f export.TrackFilter) ([]string, error) {
	ids, err := d.lib.MatchTrackIDs(ctx, library.SearchParams{Status: f.Status, MinStars: f.MinStars, Tags: f.Tags})
	if err != nil {
		return nil, err
	}
	restrict := func(allowed []string) {
		set := make(map[string]bool, len(allowed))
		for _, id := range allowed {
			set[id] = true
		}
		ids = slices.DeleteFunc(ids, func(id string) bool { return !set[id] })
	}
	if f.PlaylistID != "" {
		p, err := d.pl.Get(ctx, f.PlaylistID)
		if err != nil {
			return nil, err
		}
		allowed := make([]string, len(p.Items))
		for i, it := range p.Items {
			allowed[i] = it.Track.ID
		}
		restrict(allowed)
	}
	if len(f.TrackIDs) > 0 {
		restrict(f.TrackIDs)
	}
	return ids, nil
}

func (d *dbSource) SelectRecipes(ctx context.Context, f export.TrackFilter) ([]export.RecipeRow, error) {
	ids, err := d.trackIDs(ctx, f)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	arr := jsonArray(ids)
	rows, err := d.st.R.QueryContext(ctx, `SELECT r.id, r.hash, r.request_id, r.request_json, MIN(t.recorded_at)
		FROM tracks t JOIN recipes r ON r.id = t.recipe_id
		WHERE t.id IN (SELECT value FROM json_each(?)) AND r.engine <> 'unknown' GROUP BY r.id ORDER BY r.id`, arr)
	if err != nil {
		return nil, fmt.Errorf("exports: select recipes: %w", err)
	}
	var out []export.RecipeRow
	idx := map[string]int{}
	for rows.Next() {
		var (
			r    export.RecipeRow
			reqJ string
		)
		if err := rows.Scan(&r.RecipeID, &r.Hash, &r.RequestID, &reqJ, &r.FirstRecordedAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := json.Unmarshal([]byte(reqJ), &r.Request); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("exports: stored request of recipe %s: %w", r.RecipeID, err)
		}
		idx[r.RecipeID] = len(out)
		out = append(out, r)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	rids := make([]string, 0, len(out))
	for _, r := range out {
		rids = append(rids, r.RecipeID)
	}
	srows, err := d.st.R.QueryContext(ctx, `SELECT recipe_id, source_path, line_no, raw, import_run_id FROM recipe_sources
		WHERE raw_kind = 'jsonl_line' AND recipe_id IN (SELECT value FROM json_each(?)) ORDER BY source_path, line_no, import_run_id`, jsonArray(rids))
	if err != nil {
		return nil, fmt.Errorf("exports: recipe sources: %w", err)
	}
	defer func() { _ = srows.Close() }()
	for srows.Next() {
		var (
			rid string
			s   export.RawSource
		)
		if err := srows.Scan(&rid, &s.Path, &s.LineNo, &s.Raw, &s.ImportRunID); err != nil {
			return nil, err
		}
		out[idx[rid]].JSONLSources = append(out[idx[rid]].JSONLSources, s)
	}
	return out, srows.Err()
}

func (d *dbSource) UnknownRecipeTracks(ctx context.Context, f export.TrackFilter) ([]string, error) {
	ids, err := d.trackIDs(ctx, f)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	rows, err := d.st.R.QueryContext(ctx, `SELECT t.id FROM tracks t JOIN recipes r ON r.id = t.recipe_id
		WHERE t.id IN (SELECT value FROM json_each(?)) AND r.engine = 'unknown' ORDER BY t.id`, jsonArray(ids))
	if err != nil {
		return nil, fmt.Errorf("exports: unknown recipe tracks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (d *dbSource) SelectDrafts(ctx context.Context, f export.DraftFilter) ([]export.DraftRow, error) {
	status := f.Status
	if len(status) == 0 {
		status = []string{"backlog"}
	}
	q := `SELECT d.id, COALESCE(d.group_key, ''), d.request_id, d.request_json FROM recipe_drafts d
		WHERE d.status IN (SELECT value FROM json_each(?))`
	args := []any{jsonArray(status)}
	if f.Collection != "" {
		q += ` AND EXISTS (SELECT 1 FROM draft_collections c WHERE c.draft_id = d.id AND c.name = ?)`
		args = append(args, f.Collection)
	}
	if f.Origin != "" {
		q += ` AND d.origin = ?`
		args = append(args, f.Origin)
	}
	if f.GroupKey != "" {
		q += ` AND d.group_key = ?`
		args = append(args, f.GroupKey)
	}
	if len(f.DraftIDs) > 0 {
		q += ` AND d.id IN (SELECT value FROM json_each(?))`
		args = append(args, jsonArray(f.DraftIDs))
	}
	rows, err := d.st.R.QueryContext(ctx, q+` ORDER BY d.group_key, d.request_id, d.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("exports: select drafts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []export.DraftRow
	for rows.Next() {
		var (
			r    export.DraftRow
			reqJ string
		)
		if err := rows.Scan(&r.DraftID, &r.GroupKey, &r.RequestID, &reqJ); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(reqJ), &r.Request); err != nil {
			return nil, fmt.Errorf("exports: stored request of draft %s: %w", r.DraftID, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *dbSource) StyleSamples(ctx context.Context) ([][]byte, error) {
	rows, err := d.st.R.QueryContext(ctx, `SELECT raw FROM recipe_sources WHERE raw_kind = 'jsonl_line' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("exports: style samples: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out [][]byte
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
