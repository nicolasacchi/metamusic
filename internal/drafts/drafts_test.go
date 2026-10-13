package drafts_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/drafts"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type env struct {
	st  *store.Store
	lib *library.Service
	svc *drafts.Service
	now time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	dir := t.TempDir()
	lt.Fixture(t, st, lt.FixedNow)
	lt.WriteFiles(t, dir)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	e := &env{st: st, now: lt.FixedNow.Add(time.Hour)}
	e.lib = library.New(st, root, func() time.Time { return e.now })
	var n atomic.Int64
	n.Store(0x7000)
	e.svc = drafts.New(st, e.lib, func() time.Time { return e.now }, func() string {
		return fmt.Sprintf("01900000-0000-7000-8000-%012x", n.Add(1))
	})
	return e
}

var (
	human = auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}
	agent = auth.Principal{Kind: auth.KindAgent, ID: "tok-1", Name: "claude", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}
)

const verse = "oro kala\nvenu tilo\nmira dosa\nlano pera\n"

func lyricsOf(extra string) string { return "[Verse]\n" + verse + "[Chorus]\n" + verse + extra }

func reqJSON(id, lyrics string, more ...string) json.RawMessage {
	extra := ""
	if len(more) > 0 {
		extra = "," + strings.Join(more, ",")
	}
	b, _ := json.Marshal(lyrics)
	return json.RawMessage(fmt.Sprintf(`{"id":%q,"style":"english, 100 bpm","lyrics":%s,"cot":"full","seed":11%s}`, id, b, extra))
}

func asErr(t *testing.T, err error, status int, code string) *drafts.Error {
	t.Helper()
	var de *drafts.Error
	if !errors.As(err, &de) {
		t.Fatalf("want *drafts.Error %s, got %v", code, err)
	}
	if de.Status != status || de.Code != code {
		t.Fatalf("got %d %s (%s), want %d %s", de.Status, de.Code, de.Msg, status, code)
	}
	return de
}

