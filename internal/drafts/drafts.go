// Package drafts owns the draft editor and JSONL upload (contract C32).
// M2a-B4a freezes the signatures; M2a-B5 implements them.
package drafts

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// NewDraft is the input of Create.
type NewDraft struct {
	Request       json.RawMessage
	Label         *string
	ParentTrackID *string
	Collection    *string
}

// DraftPatch is the input of Update; Revision is required.
type DraftPatch struct {
	Revision      int
	Request       json.RawMessage
	Label         *string // "" clears
	Status        *string // backlog | archived
	ParentTrackID *string // "" clears
}

// Upload is the input of Upload.
type Upload struct {
	JSONL         string
	Collection    *string
	ParentTrackID *string
	AllowPartial  bool
	ABCFiles      map[string]string // path -> ABC text
}

// UploadDraft names one created draft.
type UploadDraft struct {
	Line      int     `json:"line"`
	ID        string  `json:"id"`
	RequestID string  `json:"request_id"`
	Label     *string `json:"label"`
}

// UploadIssue is one error or warning of an upload line.
type UploadIssue struct {
	Line  int    `json:"line"`
	Code  string `json:"code"`
	Field string `json:"field"`
}

// UploadResult is the outcome of Upload.
type UploadResult struct {
	Created    int           `json:"created"`
	Drafts     []UploadDraft `json:"drafts" nullable:"false"`
	Errors     []UploadIssue `json:"errors" nullable:"false"`
	Warnings   []UploadIssue `json:"warnings" nullable:"false"`
	Collection *string       `json:"collection"`
}

// FailedItem names the errors (and warnings) of one request in a LINT_FAILED answer. Line is the
// JSONL line of an upload and 0 for create_draft / update_draft.
type FailedItem struct {
	Line     int          `json:"line"`
	Errors   []yue2.Issue `json:"errors" nullable:"false"`
	Warnings []yue2.Issue `json:"warnings" nullable:"false"`
}

// Error is a drafts failure with a stable problem code; the API layer maps it to
// application/problem+json.
type Error struct {
	Status  int
	Code    string
	Msg     string
	Details map[string]any
}

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

func invalid(field, format string, a ...any) error {
	return &Error{Status: 422, Code: "VALIDATION_FAILED", Msg: fmt.Sprintf(format, a...), Details: map[string]any{"field": field}}
}

func lintFailed(msg string, items []FailedItem) error {
	return &Error{Status: 422, Code: "LINT_FAILED", Msg: msg, Details: map[string]any{"items": items}}
}

// Limits of an upload (C32). The 6 MiB total body cap is the operation's MaxBodyBytes.
const (
	maxUploadLines  = 1000
	maxUploadJSONL  = 4 << 20
	maxUploadFiles  = 50
	maxUploadABC    = 1 << 20
	maxLabelRunes   = 40
	maxCollectionLn = 80
)

// Service implements C32.
type Service struct {
	st    *store.Store
	lib   *library.Service
	now   func() time.Time
	newID func() string

	hashQueries atomic.Int64 // duplicate-check queries issued (tests: one per page)
}

// New builds the drafts service; nil now means time.Now.
func New(st *store.Store, lib *library.Service, now func() time.Time, newID func() string) *Service {
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = func() string { return uuid.NewV7().String() }
	}
	return &Service{st: st, lib: lib, now: now, newID: newID}
}

func (s *Service) stamp() string { return s.now().UTC().Format(time.RFC3339) }

// ---- lint ----

type hit struct{ where, draftID, recipeID string }

