package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

// DraftSummary is one backlog row.
type DraftSummary struct {
	ID                 string   `json:"id"`
	RequestID          string   `json:"request_id"`
	GroupKey           *string  `json:"group_key"`
	Label              *string  `json:"label"`
	Origin             string   `json:"origin"`
	Status             string   `json:"status"`
	Seed               int64    `json:"seed"`
	Cot                string   `json:"cot"`
	StyleExcerpt       string   `json:"style_excerpt" doc:"First 120 runes of the style"`
	StyleTerms         []string `json:"style_terms" nullable:"false"`
	SectionKinds       []string `json:"section_kinds" nullable:"false"`
	Collections        []string `json:"collections" nullable:"false"`
	ParentTrackID      *string  `json:"parent_track_id"`
	FulfilledByTrackID *string  `json:"fulfilled_by_track_id"`

	// M2a (C32)
	Revision  int             `json:"revision"`
	Editable  bool            `json:"editable" doc:"false once fulfilled"`
	CreatedBy string          `json:"created_by" doc:"Principal key; masked for agents"`
	UpdatedBy string          `json:"updated_by" doc:"Principal key; masked for agents"`
	Lint      yue2.LintCounts `json:"lint" doc:"Full lint (protocol and songwriting) of the stored request"`

	// M3 (C81.1)
	InFlight       bool `json:"in_flight" doc:"A non-terminal job renders this draft; it cannot be edited or estimated again"`
	GeneratedTakes int  `json:"generated_takes" doc:"Takes generated from this draft"`
}

// DraftPage is one page of drafts.
type DraftPage struct {
	Items       []DraftSummary `json:"items" nullable:"false"`
	Total       int            `json:"total"`
	GroupsTotal int            `json:"groups_total"`
	NextCursor  *string        `json:"next_cursor"`
}

// DraftParams are the filters of ListDrafts.
type DraftParams struct {
	Q, Status, Collection, GroupKey, Origin string
	Limit                                   int
	Cursor                                  string
}

var draftStatuses = []string{"backlog", "fulfilled", "archived"}

