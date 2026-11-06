package songpr

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/lineage"
)

// ListFilter filters list_patches.
type ListFilter struct {
	TrackID, CommentID, RootTrackID string
	Limit                           int
	Cursor                          string
}

// Page is one page of summaries, newest first.
type Page struct {
	Items      []Summary
	NextCursor *string
	Total      int
}

const patchCols = `p.id, p.base_track_id, t.title, p.base_recipe_hash, p.operator, p.title, p.rationale, p.principal_id, p.principal_kind, p.principal_name, p.created_at`

func scanSummary(r interface{ Scan(...any) error }) (Summary, error) {
	var s Summary
	var title sql.NullString
	if err := r.Scan(&s.ID, &s.Base.TrackID, &s.Base.Title, &s.Base.RecipeHash, &s.Operator, &title, &s.Rationale, &s.Author.Key, &s.Author.Kind, &s.Author.Name, &s.CreatedAt); err != nil {
		return s, err
	}
	if title.Valid {
		v := title.String
		s.Title = &v
	}
	return s, nil
}

// fill adds the addressed comments, the renders and the callback tally to a summary.
func fill(ctx context.Context, x q, s *Summary) error {
	rows, err := x.QueryContext(ctx, `SELECT c.id, c.track_id, c.kind, c.state, c.body, c.hidden_at IS NOT NULL FROM patch_comments pc JOIN comments c ON c.id = pc.comment_id
		WHERE pc.patch_id = ? ORDER BY c.created_at, c.id`, s.ID)
	if err != nil {
		return fmt.Errorf("songpr: addresses: %w", err)
	}
	s.Addresses = []Address{}
	for rows.Next() {
		var a Address
		var body string
		var hidden bool
		if err := rows.Scan(&a.CommentID, &a.TrackID, &a.Kind, &a.State, &body, &hidden); err != nil {
			rows.Close()
			return err
		}
		if !hidden {
			a.BodyExcerpt = excerpt(body)
		}
		s.Addresses = append(s.Addresses, a)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	rows, err = x.QueryContext(ctx, `SELECT j.batch_id, j.id, j.state, r.seed, j.track_id FROM jobs j JOIN recipes r ON r.id = j.recipe_id
		WHERE j.patch_id = ? ORDER BY j.created_at, j.take_index, j.id`, s.ID)
	if err != nil {
		return fmt.Errorf("songpr: renders: %w", err)
	}
	s.Renders = []Render{}
	for rows.Next() {
		var r Render
		var child sql.NullString
		if err := rows.Scan(&r.BatchID, &r.JobID, &r.State, &r.Seed, &child); err != nil {
			rows.Close()
			return err
		}
		if child.Valid {
			v := child.String
			r.ChildTrackID = &v
		}
		s.Renders = append(s.Renders, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	// the human's answer is the one that counts (D-M5-5); critic verdicts never do
	if err := x.QueryRowContext(ctx, `SELECT COUNT(c.id), COUNT(v.id),
			COALESCE(SUM(v.verdict = 'fixed'), 0), COALESCE(SUM(v.verdict = 'not_yet'), 0), COALESCE(SUM(v.verdict = 'worse'), 0),
			COALESCE(SUM(v.verdict = 'kept'), 0), COALESCE(SUM(v.verdict = 'lost'), 0)
		FROM callbacks c LEFT JOIN callback_verdicts v ON v.callback_id = c.id AND v.source = 'human' WHERE c.patch_id = ?`, s.ID).
		Scan(&s.Callbacks.Total, &s.Callbacks.Answered, &s.Callbacks.Fixed, &s.Callbacks.NotYet, &s.Callbacks.Worse, &s.Callbacks.Kept, &s.Callbacks.Lost); err != nil {
		return fmt.Errorf("songpr: callback tally: %w", err)
	}
	return nil
}

// Get reads one Song PR with its ops, diff, scope and compiled patch.
func (s *Service) Get(ctx context.Context, _ auth.Principal, id string) (SongPR, error) {
	if err := s.ready(); err != nil {
		return SongPR{}, err
	}
	var out SongPR
	var ops, compiled, scope, diff string
	row := s.o.Store.R.QueryRowContext(ctx, `SELECT `+patchCols+`, p.ops_json, p.compiled_json, p.scope_json, p.diff_json
		FROM patches p JOIN tracks t ON t.id = p.base_track_id WHERE p.id = ?`, id)
	var title sql.NullString
	err := row.Scan(&out.ID, &out.Base.TrackID, &out.Base.Title, &out.Base.RecipeHash, &out.Operator, &title, &out.Rationale, &out.Author.Key, &out.Author.Kind, &out.Author.Name,
		&out.CreatedAt, &ops, &compiled, &scope, &diff)
	if errors.Is(err, sql.ErrNoRows) {
		return SongPR{}, notFound()
	}
	if err != nil {
		return SongPR{}, fmt.Errorf("songpr: get: %w", err)
	}
	if title.Valid {
		v := title.String
		out.Title = &v
	}
	for _, d := range []struct {
		raw string
		to  any
	}{{ops, &out.Ops}, {compiled, &out.Compiled}, {scope, &out.Bounds}, {diff, &out.Diff}} {
		if err := json.Unmarshal([]byte(d.raw), d.to); err != nil {
			return SongPR{}, fmt.Errorf("songpr: stored patch %s: %w", id, err)
		}
	}
	if out.Ops == nil {
		out.Ops = []OpJSON{}
	}
	if out.Diff.Lyrics == nil {
		out.Diff.Lyrics = []LineChange{}
	}
	if err := fill(ctx, s.o.Store.R, &out.Summary); err != nil {
		return SongPR{}, err
	}
	return out, nil
}

func cursorOffset(c string) (int, error) {
	if c == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err == nil {
		if n, err := strconv.Atoi(strings.TrimPrefix(string(b), "o")); err == nil && n >= 0 && strings.HasPrefix(string(b), "o") {
			return n, nil
		}
	}
	return 0, perr(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "invalid cursor", map[string]any{"field": "cursor"})
}

func offsetCursor(n int) string {
	return base64.RawURLEncoding.EncodeToString([]byte("o" + strconv.Itoa(n)))
}

// List pages Song PRs newest first, filtered by base or child take, addressed comment and lineage root.
func (s *Service) List(ctx context.Context, _ auth.Principal, f ListFilter) (Page, error) {
	if err := s.ready(); err != nil {
		return Page{}, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 100)
	off, err := cursorOffset(f.Cursor)
	if err != nil {
		return Page{}, err
	}
	where, args := []string{"1 = 1"}, []any{}
	if f.TrackID != "" {
		where = append(where, `(p.base_track_id = ? OR EXISTS (SELECT 1 FROM jobs j WHERE j.patch_id = p.id AND j.track_id = ?))`)
		args = append(args, f.TrackID, f.TrackID)
	}
	if f.CommentID != "" {
		where = append(where, `EXISTS (SELECT 1 FROM patch_comments pc WHERE pc.patch_id = p.id AND pc.comment_id = ?)`)
		args = append(args, f.CommentID)
	}
	if f.RootTrackID != "" {
		where = append(where, `t.root_track_id = ?`)
		args = append(args, f.RootTrackID)
	}
	cond := strings.Join(where, " AND ")
	var page Page
	if err := s.o.Store.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM patches p JOIN tracks t ON t.id = p.base_track_id WHERE `+cond, args...).Scan(&page.Total); err != nil { //nolint:gosec // G202: fixed fragments, values are bound
		return Page{}, fmt.Errorf("songpr: count: %w", err)
	}
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT `+patchCols+` FROM patches p JOIN tracks t ON t.id = p.base_track_id WHERE `+cond+ //nolint:gosec // G202: fixed fragments, values are bound
		` ORDER BY p.created_at DESC, p.id DESC LIMIT ? OFFSET ?`, append(args, limit+1, off)...)
	if err != nil {
		return Page{}, fmt.Errorf("songpr: list: %w", err)
	}
	var items []Summary
	for rows.Next() {
		it, err := scanSummary(rows)
		if err != nil {
			rows.Close()
			return Page{}, err
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Page{}, err
	}
	rows.Close()
	if len(items) > limit {
		items = items[:limit]
		c := offsetCursor(off + limit)
		page.NextCursor = &c
	}
	page.Items = []Summary{}
	for _, it := range items {
		if err := fill(ctx, s.o.Store.R, &it); err != nil {
			return Page{}, err
		}
		page.Items = append(page.Items, it)
	}
	return page, nil
}

// ---- generation.PatchSource ----

var _ generation.PatchSource = (*Service)(nil)

// PatchForEstimate loads the stored patch for the planner; an unknown id is 404.
func (s *Service) PatchForEstimate(ctx context.Context, x lineage.Querier, id string) (generation.PatchRow, error) {
	var row generation.PatchRow
	var compiled string
	err := x.QueryRowContext(ctx, `SELECT id, base_track_id, operator, compiled_json FROM patches WHERE id = ?`, id).Scan(&row.ID, &row.BaseTrackID, &row.Operator, &compiled)
	if errors.Is(err, sql.ErrNoRows) {
		return row, notFound()
	}
	if err != nil {
		return row, fmt.Errorf("songpr: patch for estimate: %w", err)
	}
	var c Patch
	if err := json.Unmarshal([]byte(compiled), &c); err != nil {
		return row, fmt.Errorf("songpr: compiled patch %s: %w", id, err)
	}
	row.Lyrics, row.Style = c.Lyrics, c.Style
	ids, err := patchCommentIDs(ctx, x, id)
	row.CommentIDs = ids
	return row, err
}

func patchCommentIDs(ctx context.Context, x lineage.Querier, id string) ([]string, error) {
	rows, err := x.QueryContext(ctx, `SELECT comment_id FROM patch_comments WHERE patch_id = ? ORDER BY comment_id`, id)
	if err != nil {
		return nil, fmt.Errorf("songpr: patch comments: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		ids = append(ids, c)
	}
	return ids, rows.Err()
}

// RecheckComments fails with 409 ESTIMATE_STALE (reason comments_changed) when an addressed comment is hidden, resolved or no longer
// a fix or note. A comment this patch already moved to addressed stays valid: a patch may render up to PatchMaxRenders times, and
// its first child is what moves the comment (D-M5-3 amended).
func (s *Service) RecheckComments(ctx context.Context, x lineage.Querier, id string) error {
	ids, err := patchCommentIDs(ctx, x, id)
	if err != nil {
		return err
	}
	var bad []string
	for _, c := range ids {
		var kind, state string
		var hidden bool
		err := x.QueryRowContext(ctx, `SELECT kind, state, hidden_at IS NOT NULL FROM comments WHERE id = ?`, c).Scan(&kind, &state, &hidden)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("songpr: recheck comment: %w", err)
		}
		if err != nil || hidden || (kind != "fix" && kind != "note") || (state != "open" && state != "regressed" && state != "addressed") {
			bad = append(bad, c)
		}
	}
	if len(bad) > 0 {
		return perr(http.StatusConflict, "ESTIMATE_STALE", "a comment this Song PR addresses has changed; propose again",
			map[string]any{"reason": "comments_changed", "patch_id": id, "comment_ids": bad})
	}
	return nil
}

// RenderCount counts the batches this patch was rendered in, leaving out batches that were rejected or expired and batches whose
// jobs were all cancelled.
func (s *Service) RenderCount(ctx context.Context, x lineage.Querier, id string) (int, error) {
	var n int
	err := x.QueryRowContext(ctx, `SELECT COUNT(DISTINCT j.batch_id) FROM jobs j JOIN batches b ON b.id = j.batch_id
		WHERE j.patch_id = ? AND b.state NOT IN ('rejected','expired') AND j.state <> 'cancelled'`, id).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("songpr: render count: %w", err)
	}
	return n, nil
}