// knownHashes finds, in one query, the recipes and drafts whose canonical request equals one of the
// items' canonical requests. It returns the KnownHash callback over the result.
func (s *Service) knownHashes(ctx context.Context, items []yue2.LintItem) (func(hash, selfDraftID, selfRecipeID string) (string, bool), error) {
	byHash := map[string][]hit{}
	var hashes, texts []any
	for _, it := range items {
		p, err := yue2.ParseRequest(it.Request, yue2.ParseOptions{})
		if err != nil {
			continue
		}
		if _, dup := byHash[p.Hash]; dup {
			continue
		}
		byHash[p.Hash] = nil
		hashes = append(hashes, p.Hash)
		texts = append(texts, string(p.Canonical))
	}
	if len(hashes) == 0 {
		return func(string, string, string) (string, bool) { return "", false }, nil
	}
	s.hashQueries.Add(1)
	//nolint:gosec // G202: only "?" placeholders are concatenated
	q := `SELECT 'recipe', id, hash FROM recipes WHERE hash IN (` + placeholders(len(hashes)) + `)
		UNION ALL SELECT 'draft', id, request_json FROM recipe_drafts WHERE request_json IN (` + placeholders(len(texts)) + `)`
	rows, err := s.st.R.QueryContext(ctx, q, append(hashes, texts...)...)
	if err != nil {
		return nil, fmt.Errorf("drafts: known hashes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind, id, val string
		if err := rows.Scan(&kind, &id, &val); err != nil {
			return nil, err
		}
		if kind == "recipe" {
			byHash[val] = append(byHash[val], hit{where: "recipe", recipeID: id})
		} else {
			h := library.SHA256Hex([]byte(val))
			byHash[h] = append(byHash[h], hit{where: "draft:" + id, draftID: id})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, hs := range byHash {
		sort.Slice(hs, func(a, b int) bool { return hs[a].where < hs[b].where })
	}
	return func(hash, selfDraftID, selfRecipeID string) (string, bool) {
		for _, h := range byHash[hash] {
			if (h.draftID != "" && h.draftID == selfDraftID) || (h.recipeID != "" && h.recipeID == selfRecipeID) {
				continue
			}
			return h.where, true
		}
		return "", false
	}, nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// lintWith lints items whose SourceRecipeID is already known.
func (s *Service) lintWith(ctx context.Context, items []yue2.LintItem, batch bool) (yue2.LintReport, error) {
	known, err := s.knownHashes(ctx, items)
	if err != nil {
		return yue2.LintReport{}, err
	}
	return yue2.Lint(items, batch, yue2.LintContext{KnownHash: known}), nil
}

// Lint lints items with the duplicate check backed by the database. An item naming a DraftID is
// compared without that draft and its own source recipe.
func (s *Service) Lint(ctx context.Context, items []yue2.LintItem, batch bool) (yue2.LintReport, error) {
	var ids []any
	idx := map[string][]int{}
	for i, it := range items {
		if it.DraftID != "" && it.SourceRecipeID == "" {
			if _, ok := idx[it.DraftID]; !ok {
				ids = append(ids, it.DraftID)
			}
			idx[it.DraftID] = append(idx[it.DraftID], i)
		}
	}
	if len(ids) > 0 {
		items = append([]yue2.LintItem(nil), items...)
		rows, err := s.st.R.QueryContext(ctx, `SELECT id, source_recipe_id FROM recipe_drafts WHERE source_recipe_id IS NOT NULL AND id IN (`+placeholders(len(ids))+`)`, ids...) //nolint:gosec // G202: only "?" placeholders are concatenated
		if err != nil {
			return yue2.LintReport{}, fmt.Errorf("drafts: source recipes: %w", err)
		}
		for rows.Next() {
			var id, rid string
			if err := rows.Scan(&id, &rid); err != nil {
				_ = rows.Close()
				return yue2.LintReport{}, err
			}
			for _, i := range idx[id] {
				items[i].SourceRecipeID = rid
			}
		}
		if err := rows.Close(); err != nil {
			return yue2.LintReport{}, err
		}
	}
	return s.lintWith(ctx, items, batch)
}

type draftReq struct{ id, requestJSON, sourceRecipe string }

// requestsOf reads the stored request of each draft id.
func (s *Service) requestsOf(ctx context.Context, ids []string) (map[string]draftReq, error) {
	out := map[string]draftReq{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.st.R.QueryContext(ctx, `SELECT id, request_json, COALESCE(source_recipe_id, '') FROM recipe_drafts WHERE id IN (`+placeholders(len(ids))+`)`, args...) //nolint:gosec // G202: only "?" placeholders are concatenated
	if err != nil {
		return nil, fmt.Errorf("drafts: requests: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r draftReq
		if err := rows.Scan(&r.id, &r.requestJSON, &r.sourceRecipe); err != nil {
			return nil, err
		}
		out[r.id] = r
	}
	return out, rows.Err()
}

func (r draftReq) item(carry []yue2.Issue) yue2.LintItem {
	return yue2.LintItem{Request: json.RawMessage(r.requestJSON), DraftID: r.id, SourceRecipeID: r.sourceRecipe, Carry: carry}
}

// Decorate fills the lint counts of ds with one duplicate-check query for the whole page.
func (s *Service) Decorate(ctx context.Context, ds []library.DraftSummary) error {
	if len(ds) == 0 {
		return nil
	}
	ids := make([]string, len(ds))
	for i, d := range ds {
		ids[i] = d.ID
	}
	reqs, err := s.requestsOf(ctx, ids)
	if err != nil {
		return err
	}
	items := make([]yue2.LintItem, 0, len(ds))
	at := make([]int, 0, len(ds))
	for i, d := range ds {
		if r, ok := reqs[d.ID]; ok {
			items = append(items, r.item(nil))
			at = append(at, i)
		}
	}
	rep, err := s.lintWith(ctx, items, false)
	if err != nil {
		return err
	}
	for j, i := range at {
		ds[i].Lint = yue2.LintCounts{Errors: len(rep.Items[j].Errors), Warnings: len(rep.Items[j].Warnings)}
	}
	return nil
}

// DecorateDetail fills the lint result of d.
func (s *Service) DecorateDetail(ctx context.Context, d *library.DraftDetail) error {
	return s.decorateDetail(ctx, d, nil)
}

func (s *Service) decorateDetail(ctx context.Context, d *library.DraftDetail, carry []yue2.Issue) error {
	reqs, err := s.requestsOf(ctx, []string{d.ID})
	if err != nil {
		return err
	}
	r, ok := reqs[d.ID]
	if !ok {
		return library.ErrNotFound
	}
	rep, err := s.lintWith(ctx, []yue2.LintItem{r.item(carry)}, false)
	if err != nil {
		return err
	}
	d.Lint = rep.Items[0]
	return nil
}

// ---- parsing ----

func splitIssues(is []yue2.Issue) (errs, warns []yue2.Issue) {
	errs, warns = []yue2.Issue{}, []yue2.Issue{}
	for _, i := range is {
		if i.Severity == "error" {
			errs = append(errs, i)
		} else {
			warns = append(warns, i)
		}
	}
	return errs, warns
}

// parseOne parses one request with the editor's rules. A parse error is a LINT_FAILED answer; the
// returned warnings (dropped keys, defaults) travel with the result as lint Carry.
func parseOne(raw []byte) (yue2.Parsed, []yue2.Issue, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return yue2.Parsed{}, nil, invalid("request", "request is required")
	}
	errs, warns := splitIssues(yue2.CheckRequest(raw, yue2.ModeApp, nil))
	if len(errs) > 0 {
		return yue2.Parsed{}, nil, lintFailed("the request has protocol errors", []FailedItem{{Errors: errs, Warnings: warns}})
	}
	p, err := yue2.ParseRequest(raw, yue2.ParseOptions{})
	if err != nil {
		return yue2.Parsed{}, nil, lintFailed("the request has protocol errors", []FailedItem{{Errors: []yue2.Issue{{Code: "not_object", Severity: "error"}}, Warnings: warns}})
	}
	return p, warns, nil
}

func (s *Service) checkLabel(label *string) error {
	if label != nil && utf8.RuneCountInString(*label) > maxLabelRunes {
		return invalid("label", "at most %d characters", maxLabelRunes)
	}
	return nil
}

func (s *Service) checkParent(ctx context.Context, id *string) error {
	if id == nil || *id == "" {
		return nil
	}
	ok, err := s.lib.TrackExists(ctx, *id)
	if err != nil {
		return fmt.Errorf("drafts: parent lookup: %w", err)
	}
	if !ok {
		return invalid("parent_track_id", "no such track")
	}
	return nil
}

func checkCollection(c *string) (string, error) {
	if c == nil {
		return "", nil
	}
	name := strings.TrimSpace(*c)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxCollectionLn {
		return "", invalid("collection", "1 to %d characters", maxCollectionLn)
	}
	return name, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

const insertDraftSQL = `INSERT INTO recipe_drafts(id, origin, source_recipe_id, parent_track_id, group_key, label, request_id, request_json,
	status, created_at, updated_at, revision, created_by, updated_by) VALUES (?,?,NULL,?,?,?,?,?,'backlog',?,?,1,?,?)`

func insertDraft(ctx context.Context, tx *sql.Tx, id, origin, parent, label string, p yue2.Parsed, by, at string) error {
	if _, err := tx.ExecContext(ctx, insertDraftSQL, id, origin, nullable(parent), yue2.GroupKey(p.Request.ID), nullable(label),
		p.Request.ID, string(p.Canonical), at, at, by, by); err != nil {
		return fmt.Errorf("drafts: insert: %w", err)
	}
	return nil
}

func addToCollection(ctx context.Context, tx *sql.Tx, id, name, source string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO draft_collections(draft_id, name, source) VALUES (?,?,?) ON CONFLICT(draft_id, name) DO NOTHING`, id, name, source); err != nil {
		return fmt.Errorf("drafts: collection: %w", err)
	}
	return nil
}

// ---- create / update ----

// Create stores a new draft: origin editor (human or device) or agent, status backlog, revision 1.
func (s *Service) Create(ctx context.Context, p auth.Principal, in NewDraft) (library.DraftDetail, error) {
	parsed, warns, err := parseOne(in.Request)
	if err != nil {
		return library.DraftDetail{}, err
	}
	if err := s.checkLabel(in.Label); err != nil {
		return library.DraftDetail{}, err
	}
	if err := s.checkParent(ctx, in.ParentTrackID); err != nil {
		return library.DraftDetail{}, err
	}
	coll, err := checkCollection(in.Collection)
	if err != nil {
		return library.DraftDetail{}, err
	}
	origin := "agent"
	if p.ActsAsHuman() {
		origin = "editor"
	}
	id, label, parent := s.newID(), "", ""
	if in.Label != nil {
		label = *in.Label
	}
	if in.ParentTrackID != nil {
		parent = *in.ParentTrackID
	}
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return library.DraftDetail{}, fmt.Errorf("drafts: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := insertDraft(ctx, tx, id, origin, parent, label, parsed, p.Key(), s.stamp()); err != nil {
		return library.DraftDetail{}, err
	}
	if coll != "" {
		if err := addToCollection(ctx, tx, id, coll, origin+":"+id); err != nil {
			return library.DraftDetail{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return library.DraftDetail{}, fmt.Errorf("drafts: commit: %w", err)
	}
	return s.result(ctx, p, id, warns)
}

func (s *Service) result(ctx context.Context, p auth.Principal, id string, carry []yue2.Issue) (library.DraftDetail, error) {
	d, err := s.lib.Draft(ctx, id, p.ActsAsHuman())
	if err != nil {
		return d, err
	}
	return d, s.decorateDetail(ctx, &d, carry)
}

// Update edits a draft under optimistic locking (C27 update SQL).
func (s *Service) Update(ctx context.Context, p auth.Principal, id string, in DraftPatch) (library.DraftDetail, error) {
	if in.Revision < 1 {
		return library.DraftDetail{}, invalid("revision", "revision is required")
	}
	hasReq := len(bytes.TrimSpace(in.Request)) > 0
	if !hasReq && in.Label == nil && in.Status == nil && in.ParentTrackID == nil {
		return library.DraftDetail{}, invalid("body", "send at least one of request, label, status, parent_track_id")
	}
	if in.Status != nil && *in.Status != "backlog" && *in.Status != "archived" {
		return library.DraftDetail{}, invalid("status", "status must be backlog or archived")
	}
	if err := s.checkLabel(in.Label); err != nil {
		return library.DraftDetail{}, err
	}
	var parsed yue2.Parsed
	var warns []yue2.Issue
	if hasReq {
		var err error
		if parsed, warns, err = parseOne(in.Request); err != nil {
			return library.DraftDetail{}, err
		}
	}
	if err := s.checkParent(ctx, in.ParentTrackID); err != nil {
		return library.DraftDetail{}, err
	}

	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return library.DraftDetail{}, fmt.Errorf("drafts: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var cur struct {
		status, requestID, reqJSON string
		revision                   int
		groupKey, label, parent    sql.NullString
	}
	err = tx.QueryRowContext(ctx, `SELECT status, revision, request_id, request_json, group_key, label, parent_track_id FROM recipe_drafts WHERE id = ?`, id).
		Scan(&cur.status, &cur.revision, &cur.requestID, &cur.reqJSON, &cur.groupKey, &cur.label, &cur.parent)
	if errors.Is(err, sql.ErrNoRows) {
		return library.DraftDetail{}, library.ErrNotFound
	}
	if err != nil {
		return library.DraftDetail{}, fmt.Errorf("drafts: read: %w", err)
	}
	if cerr := classify(cur.status, cur.revision, in.Revision); cerr != nil {
		return library.DraftDetail{}, cerr
	}
	reqID, reqJSON, groupKey := cur.requestID, cur.reqJSON, cur.groupKey
	if hasReq {
		reqID, reqJSON = parsed.Request.ID, string(parsed.Canonical)
		if reqID != cur.requestID {
			groupKey = sql.NullString{String: yue2.GroupKey(reqID), Valid: true}
		}
	}
	label, status, parent := cur.label, cur.status, cur.parent
	if in.Label != nil {
		label = sql.NullString{String: *in.Label, Valid: *in.Label != ""}
	}
	if in.Status != nil {
		status = *in.Status
	}
	if in.ParentTrackID != nil {
		parent = sql.NullString{String: *in.ParentTrackID, Valid: *in.ParentTrackID != ""}
	}
	res, err := tx.ExecContext(ctx, `UPDATE recipe_drafts SET request_id=?, request_json=?, group_key=?, label=?, status=?, parent_track_id=?,
		revision=revision+1, updated_by=?, updated_at=? WHERE id=? AND revision=? AND status <> 'fulfilled'`,
		reqID, reqJSON, groupKey, label, status, parent, p.Key(), s.stamp(), id, in.Revision)
	if err != nil {
		return library.DraftDetail{}, fmt.Errorf("drafts: update: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Lost a race between the read and the write: re-read to tell the cases apart.
		if err := tx.QueryRowContext(ctx, `SELECT status, revision FROM recipe_drafts WHERE id = ?`, id).Scan(&cur.status, &cur.revision); err != nil {
			return library.DraftDetail{}, library.ErrNotFound
		}
		if cerr := classify(cur.status, cur.revision, in.Revision); cerr != nil {
			return library.DraftDetail{}, cerr
		}
		return library.DraftDetail{}, fmt.Errorf("drafts: update touched no row")
	}
	if err := tx.Commit(); err != nil {
		return library.DraftDetail{}, fmt.Errorf("drafts: commit: %w", err)
	}
	return s.result(ctx, p, id, warns)
}

// classify turns the state of a draft into the conflict the caller must see, or nil when the update
// may proceed.
func classify(status string, current, sent int) error {
	switch {
	case status == "fulfilled":
		return &Error{Status: 409, Code: "DRAFT_NOT_EDITABLE", Msg: "the draft is fulfilled and can no longer be edited", Details: map[string]any{}}
	case current != sent:
		return &Error{Status: 409, Code: "REVISION_CONFLICT", Msg: "the draft changed since you read it", Details: map[string]any{"current_revision": current}}
	}
	return nil
}

// ---- upload ----

type okLine struct {
	line   int
	parsed yue2.Parsed
	warns  []yue2.Issue
}

func abcResolver(files map[string]string) yue2.ABCResolver {
	return func(rel string) ([]byte, error) {
		if !filepath.IsLocal(rel) || strings.ContainsRune(rel, '\\') {
			return nil, fs.ErrInvalid
		}
		text, ok := files[rel]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return []byte(text), nil
	}
}

// Upload creates drafts from a JSONL document in one write transaction.
func (s *Service) Upload(ctx context.Context, p auth.Principal, in Upload) (UploadResult, error) {
	res := UploadResult{Drafts: []UploadDraft{}, Errors: []UploadIssue{}, Warnings: []UploadIssue{}}
	if len(in.JSONL) > maxUploadJSONL {
		return res, invalid("jsonl", "at most 4 MiB of JSONL")
	}
	if len(in.ABCFiles) > maxUploadFiles {
		return res, invalid("abc_files", "at most %d ABC files", maxUploadFiles)
	}
	for _, text := range in.ABCFiles {
		if len(text) > maxUploadABC {
			return res, invalid("abc_files", "each ABC file is at most 1 MiB")
		}
	}
	type rawLine struct {
		no   int
		text string
	}
	var lines []rawLine
	for i, l := range strings.Split(in.JSONL, "\n") {
		l = strings.TrimRight(l, "\r")
		if strings.TrimSpace(l) == "" {
			continue
		}
		if lines = append(lines, rawLine{i + 1, l}); len(lines) > maxUploadLines {
			return res, invalid("jsonl", "at most %d lines", maxUploadLines)
		}
	}
	if len(lines) == 0 {
		return res, invalid("jsonl", "no requests in the upload")
	}
	coll, err := checkCollection(in.Collection)
	if err != nil {
		return res, err
	}
	if err := s.checkParent(ctx, in.ParentTrackID); err != nil {
		return res, err
	}

	resolve := abcResolver(in.ABCFiles)
	failed := map[int]*FailedItem{}
	fail := func(line int, is yue2.Issue, warns []yue2.Issue) {
		f := failed[line]
		if f == nil {
			f = &FailedItem{Line: line, Errors: []yue2.Issue{}, Warnings: warns}
			failed[line] = f
		}
		f.Errors = append(f.Errors, is)
	}
	var ok []okLine
	for _, l := range lines {
		errs, warns := splitIssues(yue2.CheckRequest([]byte(l.text), yue2.ModeApp, resolve))
		if len(errs) > 0 {
			for _, e := range errs {
				fail(l.no, e, warns)
			}
			continue
		}
		parsed, err := yue2.ParseRequest([]byte(l.text), yue2.ParseOptions{ResolveABCPath: resolve})
		if err != nil {
			fail(l.no, yue2.Issue{Code: "not_object", Severity: "error"}, warns)
			continue
		}
		ok = append(ok, okLine{l.no, parsed, warns})
	}

	items := make([]yue2.LintItem, len(ok))
	for i, o := range ok {
		items[i] = yue2.LintItem{Request: o.parsed.Canonical, Carry: o.warns}
	}
	rep, err := s.lintWith(ctx, items, true)
	if err != nil {
		return res, err
	}
	drop := map[int]bool{}
	for _, is := range rep.BatchErrors {
		if is.Line >= 1 && is.Line <= len(ok) {
			i := is.Line - 1
			is.Line = 0
			fail(ok[i].line, is, rep.Items[i].Warnings)
			drop[ok[i].line] = true
		}
	}
	for i, it := range rep.Items {
		for _, e := range it.Errors {
			fail(ok[i].line, e, it.Warnings)
			drop[ok[i].line] = true
		}
	}

	var failedList []FailedItem
	for _, f := range failed {
		failedList = append(failedList, *f)
		for _, e := range f.Errors {
			res.Errors = append(res.Errors, UploadIssue{Line: f.Line, Code: e.Code, Field: e.Field})
		}
	}
	sort.Slice(failedList, func(a, b int) bool { return failedList[a].Line < failedList[b].Line })
	sort.SliceStable(res.Errors, func(a, b int) bool { return res.Errors[a].Line < res.Errors[b].Line })
	if len(failedList) > 0 && !in.AllowPartial {
		return UploadResult{Drafts: []UploadDraft{}, Errors: []UploadIssue{}, Warnings: []UploadIssue{}}, lintFailed("the upload has protocol errors; nothing was stored", failedList)
	}

	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("drafts: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	at, parent, first := s.stamp(), "", ""
	if in.ParentTrackID != nil {
		parent = *in.ParentTrackID
	}
	for i, o := range ok {
		if drop[o.line] {
			continue
		}
		id := s.newID()
		if first == "" {
			first = id
		}
		reqID := o.parsed.Request.ID
		var label *string
		if gk := yue2.GroupKey(reqID); gk != reqID {
			l := reqID[len(gk)+1:]
			label = &l
		}
		lv := ""
		if label != nil {
			lv = *label
		}
		if err := insertDraft(ctx, tx, id, "jsonl_upload", parent, lv, o.parsed, p.Key(), at); err != nil {
			return res, err
		}
		if coll != "" {
			if err := addToCollection(ctx, tx, id, coll, "upload:"+first); err != nil {
				return res, err
			}
		}
		res.Drafts = append(res.Drafts, UploadDraft{Line: o.line, ID: id, RequestID: reqID, Label: label})
		for _, w := range rep.Items[i].Warnings {
			res.Warnings = append(res.Warnings, UploadIssue{Line: o.line, Code: w.Code, Field: w.Field})
		}
	}
	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("drafts: commit: %w", err)
	}
	res.Created = len(res.Drafts)
	if coll != "" && res.Created > 0 {
		res.Collection = &coll
	}
	return res, nil
}