func excerpt(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

func sectionKinds(lyrics string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, sec := range yue2.LyricsSections(lyrics) {
		if !seen[sec.Kind] {
			seen[sec.Kind] = true
			out = append(out, sec.Kind)
		}
	}
	return out
}

// ListDrafts lists drafts ordered by group then request id.
func (s *Service) ListDrafts(ctx context.Context, p DraftParams) (DraftPage, error) {
	off, err := decodeCursor(p.Cursor)
	if err != nil {
		return DraftPage{}, err
	}
	status := p.Status
	if status == "" {
		status = "backlog"
	}
	if !oneOf(status, draftStatuses...) {
		return DraftPage{}, invalid("status", "unknown status %q", status)
	}
	if p.Origin != "" && !oneOf(p.Origin, enumOrigin...) {
		return DraftPage{}, invalid("origin", "unknown origin %q", p.Origin)
	}
	limit := clampLimit(p.Limit, 50, 100)
	from := ` FROM recipe_drafts d`
	var args []any
	if fts := ftsQuery(p.Q); fts != "" {
		from += ` JOIN (SELECT draft_id FROM drafts_fts WHERE drafts_fts MATCH ?) f ON f.draft_id = d.id`
		args = append(args, fts)
	}
	where := ` WHERE d.status = ?`
	args = append(args, status)
	if p.Collection != "" {
		where += ` AND EXISTS (SELECT 1 FROM draft_collections c WHERE c.draft_id = d.id AND c.name = ?)`
		args = append(args, p.Collection)
	}
	if p.GroupKey != "" {
		where += ` AND d.group_key = ?`
		args = append(args, p.GroupKey)
	}
	if p.Origin != "" {
		where += ` AND d.origin = ?`
		args = append(args, p.Origin)
	}
	page := DraftPage{Items: []DraftSummary{}}
	if err := s.st.R.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(DISTINCT COALESCE(d.group_key, d.request_id))`+from+where, args...).Scan(&page.Total, &page.GroupsTotal); err != nil {
		return page, fmt.Errorf("library: count drafts: %w", err)
	}
	rows, err := s.st.R.QueryContext(ctx, `SELECT d.id, d.request_id, d.group_key, d.label, d.origin, d.status,
		json_extract(d.request_json, '$.seed'), json_extract(d.request_json, '$.cot'), json_extract(d.request_json, '$.style'),
		json_extract(d.request_json, '$.lyrics'), d.parent_track_id, d.fulfilled_by_track_id, d.revision, d.created_by, d.updated_by,
		EXISTS(SELECT 1 FROM jobs j WHERE j.draft_id = d.id AND j.state NOT IN ('completed','failed','cancelled','expired')),
		(SELECT COUNT(*) FROM tracks t WHERE t.job_id IN (SELECT j.id FROM jobs j WHERE j.draft_id = d.id))`+from+where+
		` ORDER BY d.group_key, d.request_id, d.id LIMIT ? OFFSET ?`, append(args, limit, off)...)
	if err != nil {
		return page, fmt.Errorf("library: list drafts: %w", err)
	}
	var all []DraftSummary
	for rows.Next() {
		var d DraftSummary
		var gk, lb, par, ful sql.NullString
		var style, lyrics string
		if err := rows.Scan(&d.ID, &d.RequestID, &gk, &lb, &d.Origin, &d.Status, &d.Seed, &d.Cot, &style, &lyrics, &par, &ful, &d.Revision, &d.CreatedBy, &d.UpdatedBy, &d.InFlight, &d.GeneratedTakes); err != nil {
			_ = rows.Close()
			return page, err
		}
		d.GroupKey, d.Label, d.ParentTrackID, d.FulfilledByTrackID = ptrStr(gk), ptrStr(lb), ptrStr(par), ptrStr(ful)
		d.Editable = d.Status != "fulfilled" && !d.InFlight
		d.StyleExcerpt = excerpt(style, 120)
		d.StyleTerms, _ = yue2.StyleTerms(style)
		if d.StyleTerms == nil {
			d.StyleTerms = []string{}
		}
		d.SectionKinds = sectionKinds(lyrics)
		d.Collections = []string{}
		all = append(all, d)
	}
	if err := rows.Close(); err != nil {
		return page, err
	}
	if err := s.fillCollections(ctx, all); err != nil {
		return page, err
	}
	pg := newPager(page)
	more := false
	for _, d := range all {
		if !pg.add(sizeOf(d)) {
			more = true
			break
		}
		page.Items = append(page.Items, d)
	}
	if next := off + len(page.Items); more || next < page.Total {
		c := encodeCursor(next)
		page.NextCursor = &c
	}
	return page, nil
}

func ptrStr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	return &n.String
}

func (s *Service) fillCollections(ctx context.Context, ds []DraftSummary) error {
	if len(ds) == 0 {
		return nil
	}
	idx := map[string]int{}
	args := make([]any, len(ds))
	for i, d := range ds {
		idx[d.ID] = i
		args[i] = d.ID
	}
	q := `SELECT draft_id, name FROM draft_collections WHERE draft_id IN (` + placeholders(len(ds)) + `) ORDER BY name` //nolint:gosec // G202: only "?" placeholders are concatenated
	rows, err := s.st.R.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		ds[idx[id]].Collections = append(ds[idx[id]].Collections, name)
	}
	return rows.Err()
}

// DraftRequest is the engine-native request of a draft.
type DraftRequest struct {
	ID     string `json:"id"`
	Style  string `json:"style"`
	Lyrics string `json:"lyrics"`
	Cot    string `json:"cot"`
	Seed   int64  `json:"seed"`

	// M2a (C32): present when set
	ABC      *string  `json:"abc,omitempty"`
	CfgScale *float64 `json:"cfg_scale,omitempty"`
}

// DraftSection is one lyric section of a draft.
type DraftSection struct {
	Idx   int      `json:"idx"`
	Kind  string   `json:"kind"`
	Label string   `json:"label"`
	Raw   string   `json:"raw"`
	Lines []string `json:"lines" nullable:"false"`
}

// DraftSourceRef is one occurrence of the draft's recipe.
type DraftSourceRef struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	RawKind string `json:"raw_kind"`
}

// DraftSource says where the draft's recipe came from.
type DraftSource struct {
	RecipeID   string           `json:"recipe_id"`
	RecipeHash string           `json:"recipe_hash"`
	Sources    []DraftSourceRef `json:"sources" nullable:"false"`
}

// GroupMember is another draft of the same group.
type GroupMember struct {
	ID        string  `json:"id"`
	RequestID string  `json:"request_id"`
	Label     *string `json:"label"`
	Status    string  `json:"status"`
}

// DraftDetail is the full draft view.
type DraftDetail struct {
	ID                 string            `json:"id"`
	Origin             string            `json:"origin"`
	Status             string            `json:"status"`
	RequestID          string            `json:"request_id"`
	GroupKey           *string           `json:"group_key"`
	Label              *string           `json:"label"`
	Request            DraftRequest      `json:"request"`
	Sections           []DraftSection    `json:"sections" nullable:"false"`
	Collections        []string          `json:"collections" nullable:"false"`
	HasReference       bool              `json:"has_reference"`
	Reference          map[string]string `json:"reference" nullable:"true" doc:"Reference metadata of the draft. A human sees every key (source_title, source_artist, variant_type); an agent sees only source_title and source_artist, the text the draft's Muse carries (D-L1-36). Null when the draft has no reference"`
	Source             *DraftSource      `json:"source" doc:"null for drafts without an imported recipe"`
	ParentTrackID      *string           `json:"parent_track_id"`
	FulfilledByTrackID *string           `json:"fulfilled_by_track_id"`
	Group              []GroupMember     `json:"group" nullable:"false"`
	CreatedAt          string            `json:"created_at"`
	UpdatedAt          string            `json:"updated_at"`

	// M2a (C32)
	Revision  int             `json:"revision"`
	Editable  bool            `json:"editable" doc:"false once fulfilled"`
	CreatedBy string          `json:"created_by" doc:"Principal key; masked for agents"`
	UpdatedBy string          `json:"updated_by" doc:"Principal key; masked for agents"`
	Lint      yue2.LintResult `json:"lint" doc:"Full lint of the stored request"`

	// M3 (C81.1)
	InFlight       bool `json:"in_flight" doc:"A non-terminal job renders this draft; it cannot be edited or estimated again"`
	GeneratedTakes int  `json:"generated_takes" doc:"Takes generated from this draft"`
}

// Draft returns one draft. has_reference is always reported. The reference map carries every key for human
// principals (withReference); for agents it is cut to source_title and source_artist, whitespace-normalised the
// way the draft's Muse stores them (D-L1-36: an agent never sees more than the Muse holds).
func (s *Service) Draft(ctx context.Context, id string, withReference bool) (DraftDetail, error) {
	d := DraftDetail{Lint: yue2.LintResult{Errors: []yue2.Issue{}, Warnings: []yue2.Issue{}}}
	var gk, lb, src, par, ful sql.NullString
	var reqJSON string
	err := s.st.R.QueryRowContext(ctx, `SELECT id, origin, status, request_id, group_key, label, request_json, source_recipe_id, parent_track_id,
		fulfilled_by_track_id, created_at, updated_at, revision, created_by, updated_by FROM recipe_drafts WHERE id = ?`, id).
		Scan(&d.ID, &d.Origin, &d.Status, &d.RequestID, &gk, &lb, &reqJSON, &src, &par, &ful, &d.CreatedAt, &d.UpdatedAt, &d.Revision, &d.CreatedBy, &d.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	if err != nil {
		return d, fmt.Errorf("library: draft: %w", err)
	}
	d.GroupKey, d.Label, d.ParentTrackID, d.FulfilledByTrackID = ptrStr(gk), ptrStr(lb), ptrStr(par), ptrStr(ful)
	d.Editable = d.Status != "fulfilled"
	if err := s.st.R.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM jobs j WHERE j.draft_id = ?1 AND j.state NOT IN ('completed','failed','cancelled','expired')),
		(SELECT COUNT(*) FROM tracks t WHERE t.job_id IN (SELECT j.id FROM jobs j WHERE j.draft_id = ?1))`, id).Scan(&d.InFlight, &d.GeneratedTakes); err != nil {
		return d, fmt.Errorf("library: draft jobs: %w", err)
	}
	d.Editable = d.Editable && !d.InFlight
	if err := json.Unmarshal([]byte(reqJSON), &d.Request); err != nil {
		return d, fmt.Errorf("library: draft request: %w", err)
	}
	d.Sections = []DraftSection{}
	for _, sec := range yue2.LyricsSections(d.Request.Lyrics) {
		l := sec.Lines
		if l == nil {
			l = []string{}
		}
		d.Sections = append(d.Sections, DraftSection{Idx: sec.Idx, Kind: sec.Kind, Label: sec.Label, Raw: sec.Raw, Lines: l})
	}
	d.Collections = []string{}
	d.Group = []GroupMember{}
	q := s.st.R
	rows, err := q.QueryContext(ctx, `SELECT name FROM draft_collections WHERE draft_id = ? ORDER BY name`, id)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			_ = rows.Close()
			return d, err
		}
		d.Collections = append(d.Collections, n)
	}
	if err := rows.Close(); err != nil {
		return d, err
	}
	if d.GroupKey != nil {
		rows, err = q.QueryContext(ctx, `SELECT id, request_id, label, status FROM recipe_drafts WHERE group_key = ? ORDER BY request_id, id`, *d.GroupKey)
		if err != nil {
			return d, err
		}
		for rows.Next() {
			var m GroupMember
			var l sql.NullString
			if err := rows.Scan(&m.ID, &m.RequestID, &l, &m.Status); err != nil {
				_ = rows.Close()
				return d, err
			}
			m.Label = ptrStr(l)
			d.Group = append(d.Group, m)
		}
		if err := rows.Close(); err != nil {
			return d, err
		}
	}
	if !src.Valid {
		return d, nil
	}
	ds := DraftSource{RecipeID: src.String, Sources: []DraftSourceRef{}}
	if err := q.QueryRowContext(ctx, `SELECT hash FROM recipes WHERE id = ?`, src.String).Scan(&ds.RecipeHash); err != nil {
		return d, err
	}
	rows, err = q.QueryContext(ctx, `SELECT source_path, line_no, raw_kind FROM recipe_sources WHERE recipe_id = ? ORDER BY source_path, line_no, raw_sha256`, src.String)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var r DraftSourceRef
		if err := rows.Scan(&r.Path, &r.Line, &r.RawKind); err != nil {
			_ = rows.Close()
			return d, err
		}
		ds.Sources = append(ds.Sources, r)
	}
	if err := rows.Close(); err != nil {
		return d, err
	}
	d.Source = &ds
	rows, err = q.QueryContext(ctx, `SELECT ref_json FROM recipe_refs WHERE recipe_id = ? ORDER BY source`, src.String)
	if err != nil {
		return d, err
	}
	merged := map[string]string{}
	for rows.Next() {
		var rj string
		if err := rows.Scan(&rj); err != nil {
			_ = rows.Close()
			return d, err
		}
		d.HasReference = true
		var m map[string]string
		if json.Unmarshal([]byte(rj), &m) == nil {
			for k, v := range m {
				merged[k] = v
			}
		}
	}
	if err := rows.Close(); err != nil {
		return d, err
	}
	switch {
	case !d.HasReference:
	case withReference:
		d.Reference = merged
	default:
		d.Reference = museText(merged)
	}
	return d, nil
}

// museText is the part of a reference map that a Muse carries (title and artist), normalised like the Muse stores it;
// nil when neither is present.
func museText(ref map[string]string) map[string]string {
	out := map[string]string{}
	for _, k := range []string{"source_title", "source_artist"} {
		if v := strings.Join(strings.Fields(ref[k]), " "); v != "" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
