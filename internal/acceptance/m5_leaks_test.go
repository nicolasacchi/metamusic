package acceptance_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestOps_M5_LeaksNothing: the text of the loop (comment bodies, the lyric lines of a patch, the rationales, a verdict note) is
// stored where it belongs and nowhere else: not in the audit rows (and get_audit over REST and MCP), the job events and
// transitions, the ledger, the ntfy messages, the captured logs, the next actions, and not in any problem body, including those of
// refusals that name the very text they refuse (C264, C255.6, D-M5-3).
func TestOps_M5_LeaksNothing(t *testing.T) {
	logs := captureLogs(t)
	l := newM5Loop(t, servertest.Options{Canary: true})
	h := l.h
	tag := randomTag(t)
	keepBody, fixBody, hiddenBody := "keepbody"+tag, "fixbody"+tag, "hiddenbody"+tag
	newLine, rationale, createWhy, note, refusedLine := "newline"+tag, "patchwhy"+tag, "renderwhy"+tag, "verdictnote"+tag, "refusedline"+tag
	lk := newLeaks(t)
	for kind, v := range map[string]string{"keep_body": keepBody, "fix_body": fixBody, "hidden_body": hiddenBody, "new_line": newLine, "patch_rationale": rationale,
		"render_rationale": createWhy, "verdict_note": note, "refused_line": refusedLine} {
		lk.add(kind, v)
	}

	keep := l.humanComment(t, l.mother, "keep", keepBody, 0.5)
	fix := l.humanComment(t, l.mother, "fix", fixBody, 1.5)
	hidden := l.humanComment(t, l.mother, "fix", hiddenBody, 2.5)
	h.OK(t, "PATCH", "/api/comments/"+hidden, map[string]any{"hidden": true})
	_ = keep

	// the refusals that name the text: a hidden comment, a line outside the scope, a tag in a line, an untimed comment without a scope
	var problems []string
	refuse := func(label string, args map[string]any) {
		t.Helper()
		res := rawTool(t, l.agent, "propose_patch", args, true)
		if !res.IsError {
			t.Fatalf("%s: was accepted", label)
		}
		problems = append(problems, label+": "+errText(res))
	}
	refuse("hidden comment", map[string]any{"base_track_id": l.mother, "addresses_comment_ids": []string{hidden}, "rationale": rationale, "extra_sections": []int{1},
		"ops": []map[string]any{{"op": "replace_line", "section_idx": 1, "line_idx": 0, "text": refusedLine}}})
	refuse("tag in line", map[string]any{"base_track_id": l.mother, "addresses_comment_ids": []string{fix}, "rationale": rationale, "extra_sections": []int{1},
		"ops": []map[string]any{{"op": "replace_line", "section_idx": 1, "line_idx": 0, "text": "[Chorus] " + refusedLine}}})
	refuse("out of scope", map[string]any{"base_track_id": l.mother, "addresses_comment_ids": []string{fix}, "rationale": rationale, "extra_sections": []int{1},
		"ops": []map[string]any{{"op": "replace_line", "section_idx": 0, "line_idx": 0, "text": refusedLine}}})

	patch := obj(callTool(t, l.agent, "propose_patch", map[string]any{"base_track_id": l.mother, "addresses_comment_ids": []string{fix}, "rationale": rationale,
		"extra_sections": []int{1}, "ops": []map[string]any{{"op": "replace_line", "section_idx": 1, "line_idx": 0, "text": newLine}}}, true))
	est := obj(decodeStructured(t, l.estimateAgent(t, patch["id"].(string), 1)))
	cres := rawTool(t, l.agent, "create_generation", map[string]any{"confirmation_token": est["confirmation_token"], "rationale": createWhy}, false)
	if cres.IsError {
		t.Fatalf("create: %s", errText(cres))
	}
	children := l.finish(t, obj(decodeStructured(t, cres)))
	cbs := l.callbacksOf(t, children[0])
	fixCb, keepCb := callbackOf(t, cbs, "fix"), callbackOf(t, cbs, "keep")
	// the critic and the operator answer with notes; the keep is lost (a regression comment copies the keep body, which is the comment's own)
	for _, c := range []*servertest.Client{nil2agent(t, l), h} {
		if r := l.verdict(t, c, fixCb["id"].(string), "not_yet", map[string]any{"note": note}); r.Status != 201 {
			t.Fatalf("verdict: %d %s", r.Status, r.Body)
		}
	}
	if r := l.verdict(t, h, keepCb["id"].(string), "lost", map[string]any{"note": note}); r.Status != 201 {
		t.Fatalf("lost: %d %s", r.Status, r.Body)
	}
	// refusals of the callback surface
	dup := l.verdict(t, h, fixCb["id"].(string), "worse", map[string]any{"note": note + "x"})
	wrong := l.verdict(t, h, keepCb["id"].(string), "fixed", map[string]any{"note": note})
	if dup.Status != 409 || wrong.Status != 422 {
		t.Fatalf("callback refusals: %d %d", dup.Status, wrong.Status)
	}
	problems = append(problems, string(dup.Body), string(wrong.Body))

	// stored text: the places that must not hold it
	for name, text := range dumpTables(t, l.Store.R, "audit_log", "job_events", "job_transitions", "ledger", "approvals") {
		lk.scan("table "+name, text)
	}
	lk.scan("captured logs", logs.String())
	for i, nr := range l.Ntfy.Requests() {
		lk.scan(fmt.Sprintf("ntfy request %d body", i), nr.Body)
		lk.scan(fmt.Sprintf("ntfy request %d path", i), nr.Path)
	}
	for i, p := range problems {
		lk.scan(fmt.Sprintf("problem body %d", i), p)
	}
	audit := h.Do(t, "GET", "/api/audit?limit=200", nil)
	lk.scan("get_audit over REST", string(audit.Body))
	lk.scan("get_audit over MCP", fmt.Sprint(callTool(t, l.agent, "get_audit", map[string]any{"limit": 200}, true)))
	lk.scan("next actions over REST", string(h.Do(t, "GET", "/api/next-actions?limit=50", nil).Body))
	lk.scan("next actions over MCP", fmt.Sprint(callTool(t, l.agent, "list_next_actions", map[string]any{"limit": 50}, true)))
	lk.flush()
	if lk.scanned < 15 {
		t.Errorf("scanned only %d texts", lk.scanned)
	}

	// the text did enter the places it belongs to: the scan is not vacuous
	for q, want := range map[string]string{
		`SELECT body FROM comments WHERE body = ?`:                                   fixBody,
		`SELECT rationale FROM patches WHERE rationale = ?`:                          rationale,
		`SELECT rationale FROM batches WHERE rationale = ?`:                          createWhy,
		`SELECT note FROM callback_verdicts WHERE note = ?`:                          note,
		`SELECT body FROM comments WHERE source = 'callback' AND body LIKE '%' || ?`: keepBody,
	} {
		var got string
		if err := l.Store.R.QueryRow(q, want).Scan(&got); err != nil {
			t.Errorf("canary %q is not stored where it belongs: %v", want, err)
		}
	}
	if n := strings.Count(string(audit.Body), `"op":"propose_patch"`); n < 1 {
		t.Error("the audit lists no propose_patch: the audit scan is vacuous")
	}
	if len(l.Ntfy.Requests()) == 0 {
		t.Error("no ntfy message was sent: the ntfy scan is vacuous")
	}
}
