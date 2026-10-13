package drafts_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/drafts"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// newEnvM2c is newEnv over lt.FixtureM2c: a reference FLAC and a lossy reference next to the M2a rows.
func newEnvM2c(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	dir := t.TempDir()
	lt.FixtureM2c(t, st, lt.FixedNow)
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

// stubIDs returns the request ids of the stored stub recipes (the reference fixtures').
func stubIDs(t *testing.T, e *env) []string {
	t.Helper()
	rows, err := e.st.R.Query(`SELECT request_id FROM recipes WHERE engine = 'unknown' ORDER BY request_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if len(out) < 2 {
		t.Fatalf("stub recipes: %v", out)
	}
	return out
}

func detailField(t *testing.T, de *drafts.Error) any {
	t.Helper()
	return de.Details["field"]
}

func TestDrafts_RejectUnknownID(t *testing.T) {
	t.Parallel()
	e := newEnvM2c(t)
	ctx := context.Background()
	stub := stubIDs(t, e)[0]
	before := count(t, e, `SELECT count(*) FROM recipe_drafts`)
	_, err := e.svc.Create(ctx, human, drafts.NewDraft{Request: reqJSON(stub, lyricsOf(""))})
	de := asErr(t, err, 422, "RECIPE_UNKNOWN")
	if detailField(t, de) != "id" {
		t.Errorf("details %v", de.Details)
	}
	if count(t, e, `SELECT count(*) FROM recipe_drafts`) != before {
		t.Error("a draft was created")
	}

	// Update: a valid draft cannot be re-pointed at a stub id.
	d, err := e.svc.Create(ctx, human, drafts.NewDraft{Request: reqJSON("rej-ok", lyricsOf(""))})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Update(ctx, human, d.ID, drafts.DraftPatch{Revision: d.Revision, Request: reqJSON(stub, lyricsOf(""))})
	de = asErr(t, err, 422, "RECIPE_UNKNOWN")
	if detailField(t, de) != "id" {
		t.Errorf("update details %v", de.Details)
	}
	// A label-only edit of the same draft still works.
	if _, err := e.svc.Update(ctx, human, d.ID, drafts.DraftPatch{Revision: d.Revision, Label: str("x")}); err != nil {
		t.Errorf("label edit: %v", err)
	}
}

func TestDrafts_RejectUnknownParent(t *testing.T) {
	t.Parallel()
	e := newEnvM2c(t)
	ctx := context.Background()
	for _, ref := range []string{lt.TrackRefFlac, lt.TrackLossy} {
		_, err := e.svc.Create(ctx, human, drafts.NewDraft{Request: reqJSON("rej-p-"+ref[len(ref)-3:], lyricsOf("")), ParentTrackID: str(ref)})
		de := asErr(t, err, 422, "RECIPE_UNKNOWN")
		if detailField(t, de) != "parent_track_id" {
			t.Errorf("details %v", de.Details)
		}
	}
	d, err := e.svc.Create(ctx, human, drafts.NewDraft{Request: reqJSON("rej-p-ok", lyricsOf(""))})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Update(ctx, human, d.ID, drafts.DraftPatch{Revision: d.Revision, ParentTrackID: str(lt.TrackRefFlac)})
	if de := asErr(t, err, 422, "RECIPE_UNKNOWN"); detailField(t, de) != "parent_track_id" {
		t.Errorf("update details %v", de.Details)
	}
	// A known track is still a fine parent.
	if _, err := e.svc.Create(ctx, human, drafts.NewDraft{Request: reqJSON("rej-p-known", lyricsOf("")), ParentTrackID: str(lt.TrackLuna)}); err != nil {
		t.Errorf("known parent: %v", err)
	}
	if _, err := e.svc.Update(ctx, human, d.ID, drafts.DraftPatch{Revision: d.Revision, ParentTrackID: str(lt.TrackLuna)}); err != nil {
		t.Errorf("known parent update: %v", err)
	}
}

func TestDrafts_UploadRejectsUnknown(t *testing.T) {
	t.Parallel()
	body := func(stub string) string {
		return jsonl(line("up-r1"), line(stub), line("up-r3"))
	}

	e := newEnvM2c(t)
	ctx := context.Background()
	stub := stubIDs(t, e)[0]
	before := count(t, e, `SELECT count(*) FROM recipe_drafts`)
	_, err := e.svc.Upload(ctx, human, drafts.Upload{JSONL: body(stub)})
	de := asErr(t, err, 422, "LINT_FAILED")
	items, _ := de.Details["items"].([]drafts.FailedItem)
	if len(items) != 1 || items[0].Line != 2 || len(items[0].Errors) != 1 || items[0].Errors[0].Code != "recipe_unknown" || items[0].Errors[0].Field != "id" {
		t.Errorf("items %+v", de.Details["items"])
	}
	if count(t, e, `SELECT count(*) FROM recipe_drafts`) != before {
		t.Error("rows written despite the failure")
	}

	res, err := e.svc.Upload(ctx, human, drafts.Upload{JSONL: body(stub), AllowPartial: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 2 || len(res.Errors) != 1 || res.Errors[0] != (drafts.UploadIssue{Line: 2, Code: "recipe_unknown", Field: "id"}) {
		t.Errorf("partial result %+v", res)
	}
}

func TestLint_RecipeUnknown(t *testing.T) {
	t.Parallel()
	e := newEnvM2c(t)
	stub := stubIDs(t, e)[0]
	items := []yue2LintItem{{Request: line("lint-a")}, {Request: line(stub)}, {Request: line("lint-c")}}
	rep, err := e.svc.Lint(context.Background(), lintItems(items), true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK || len(rep.Items) != 3 {
		t.Fatalf("report %+v", rep)
	}
	for i, it := range rep.Items {
		has := false
		for _, is := range it.Errors {
			if is.Code == "recipe_unknown" {
				has = true
				if is.Field != "id" || is.Severity != "error" || is.Message == "" {
					t.Errorf("issue %+v", is)
				}
			}
		}
		if has != (i == 1) {
			t.Errorf("item %d: recipe_unknown=%v errors %+v", i, has, it.Errors)
		}
		if i != 1 && len(it.Errors) != 0 {
			t.Errorf("item %d should be clean: %+v", i, it.Errors)
		}
	}
	// batch duplicate logic unaffected: two identical lines still warn.
	rep, err = e.svc.Lint(context.Background(), lintItems([]yue2LintItem{{Request: line("lint-d")}, {Request: line("lint-d")}}), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.BatchErrors) == 0 {
		t.Errorf("batch duplicate id not reported: %+v", rep)
	}
}

type yue2LintItem struct{ Request string }

func lintItems(in []yue2LintItem) []yue2.LintItem {
	out := make([]yue2.LintItem, len(in))
	for i, it := range in {
		out[i] = yue2.LintItem{Request: json.RawMessage(it.Request)}
	}
	return out
}
