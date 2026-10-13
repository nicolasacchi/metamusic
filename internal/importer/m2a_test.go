package importer

import (
	"strings"
	"testing"
)

// edit simulates the editor (M2a-B5): a new revision with another request.
func (e *env) editDraft(id string) {
	e.t.Helper()
	if _, err := e.st.W.Exec(`UPDATE recipe_drafts SET request_json = json_set(request_json, '$.lyrics', '[Verse 1]\nedited line'), revision = 2, updated_by = 'human:test' WHERE id = ?`, id); err != nil {
		e.t.Fatal(err)
	}
}

func warnCount(is []Issue, code string) int {
	n := 0
	for _, i := range is {
		if i.Code == code {
			n++
		}
	}
	return n
}

// An edited draft is never fulfilled by an import (M1 C11/C17 amended by M2a C27).
func TestApply_EditedDraftNotFulfilled(t *testing.T) {
	e := newEnv(t)
	e.put("r.jsonl", lines(req("ed-1", 1), req("ed-2", 2)))
	e.applyAll()
	d := e.drafts()
	if d["ed-1#1"].Status != "backlog" || d["ed-2#2"].Status != "backlog" {
		t.Fatalf("setup drafts: %+v", d)
	}
	editedBefore := e.scalar(`SELECT request_json FROM recipe_drafts WHERE id=?`, d["ed-1#1"].ID)
	e.editDraft(d["ed-1#1"].ID)
	editedJSON := e.scalar(`SELECT request_json FROM recipe_drafts WHERE id=?`, d["ed-1#1"].ID)
	if editedJSON == editedBefore {
		t.Fatal("setup: the edit changed nothing")
	}
	e.flac("ed_1.flac", "ed1")
	e.flac("ed_2.flac", "ed2")
	_, res := e.applyAll()
	if res.State != "completed" || res.TracksImported != 2 {
		t.Fatalf("run = %+v", res)
	}
	after := e.drafts()
	if after["ed-1#1"].Status != "backlog" || after["ed-1#1"].FulfilledBy != "" {
		t.Errorf("edited draft = %+v", after["ed-1#1"])
	}
	if after["ed-2#2"].Status != "fulfilled" {
		t.Errorf("unedited draft = %+v", after["ed-2#2"])
	}
	if got := e.scalar(`SELECT request_json FROM recipe_drafts WHERE id=?`, d["ed-1#1"].ID); got != editedJSON {
		t.Error("the edited request changed")
	}
	if n := warnCount(res.Report.Warnings, "draft_edited_not_fulfilled"); n != 1 {
		t.Errorf("draft_edited_not_fulfilled x%d in %v", n, issueCodes(res.Report.Warnings))
	}
	// later runs keep the edited draft and stay quiet
	_, res2 := e.applyAll()
	if res2.TracksImported != 0 || e.drafts()["ed-1#1"].Status != "backlog" {
		t.Errorf("second run = %+v", res2)
	}
}

// A mother never relinks a varianti draft a human edited (and cleared the mother of).
func TestRelink_SkipsEditedDrafts(t *testing.T) {
	e := newEnv(t)
	e.put("varianti/rl_001_v2.json", req("rl_001_v2", 2))
	e.put("varianti/rl_001_v3.json", req("rl_001_v3", 3))
	e.applyAll()
	d := e.drafts()
	if d["rl_001_v2#2"].Parent != "" || d["rl_001_v3#3"].Parent != "" {
		t.Fatalf("setup: mothers %+v", d)
	}
	e.editDraft(d["rl_001_v2#2"].ID)
	// the mother arrives
	e.put("r.jsonl", lines(req("rl_001", 1)))
	e.flac("rl_001.flac", "rl1")
	inv, res := e.applyAll()
	after := e.drafts()
	if after["rl_001_v2#2"].Parent != "" {
		t.Errorf("edited draft was relinked: %q", after["rl_001_v2#2"].Parent)
	}
	if after["rl_001_v3#3"].Parent != e.tracks()["rl_001#1"].ID {
		t.Errorf("unedited draft not relinked: %q", after["rl_001_v3#3"].Parent)
	}
	if n := warnCount(res.Report.Warnings, "draft_edited_not_relinked"); n != 1 {
		t.Errorf("draft_edited_not_relinked x%d in %v", n, issueCodes(res.Report.Warnings))
	}
	// the dry-run plan does not promise a mother for the edited draft
	for _, dr := range inv.Report.Drafts {
		if dr.RequestID == "rl_001_v2" && dr.Mother != nil {
			t.Errorf("plan names a mother for the edited draft: %v", *dr.Mother)
		}
	}
	if !strings.Contains(strings.Join(issueCodes(res.Report.Warnings), ","), "draft_edited_not_relinked") {
		t.Error("warning missing")
	}
}