func count(t *testing.T, e *env, q string, a ...any) int {
	t.Helper()
	var n int
	if err := e.st.R.QueryRow(q, a...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func exec(t *testing.T, e *env, q string, a ...any) {
	t.Helper()
	if _, err := e.st.W.Exec(q, a...); err != nil {
		t.Fatal(err)
	}
}

func str(s string) *string { return &s }

func TestDrafts_CreateUpdateRevision(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()

	d, err := e.svc.Create(ctx, human, drafts.NewDraft{Request: reqJSON("song-a_v1", lyricsOf(""))})
	if err != nil {
		t.Fatal(err)
	}
	if d.Revision != 1 || d.Origin != "editor" || d.Status != "backlog" || !d.Editable || d.CreatedBy != human.Key() || d.UpdatedBy != human.Key() {
		t.Fatalf("created: %+v", d)
	}
	if d.RequestID != "song-a_v1" || d.GroupKey == nil || *d.GroupKey != "song-a" || d.Source != nil {
		t.Errorf("ids: %+v", d)
	}
	if d.Lint.Index != 0 || d.Lint.Hash == "" || len(d.Lint.Errors) != 0 {
		t.Errorf("lint %+v", d.Lint)
	}
	a, err := e.svc.Create(ctx, agent, drafts.NewDraft{Request: reqJSON("song-b", lyricsOf(""))})
	if err != nil {
		t.Fatal(err)
	}
	if a.Origin != "agent" || a.CreatedBy != agent.Key() {
		t.Errorf("agent draft: %+v", a)
	}

	e.now = e.now.Add(time.Minute)
	up, err := e.svc.Update(ctx, agent, d.ID, drafts.DraftPatch{Revision: 1, Request: reqJSON("song-a_v1", lyricsOf("[Outro]\nzebra moon\n"))})
	if err != nil {
		t.Fatal(err)
	}
	if up.Revision != 2 || up.UpdatedBy != agent.Key() || up.CreatedBy != human.Key() || up.UpdatedAt == up.CreatedAt || up.Origin != "editor" {
		t.Errorf("updated: %+v", up)
	}
	if n := count(t, e, `SELECT count(*) FROM drafts_fts WHERE drafts_fts MATCH 'zebra'`); n != 1 {
		t.Errorf("fts finds the new lyrics: %d", n)
	}

	// stale revision
	de := asErr(t, mustErr(e.svc.Update(ctx, human, d.ID, drafts.DraftPatch{Revision: 1, Label: str("x")})), 409, "REVISION_CONFLICT")
	if de.Details["current_revision"] != 2 {
		t.Errorf("details %v", de.Details)
	}
	// label set and cleared
	up, err = e.svc.Update(ctx, human, d.ID, drafts.DraftPatch{Revision: 2, Label: str("take two")})
	if err != nil || up.Label == nil || *up.Label != "take two" || up.Revision != 3 {
		t.Fatalf("label: %+v %v", up, err)
	}
	up, err = e.svc.Update(ctx, human, d.ID, drafts.DraftPatch{Revision: 3, Label: str("")})
	if err != nil || up.Label != nil {
		t.Fatalf("clear label: %+v %v", up, err)
	}
	// archived, then back to backlog
	up, err = e.svc.Update(ctx, human, d.ID, drafts.DraftPatch{Revision: 4, Status: str("archived")})
	if err != nil || up.Status != "archived" || !up.Editable {
		t.Fatalf("archive: %+v %v", up, err)
	}
	if list, _ := e.lib.ListDrafts(ctx, library.DraftParams{Q: "zebra"}); len(list.Items) != 0 {
		t.Errorf("archived draft in backlog: %+v", list.Items)
	}
	up, err = e.svc.Update(ctx, human, d.ID, drafts.DraftPatch{Revision: 5, Status: str("backlog")})
	if err != nil || up.Status != "backlog" {
		t.Fatalf("restore: %+v %v", up, err)
	}
	// a bad status
	asErr(t, mustErr(e.svc.Update(ctx, human, d.ID, drafts.DraftPatch{Revision: 6, Status: str("fulfilled")})), 422, "VALIDATION_FAILED")
	// no fields
	asErr(t, mustErr(e.svc.Update(ctx, human, d.ID, drafts.DraftPatch{Revision: 6})), 422, "VALIDATION_FAILED")
	// unknown draft
	if _, err := e.svc.Update(ctx, human, "nope", drafts.DraftPatch{Revision: 1, Label: str("x")}); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown draft: %v", err)
	}

	// fulfilled drafts are not editable (even with a stale revision)
	exec(t, e, `UPDATE recipe_drafts SET status='fulfilled', fulfilled_by_track_id=? WHERE id=?`, lt.TrackLuna, a.ID)
	asErr(t, mustErr(e.svc.Update(ctx, human, a.ID, drafts.DraftPatch{Revision: 1, Label: str("x")})), 409, "DRAFT_NOT_EDITABLE")
	asErr(t, mustErr(e.svc.Update(ctx, human, a.ID, drafts.DraftPatch{Revision: 9, Label: str("x")})), 409, "DRAFT_NOT_EDITABLE")

	// parse errors are 422 LINT_FAILED with the issues; nothing is stored
	before := count(t, e, `SELECT count(*) FROM recipe_drafts`)
	de = asErr(t, mustErr(e.svc.Create(ctx, human, drafts.NewDraft{Request: json.RawMessage(`{"id":"x","style":"s","lyrics":"l","seed":9007199254740992}`)})), 422, "LINT_FAILED")
	items, _ := de.Details["items"].([]drafts.FailedItem)
	if len(items) != 1 || items[0].Errors[0].Code != "seed_out_of_range" {
		t.Errorf("details %+v", de.Details)
	}
	asErr(t, mustErr(e.svc.Update(ctx, human, d.ID, drafts.DraftPatch{Revision: 6, Request: json.RawMessage(`{"id":"x"}`)})), 422, "LINT_FAILED")
	if count(t, e, `SELECT count(*) FROM recipe_drafts`) != before {
		t.Error("a failed create stored a row")
	}

	// dropped keys are warnings, not errors
	w, err := e.svc.Create(ctx, human, drafts.NewDraft{Request: reqJSON("song-w", lyricsOf(""), `"title":"T"`)})
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(w.Lint, "dropped_key:title") {
		t.Errorf("dropped key warning missing: %+v", w.Lint.Warnings)
	}
	var stored string
	_ = e.st.R.QueryRow(`SELECT request_json FROM recipe_drafts WHERE id=?`, w.ID).Scan(&stored)
	if strings.Contains(stored, "title") {
		t.Errorf("stored request keeps the dropped key: %s", stored)
	}

	// an id change gives a new group key; the label is kept
	g, err := e.svc.Update(ctx, human, d.ID, drafts.DraftPatch{Revision: 6, Request: reqJSON("other_v2", lyricsOf(""))})
	if err != nil || g.RequestID != "other_v2" || g.GroupKey == nil || *g.GroupKey != "other" {
		t.Fatalf("id change: %+v %v", g, err)
	}

	// input validation
	asErr(t, mustErr(e.svc.Create(ctx, human, drafts.NewDraft{Request: reqJSON("v1", lyricsOf(""), `"x":1`), Label: str(strings.Repeat("l", 41))})), 422, "VALIDATION_FAILED")
	asErr(t, mustErr(e.svc.Create(ctx, human, drafts.NewDraft{Request: reqJSON("v2", lyricsOf("")), ParentTrackID: str("missing")})), 422, "VALIDATION_FAILED")
	c, err := e.svc.Create(ctx, human, drafts.NewDraft{Request: reqJSON("v3", lyricsOf("")), ParentTrackID: str(lt.TrackLuna), Collection: str("my set")})
	if err != nil || c.ParentTrackID == nil || *c.ParentTrackID != lt.TrackLuna || len(c.Collections) != 1 || c.Collections[0] != "my set" {
		t.Fatalf("parent+collection: %+v %v", c, err)
	}
	cl, err := e.svc.Update(ctx, human, c.ID, drafts.DraftPatch{Revision: 1, ParentTrackID: str("")})
	if err != nil || cl.ParentTrackID != nil {
		t.Fatalf("clear parent: %+v %v", cl, err)
	}
	asErr(t, mustErr(e.svc.Update(ctx, human, c.ID, drafts.DraftPatch{Revision: 2, ParentTrackID: str("missing")})), 422, "VALIDATION_FAILED")
}

func mustErr(_ library.DraftDetail, err error) error { return err }

func hasWarning(l yue2.LintResult, code string) bool {
	for _, w := range l.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

func TestDrafts_EditImportedKeepsSource(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	for _, id := range []string{lt.Draft1, lt.DraftVarianti} {
		before, err := e.lib.Draft(ctx, id, true)
		if err != nil || before.Source == nil || !before.Editable {
			t.Fatalf("%s: %+v %v", id, before, err)
		}
		var srcBefore, rawBefore string
		_ = e.st.R.QueryRow(`SELECT source_recipe_id FROM recipe_drafts WHERE id=?`, id).Scan(&srcBefore)
		_ = e.st.R.QueryRow(`SELECT group_concat(raw_sha256 || source_path || line_no) FROM recipe_sources WHERE recipe_id=?`, srcBefore).Scan(&rawBefore)
		nreq := reqJSON(before.RequestID, lyricsOf("[Outro]\nedited words\n"))
		after, err := e.svc.Update(ctx, human, id, drafts.DraftPatch{Revision: before.Revision, Request: nreq})
		if err != nil {
			t.Fatal(err)
		}
		if after.Revision != before.Revision+1 || after.Origin != before.Origin || after.Source == nil || after.Source.RecipeID != srcBefore {
			t.Errorf("%s: %+v", id, after)
		}
		var srcAfter, rawAfter string
		_ = e.st.R.QueryRow(`SELECT source_recipe_id FROM recipe_drafts WHERE id=?`, id).Scan(&srcAfter)
		_ = e.st.R.QueryRow(`SELECT group_concat(raw_sha256 || source_path || line_no) FROM recipe_sources WHERE recipe_id=?`, srcAfter).Scan(&rawAfter)
		if srcAfter != srcBefore || rawAfter != rawBefore || rawBefore == "" {
			t.Errorf("%s: source changed: %s/%s %s/%s", id, srcBefore, srcAfter, rawBefore, rawAfter)
		}
		// an unchanged group key: the request id did not change
		if (before.GroupKey == nil) != (after.GroupKey == nil) || (before.GroupKey != nil && *before.GroupKey != *after.GroupKey) {
			t.Errorf("%s: group key %v -> %v", id, before.GroupKey, after.GroupKey)
		}
		// the stored recipe is untouched
		var rj string
		_ = e.st.R.QueryRow(`SELECT request_json FROM recipes WHERE id=?`, srcBefore).Scan(&rj)
		if strings.Contains(rj, "edited words") {
			t.Errorf("%s: recipe edited", id)
		}
		// the edited draft is not a duplicate of its own source recipe
		if hasWarning(after.Lint, "duplicate_recipe") {
			t.Errorf("%s: self duplicate", id)
		}
	}
}

func TestDrafts_LintDecoration(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	// a draft that duplicates a stored recipe, and one that duplicates another draft
	var recipeJSON string
	_ = e.st.R.QueryRow(`SELECT request_json FROM recipes ORDER BY id LIMIT 1`).Scan(&recipeJSON)
	dupRecipe, err := e.svc.Create(ctx, human, drafts.NewDraft{Request: json.RawMessage(recipeJSON)})
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(dupRecipe.Lint, "duplicate_recipe") {
		t.Errorf("recipe duplicate not reported: %+v", dupRecipe.Lint.Warnings)
	}
	one, _ := e.svc.Create(ctx, human, drafts.NewDraft{Request: reqJSON("lone", lyricsOf(""))})
	if hasWarning(one.Lint, "duplicate_recipe") {
		t.Errorf("lone draft flagged")
	}
	two, _ := e.svc.Create(ctx, human, drafts.NewDraft{Request: reqJSON("lone", lyricsOf(""))})
	if !hasWarning(two.Lint, "duplicate_recipe") {
		t.Errorf("draft duplicate not reported")
	}

	page, err := e.lib.ListDrafts(ctx, library.DraftParams{Limit: 100})
	if err != nil || len(page.Items) < 6 {
		t.Fatalf("page: %d %v", len(page.Items), err)
	}
	for _, o := range []string{"", "archived"} {
		_ = o
	}
	if err := e.svc.Decorate(ctx, page.Items); err != nil {
		t.Fatal(err)
	}
	for _, s := range page.Items {
		d, err := e.lib.Draft(ctx, s.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.svc.DecorateDetail(ctx, &d); err != nil {
			t.Fatal(err)
		}
		// recompute from the stored request
		var rj string
		var src *string
		_ = e.st.R.QueryRow(`SELECT request_json, source_recipe_id FROM recipe_drafts WHERE id=?`, s.ID).Scan(&rj, &src)
		item := yue2.LintItem{Request: json.RawMessage(rj), DraftID: s.ID}
		if src != nil {
			item.SourceRecipeID = *src
		}
		rep, err := e.svc.Lint(ctx, []yue2.LintItem{item}, false)
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(rep.Items[0], d.Lint); diff != "" {
			t.Errorf("%s: DecorateDetail != Lint (-lint +detail):\n%s", s.RequestID, diff)
		}
		want := yue2.LintCounts{Errors: len(rep.Items[0].Errors), Warnings: len(rep.Items[0].Warnings)}
		if s.Lint != want {
			t.Errorf("%s: summary lint %+v, want %+v", s.RequestID, s.Lint, want)
		}
		if s.Lint.Warnings == 0 {
			t.Errorf("%s: fixture drafts carry songwriting warnings", s.RequestID)
		}
		if s.Lint.Errors != 0 {
			t.Errorf("%s: fixture draft has errors %+v", s.RequestID, rep.Items[0].Errors)
		}
	}
	// the results of create and update carry the same lint as get
	g, _ := e.lib.Draft(ctx, two.ID, true)
	_ = e.svc.DecorateDetail(ctx, &g)
	if diff := cmp.Diff(g.Lint, two.Lint); diff != "" {
		t.Errorf("create result lint != get lint:\n%s", diff)
	}
	up, err := e.svc.Update(ctx, human, two.ID, drafts.DraftPatch{Revision: 1, Request: reqJSON("lone2", lyricsOf("[Outro]\nx y\n"))})
	if err != nil {
		t.Fatal(err)
	}
	g, _ = e.lib.Draft(ctx, two.ID, true)
	_ = e.svc.DecorateDetail(ctx, &g)
	if diff := cmp.Diff(g.Lint, up.Lint); diff != "" {
		t.Errorf("update result lint != get lint:\n%s", diff)
	}
	// an empty page is fine
	if err := e.svc.Decorate(ctx, nil); err != nil {
		t.Error(err)
	}
}

func TestDrafts_LintByDraftID(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	var rj string
	_ = e.st.R.QueryRow(`SELECT request_json FROM recipe_drafts WHERE id=?`, lt.Draft1).Scan(&rj)
	// without draft_id the draft's own request duplicates its recipe; with it, it does not
	rep, _ := e.svc.Lint(ctx, []yue2.LintItem{{Request: json.RawMessage(rj)}}, true)
	if !hasWarning(rep.Items[0], "duplicate_recipe") {
		t.Errorf("no draft id: %+v", rep.Items[0].Warnings)
	}
	rep, _ = e.svc.Lint(ctx, []yue2.LintItem{{Request: json.RawMessage(rj), DraftID: lt.Draft1}}, true)
	if hasWarning(rep.Items[0], "duplicate_recipe") {
		t.Errorf("draft id given: %+v", rep.Items[0].Warnings)
	}
	rep, err := e.svc.Lint(ctx, []yue2.LintItem{{Request: json.RawMessage(`{"id":1}`)}}, true)
	if err != nil || rep.OK {
		t.Errorf("invalid: %+v %v", rep, err)
	}
}

const abcText = "X:1\nT:t\nM:4/4\nQ:1/4=120\nK:C\nCDEF|CDEF|CDEF|CDEF|\n"

func jsonl(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

func line(id string, more ...string) string {
	return string(reqJSON(id, lyricsOf(""), more...))
}

func TestUploadBatch_AllOrNothing(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	before := count(t, e, `SELECT count(*) FROM recipe_drafts`)
	bad := strings.Replace(line("up-b"), `"cot":"full"`, `"cot":"none"`, 1)
	_, err := e.svc.Upload(ctx, human, drafts.Upload{JSONL: jsonl(line("up-a"), bad, line("up-c"))})
	de := asErr(t, err, 422, "LINT_FAILED")
	items, _ := de.Details["items"].([]drafts.FailedItem)
	if len(items) != 1 || items[0].Line != 2 || items[0].Errors[0].Code != "bad_cot" {
		t.Errorf("items %+v", de.Details["items"])
	}
	if count(t, e, `SELECT count(*) FROM recipe_drafts`) != before {
		t.Error("rows written despite the failure")
	}
}

func TestUploadBatch_AllowPartial(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	bad := strings.Replace(line("up-b"), `"cot":"full"`, `"cot":"none"`, 1)
	res, err := e.svc.Upload(context.Background(), agent, drafts.Upload{JSONL: jsonl(line("up-a_v2"), bad, "", line("up-c", `"title":"T"`)), AllowPartial: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 2 || len(res.Drafts) != 2 || len(res.Errors) != 1 || res.Errors[0].Line != 2 || res.Errors[0].Code != "bad_cot" || res.Errors[0].Field != "cot" {
		t.Fatalf("result %+v", res)
	}
	if res.Drafts[0].Line != 1 || res.Drafts[1].Line != 4 || res.Drafts[0].RequestID != "up-a_v2" {
		t.Errorf("drafts %+v", res.Drafts)
	}
	if res.Drafts[0].Label == nil || *res.Drafts[0].Label != "v2" || res.Drafts[1].Label != nil {
		t.Errorf("labels %+v %+v", res.Drafts[0].Label, res.Drafts[1].Label)
	}
	foundDropped := false
	for _, w := range res.Warnings {
		if w.Code == "dropped_key:title" && w.Line == 4 {
			foundDropped = true
		}
	}
	if !foundDropped {
		t.Errorf("warnings %+v", res.Warnings)
	}
	if n := count(t, e, `SELECT count(*) FROM recipe_drafts WHERE origin='jsonl_upload' AND created_by=?`, agent.Key()); n != 2 {
		t.Errorf("stored %d", n)
	}
	if res.Collection != nil {
		t.Errorf("collection %v", *res.Collection)
	}
	// everything failed: nothing created, no error
	res, err = e.svc.Upload(context.Background(), agent, drafts.Upload{JSONL: jsonl(bad), AllowPartial: true})
	if err != nil || res.Created != 0 || len(res.Errors) != 1 || res.Drafts == nil || res.Warnings == nil {
		t.Errorf("all failed: %+v %v", res, err)
	}
}

func TestUploadBatch_DuplicateIDs(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	in := drafts.Upload{JSONL: jsonl(line("dup-a"), "   ", line("dup-b"), line("dup-a", `"seed":12`))}
	_, err := e.svc.Upload(ctx, human, in)
	de := asErr(t, err, 422, "LINT_FAILED")
	items, _ := de.Details["items"].([]drafts.FailedItem)
	if len(items) != 1 || items[0].Line != 4 || items[0].Errors[0].Code != "duplicate_id" {
		t.Errorf("items %+v", de.Details["items"])
	}
	in.AllowPartial = true
	res, err := e.svc.Upload(ctx, human, in)
	if err != nil || res.Created != 2 || len(res.Errors) != 1 || res.Errors[0].Line != 4 || res.Errors[0].Code != "duplicate_id" {
		t.Errorf("partial: %+v %v", res, err)
	}
}

func TestUploadBatch_ABCFiles(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	l := line("abc-a", `"cot":"melody"`, `"abc_path":"scores/a.abc"`)
	l = strings.Replace(l, `"cot":"full",`, "", 1)
	res, err := e.svc.Upload(ctx, human, drafts.Upload{JSONL: jsonl(l), ABCFiles: map[string]string{"scores/a.abc": abcText}})
	if err != nil || res.Created != 1 {
		t.Fatalf("upload: %+v %v", res, err)
	}
	var rj string
	_ = e.st.R.QueryRow(`SELECT request_json FROM recipe_drafts WHERE id=?`, res.Drafts[0].ID).Scan(&rj)
	if !strings.Contains(rj, `"abc":"X:1\nT:t`) || strings.Contains(rj, "abc_path") {
		t.Errorf("abc not inlined: %s", rj)
	}
	d, _ := e.lib.Draft(ctx, res.Drafts[0].ID, true)
	_ = e.svc.DecorateDetail(ctx, &d)
	if len(d.Lint.Errors) != 0 || d.Lint.DurationBasis != "abc" || d.Request.ABC == nil {
		t.Errorf("lint %+v", d.Lint)
	}
	for _, bad := range []string{"../x.abc", "/abs.abc", "scores\\a.abc", "missing.abc"} {
		l := strings.Replace(line("abc-b", `"abc_path":"`+strings.ReplaceAll(bad, `\`, `\\`)+`"`), `"cot":"full"`, `"cot":"melody"`, 1)
		_, err := e.svc.Upload(ctx, human, drafts.Upload{JSONL: jsonl(l), ABCFiles: map[string]string{"scores/a.abc": abcText, "../x.abc": abcText, "/abs.abc": abcText, "scores\\a.abc": abcText}})
		de := asErr(t, err, 422, "LINT_FAILED")
		items, _ := de.Details["items"].([]drafts.FailedItem)
		if len(items) != 1 || items[0].Errors[0].Code != "abc_path_unresolved" {
			t.Errorf("%s: %+v", bad, de.Details)
		}
	}
}

func TestUploadBatch_Collection(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	res, err := e.svc.Upload(context.Background(), human, drafts.Upload{JSONL: jsonl(line("col-a"), line("col-b")), Collection: str("batch one"), ParentTrackID: str(lt.TrackLuna)})
	if err != nil || res.Created != 2 || res.Collection == nil || *res.Collection != "batch one" {
		t.Fatalf("%+v %v", res, err)
	}
	rows, err := e.st.R.Query(`SELECT draft_id, name, source FROM draft_collections WHERE name='batch one' ORDER BY draft_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id, name, src string
		_ = rows.Scan(&id, &name, &src)
		if src != "upload:"+res.Drafts[0].ID {
			t.Errorf("source %q for %s", src, id)
		}
		n++
	}
	if n != 2 {
		t.Errorf("members %d", n)
	}
	var parent string
	_ = e.st.R.QueryRow(`SELECT parent_track_id FROM recipe_drafts WHERE id=?`, res.Drafts[1].ID).Scan(&parent)
	if parent != lt.TrackLuna {
		t.Errorf("parent %q", parent)
	}
	asErr(t, errOnly(e.svc.Upload(context.Background(), human, drafts.Upload{JSONL: jsonl(line("col-c")), ParentTrackID: str("missing")})), 422, "VALIDATION_FAILED")
}

func errOnly(_ drafts.UploadResult, err error) error { return err }

func TestUploadBatch_Limits(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	field := func(err error) string {
		t.Helper()
		var de *drafts.Error
		if !errors.As(err, &de) {
			var ve *library.ValidationError
			if errors.As(err, &ve) {
				return ve.Field
			}
			t.Fatalf("not a validation error: %v", err)
		}
		if de.Status != 422 || de.Code != "VALIDATION_FAILED" {
			t.Fatalf("%d %s", de.Status, de.Code)
		}
		f, _ := de.Details["field"].(string)
		return f
	}
	many := strings.Repeat("{}\n", 1001)
	if f := field(errOnly(e.svc.Upload(ctx, human, drafts.Upload{JSONL: many}))); f != "jsonl" {
		t.Errorf("1001 lines: field %q", f)
	}
	// 1000 non-blank lines are fine to parse (they fail as lines, not as a limit); blank lines do not count
	blank := strings.Repeat("\n", 5000) + line("lim-a")
	if res, err := e.svc.Upload(ctx, human, drafts.Upload{JSONL: blank}); err != nil || res.Created != 1 {
		t.Errorf("blank lines: %+v %v", res, err)
	}
	if f := field(errOnly(e.svc.Upload(ctx, human, drafts.Upload{JSONL: strings.Repeat(" ", 4<<20+1)}))); f != "jsonl" {
		t.Errorf("4 MiB + 1: field %q", f)
	}
	files := map[string]string{}
	for i := 0; i < 51; i++ {
		files[fmt.Sprintf("f%d.abc", i)] = abcText
	}
	if f := field(errOnly(e.svc.Upload(ctx, human, drafts.Upload{JSONL: line("lim-b"), ABCFiles: files}))); f != "abc_files" {
		t.Errorf("51 files: field %q", f)
	}
	if f := field(errOnly(e.svc.Upload(ctx, human, drafts.Upload{JSONL: line("lim-b"), ABCFiles: map[string]string{"a.abc": strings.Repeat("x", 1<<20+1)}}))); f != "abc_files" {
		t.Errorf("1 MiB + 1: field %q", f)
	}
	// an empty upload is a validation error too
	if f := field(errOnly(e.svc.Upload(ctx, human, drafts.Upload{JSONL: "\n  \n"}))); f != "jsonl" {
		t.Errorf("empty: field %q", f)
	}
	// exactly 50 files of exactly 1 MiB are accepted
	okFiles := map[string]string{}
	for i := 0; i < 50; i++ {
		okFiles[fmt.Sprintf("f%d.abc", i)] = strings.Repeat("x", 1<<20)
	}
	if _, err := e.svc.Upload(ctx, human, drafts.Upload{JSONL: line("lim-c"), ABCFiles: okFiles}); err != nil {
		t.Errorf("limits are inclusive: %v", err)
	}
}

func TestUploadBatch_Atomic(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// a write failure in the middle rolls everything back: make the third insert fail via a trigger
	exec(t, e, `CREATE TRIGGER fail_third BEFORE INSERT ON recipe_drafts WHEN NEW.request_id = 'boom' BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	before := count(t, e, `SELECT count(*) FROM recipe_drafts`)
	_, err := e.svc.Upload(context.Background(), human, drafts.Upload{JSONL: jsonl(line("ok-a"), line("ok-b"), line("boom"))})
	if err == nil {
		t.Fatal("want an error")
	}
	if count(t, e, `SELECT count(*) FROM recipe_drafts`) != before {
		t.Error("partial rows survived the failure")
	}
}

func TestDrafts_JSONShapes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	res, err := e.svc.Upload(context.Background(), human, drafts.Upload{JSONL: jsonl(line("shape-a"))})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"created", "drafts", "errors", "warnings", "collection"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %s in %s", k, b)
		}
	}
	if !reflect.DeepEqual(m["errors"], []any{}) {
		t.Errorf("errors %v", m["errors"])
	}
}

func TestDrafts_StorageFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	good := drafts.NewDraft{Request: reqJSON("sf-a", lyricsOf(""))}

	// write failures roll back and surface as errors
	exec(t, e, `CREATE TRIGGER no_coll BEFORE INSERT ON draft_collections BEGIN SELECT RAISE(ABORT, 'no collections'); END`)
	if _, err := e.svc.Create(ctx, human, drafts.NewDraft{Request: reqJSON("sf-b", lyricsOf("")), Collection: str("c")}); err == nil {
		t.Error("create with a failing collection insert")
	}
	if _, err := e.svc.Upload(ctx, human, drafts.Upload{JSONL: line("sf-c"), Collection: str("c")}); err == nil {
		t.Error("upload with a failing collection insert")
	}
	if n := count(t, e, `SELECT count(*) FROM recipe_drafts WHERE request_id IN ('sf-b','sf-c')`); n != 0 {
		t.Errorf("%d rows survived", n)
	}
	d, err := e.svc.Create(ctx, human, good)
	if err != nil {
		t.Fatal(err)
	}
	exec(t, e, `CREATE TRIGGER no_upd BEFORE UPDATE ON recipe_drafts BEGIN SELECT RAISE(ABORT, 'no updates'); END`)
	if _, err := e.svc.Update(ctx, human, d.ID, drafts.DraftPatch{Revision: 1, Label: str("x")}); err == nil {
		t.Error("update with a failing write")
	}
	exec(t, e, `DROP TRIGGER no_upd`)

	// a closed database fails every entry point instead of panicking
	if err := e.st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Create(ctx, human, good); err == nil {
		t.Error("create on a closed store")
	}
	if _, err := e.svc.Update(ctx, human, d.ID, drafts.DraftPatch{Revision: 1, Label: str("x")}); err == nil {
		t.Error("update on a closed store")
	}
	if _, err := e.svc.Upload(ctx, human, drafts.Upload{JSONL: line("sf-d")}); err == nil {
		t.Error("upload on a closed store")
	}
	if _, err := e.svc.Lint(ctx, []yue2.LintItem{{Request: good.Request, DraftID: d.ID}}, true); err == nil {
		t.Error("lint on a closed store")
	}
	if _, err := e.svc.Lint(ctx, []yue2.LintItem{{Request: good.Request}}, true); err == nil {
		t.Error("lint without draft ids on a closed store")
	}
	if err := e.svc.Decorate(ctx, []library.DraftSummary{{ID: d.ID}}); err == nil {
		t.Error("decorate on a closed store")
	}
	dd := library.DraftDetail{ID: d.ID}
	if err := e.svc.DecorateDetail(ctx, &dd); err == nil {
		t.Error("decorate detail on a closed store")
	}
}

func TestDrafts_EdgeInputs(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	// New with nil clock and id function works
	s := drafts.New(e.st, e.lib, nil, nil)
	d, err := s.Create(ctx, human, drafts.NewDraft{Request: reqJSON("edge-a", lyricsOf("")), Collection: str("  spaced  ")})
	if err != nil || len(d.ID) != 36 || len(d.Collections) != 1 || d.Collections[0] != "spaced" {
		t.Fatalf("%+v %v", d, err)
	}
	// empty and whitespace requests, bad collection names, revision 0
	asErr(t, mustErr(s.Create(ctx, human, drafts.NewDraft{})), 422, "VALIDATION_FAILED")
	asErr(t, mustErr(s.Create(ctx, human, drafts.NewDraft{Request: json.RawMessage("  ")})), 422, "VALIDATION_FAILED")
	asErr(t, mustErr(s.Create(ctx, human, drafts.NewDraft{Request: reqJSON("edge-b", lyricsOf("")), Collection: str(" ")})), 422, "VALIDATION_FAILED")
	asErr(t, mustErr(s.Create(ctx, human, drafts.NewDraft{Request: json.RawMessage(`[1]`)})), 422, "LINT_FAILED")
	asErr(t, mustErr(s.Update(ctx, human, d.ID, drafts.DraftPatch{Label: str("x")})), 422, "VALIDATION_FAILED")
	asErr(t, mustErr(s.Update(ctx, human, d.ID, drafts.DraftPatch{Revision: 1, Label: str(strings.Repeat("é", 41))})), 422, "VALIDATION_FAILED")
	if e := (&drafts.Error{Code: "X", Msg: "m"}).Error(); e != "X: m" {
		t.Errorf("Error() = %q", e)
	}
	// uploads: a non-object line and CRLF line ends
	_, err = s.Upload(ctx, human, drafts.Upload{JSONL: "[1]\r\n" + line("edge-c") + "\r\n"})
	de := asErr(t, err, 422, "LINT_FAILED")
	if items, _ := de.Details["items"].([]drafts.FailedItem); len(items) != 1 || items[0].Line != 1 || items[0].Errors[0].Code != "not_object" {
		t.Errorf("items %+v", de.Details["items"])
	}
	res, err := s.Upload(ctx, human, drafts.Upload{JSONL: line("edge-d") + "\r\n"})
	if err != nil || res.Created != 1 {
		t.Errorf("crlf: %+v %v", res, err)
	}
	if _, err := s.Upload(ctx, human, drafts.Upload{JSONL: line("edge-e"), Collection: str(strings.Repeat("c", 81))}); err == nil {
		t.Error("81-character collection")
	}
	// lint with an unparseable item and a draft id naming no draft
	rep, err := s.Lint(ctx, []yue2.LintItem{{Request: json.RawMessage(`{`), DraftID: "ghost"}}, true)
	if err != nil || rep.OK {
		t.Errorf("%+v %v", rep, err)
	}
	// decorating a detail of a draft that does not exist
	if err := s.DecorateDetail(ctx, &library.DraftDetail{ID: "ghost"}); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("ghost detail: %v", err)
	}
}
