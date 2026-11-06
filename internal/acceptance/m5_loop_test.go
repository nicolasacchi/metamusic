package acceptance_test

import (
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestAcceptance_M5_SongPRLoop is the whole close-the-loop story through the real REST and MCP surface, the fake backend and the real
// ingest (roadmap M5): a comment becomes a Song PR, the Song PR renders within the agent's auto-approve, the take hook writes the
// callbacks and moves the comment to addressed, the operator's verdicts move the comment states (fixed resolves, not_yet reopens,
// worse regresses), a lost keep writes a regression comment on the child, and the critic's verdict never moves anything.
func TestAcceptance_M5_SongPRLoop(t *testing.T) {
	l := newM5Loop(t, servertest.Options{Canary: true})
	h := l.h

	// 1. a keep comment by the operator, a fix comment by the agent (the hook arrives late)
	keep := l.humanComment(t, l.mother, "keep", "the verse melody is perfect", 0.5)
	fix := l.agentComment(t, l.mother, "fix", "the hook arrives too late", 1.5)
	other := l.agentComment(t, l.mother, "fix", "the outro drags", 4.5) // not addressed by the patch: it stays open for the descendants
	ctx := obj(callTool(t, l.agent, "get_track_context", map[string]any{"id": l.mother}, false))
	if oc := obj(ctx["judgments"])["open_fix"]; oc != 2.0 {
		t.Fatalf("context of the mother: open_fix = %v", oc)
	}

	// 2. propose_patch (MCP generic): the chorus line replaced, bounds and diff on the wire
	patch := l.proposeChorus(t, []string{fix}, "the hook arrives right on time")
	pid := patch["id"].(string)
	if patch["operator"] != "lyric_edit" || len(arr(obj(patch["diff"])["lyrics"])) != 1 || obj(patch["base"])["track_id"] != l.mother ||
		!strings.Contains(obj(patch["compiled"])["lyrics"].(string), "the hook arrives right on time") {
		t.Fatalf("patch = %v", patch)
	}
	if b := obj(obj(patch["bounds"])["bounds"]); b["lines_changed"] != 1.0 || b["sections_scoped"] != 1.0 {
		t.Errorf("bounds = %v", b)
	}
	if got := obj(callTool(t, l.agent, "get_patch", map[string]any{"id": pid}, true)); got["id"] != pid {
		t.Errorf("get_patch = %v", got)
	}
	if page := obj(callTool(t, l.agent, "list_patches", map[string]any{"track_id": l.mother}, true)); page["total"] != 1.0 {
		t.Errorf("list_patches = %v", page)
	}
	// nothing spent, no child, no callback yet; the comment is still open
	if n := count(t, l.Rig, `SELECT COUNT(*) FROM callbacks`); n != 0 {
		t.Fatalf("%d callbacks before any render", n)
	}
	if s, _ := l.commentState(t, fix); s != "open" {
		t.Fatalf("fix comment %s before the render", s)
	}

	// 3. the agent renders the Song PR within auto-approve (n=2: the base seed plus one new seed); the real ingest lands the children
	batch, children := l.renderAgentOK(t, pid, 2)
	if len(children) != 2 || children[0] == children[1] {
		t.Fatalf("children = %v", children)
	}
	for _, c := range children {
		var parent, op string
		if err := l.Store.R.QueryRow(`SELECT t.parent_track_id, m.operator FROM tracks t JOIN mutations m ON m.child_track_id = t.id WHERE t.id = ?`, c).Scan(&parent, &op); err != nil {
			t.Fatal(err)
		}
		if parent != l.mother || op != "lyric_edit" {
			t.Errorf("child %s: parent %s operator %s", c, parent, op)
		}
		if lyr := l.lyricsOf(t, c); !strings.Contains(lyr, "the hook arrives right on time") || strings.Contains(lyr, "mosa neko") {
			t.Errorf("child lyrics = %q", lyr)
		}
	}
	// the patch lists its renders and the callbacks the hook wrote: per child one fix (the addressed comment) and one keep
	got := obj(h.OK(t, "GET", "/api/patches/"+pid, nil))
	if len(arr(got["renders"])) != 2 || obj(got["callbacks"])["total"] != 4.0 || obj(got["callbacks"])["answered"] != 0.0 {
		t.Fatalf("patch after the render = %v", got)
	}
	if s, by := l.commentState(t, fix); s != "addressed" || by != "system:job" {
		t.Errorf("fix comment: %s by %s, want addressed by system:job", s, by)
	}
	if s, _ := l.commentState(t, keep); s != "open" {
		t.Errorf("a keep comment moved to %s", s)
	}
	cb1, cb2 := l.callbacksOf(t, children[0]), l.callbacksOf(t, children[1])
	if len(cb1) != 2 || len(cb2) != 2 {
		t.Fatalf("callbacks per child: %d and %d, want 2 each", len(cb1), len(cb2))
	}
	fix1, keep1, fix2, keep2 := callbackOf(t, cb1, "fix"), callbackOf(t, cb1, "keep"), callbackOf(t, cb2, "fix"), callbackOf(t, cb2, "keep")
	if fix1["state"] != "pending" || obj(fix1["comment"])["id"] != fix || obj(keep1["comment"])["id"] != keep || obj(fix1["spot"])["basis"] == nil {
		t.Errorf("fix callback = %v", fix1)
	}
	// the agent sees the same callbacks over MCP
	if page := obj(callTool(t, l.agent, "list_callbacks", map[string]any{"state": []string{"pending"}}, true)); page["total"] != 4.0 {
		t.Errorf("agent list_callbacks = %v", page)
	}
	// one count-only ntfy for the batch, none carrying text of the loop
	var cbNotes []string
	for _, nr := range l.Ntfy.Requests() {
		if strings.Contains(nr.Body, "callbacks") {
			cbNotes = append(cbNotes, nr.Body)
		}
	}
	if len(cbNotes) != 1 || !strings.Contains(cbNotes[0], "4") {
		t.Errorf("callbacks-ready notifications = %q, want one naming the count", cbNotes)
	}
	_ = batch

	// 4. /context of a child carries the ancestor's still-open fix comment with a spot mapped onto the child (the addressed one is not
	// open any more), and the next Song PR can be proposed against the child for that ancestor comment
	cc := obj(obj(callTool(t, l.agent, "get_track_context", map[string]any{"id": children[0]}, false))["judgments"])
	var anc map[string]any
	for _, c := range arr(cc["comments"]) {
		if obj(c)["id"] == other {
			anc = obj(c)
		}
		if obj(c)["id"] == fix {
			t.Errorf("an addressed comment is listed as open work on the child: %v", c)
		}
	}
	if anc == nil || anc["relation"] != "ancestor" || obj(anc["spot"])["basis"] != "proportional" || cc["ancestor_open_fix"] != 1.0 {
		t.Errorf("the child's context lacks the ancestor fix with a spot: %v", cc)
	}
	next := obj(callTool(t, l.agent, "propose_patch", map[string]any{"base_track_id": children[0], "addresses_comment_ids": []string{other}, "rationale": "the outro drags",
		"extra_sections": []int{1}, "ops": []map[string]any{{"op": "replace_line", "section_idx": 1, "line_idx": 0, "text": "the outro moves along"}}}, true))
	if obj(next["base"])["track_id"] != children[0] || len(arr(next["addresses"])) != 1 {
		t.Errorf("a second-generation patch for the ancestor comment = %v", next)
	}

	// the next actions point at the work: the operator at the callbacks that wait on each child, the agent at the patch not yet rendered
	nextFor := func(page any, code string) (subjects []string) {
		for _, it := range arr(obj(page)["items"]) {
			if obj(it)["code"] == code {
				subjects = append(subjects, obj(obj(it)["subject"])["id"].(string))
			}
		}
		return subjects
	}
	if got := nextFor(h.OK(t, "GET", "/api/next-actions?limit=50", nil), "callback_waiting"); !sameSet(got, children) {
		t.Errorf("callback_waiting subjects = %v, want the children %v", got, children)
	}
	if got := nextFor(callTool(t, l.agent, "list_next_actions", map[string]any{"limit": 50}, true), "patch_ready_to_render"); !sameSet(got, children[:1]) {
		t.Errorf("patch_ready_to_render subjects = %v, want %v", got, children[:1])
	}

	// 5. the critic (the agent) answers one fix callback: it never moves the comment, and the operator does not see it before answering
	crit := l.verdict(t, nil2agent(t, l), fix1["id"].(string), "fixed", nil)
	if crit.Status != 201 {
		t.Fatalf("critic verdict: %d %s", crit.Status, crit.Body)
	}
	if s, _ := l.commentState(t, fix); s != "addressed" {
		t.Errorf("a critic verdict moved the comment to %s", s)
	}
	if v := obj(h.OK(t, "GET", "/api/callbacks/"+fix1["id"].(string), nil)); len(arr(v["critic_verdicts"])) != 0 || v["state"] != "pending" {
		t.Errorf("the operator sees the critic before answering: %v", v)
	}

	// 6. the operator answers: child 1 not_yet (the comment reopens), child 2 fixed (resolved, whatever the order)
	if r := l.verdict(t, h, fix1["id"].(string), "not_yet", map[string]any{"heard_at_s": 1.2, "note": "still a beat late"}); r.Status != 201 {
		t.Fatalf("not_yet: %d %s", r.Status, r.Body)
	}
	if s, _ := l.commentState(t, fix); s != "open" {
		t.Errorf("after not_yet the comment is %s, want open", s)
	}
	r2 := l.verdict(t, h, fix2["id"].(string), "fixed", nil)
	if r2.Status != 201 || obj(r2.JSON(t))["comment_state"] != "resolved" {
		t.Fatalf("fixed: %d %s", r2.Status, r2.Body)
	}
	if s, by := l.commentState(t, fix); s != "resolved" || !strings.HasPrefix(by, "human:") {
		t.Errorf("after fixed the comment is %s by %s", s, by)
	}
	if v := obj(h.OK(t, "GET", "/api/callbacks/"+fix1["id"].(string), nil)); len(arr(v["critic_verdicts"])) != 1 || v["state"] != "answered" {
		t.Errorf("after answering the operator should see the critic: %v", v)
	}

	// 7. a lost keep writes a regression comment on the child; a kept one writes nothing
	lost := l.verdict(t, h, keep1["id"].(string), "lost", map[string]any{"heard_at_s": 0.4})
	if lost.Status != 201 {
		t.Fatalf("lost: %d %s", lost.Status, lost.Body)
	}
	reg, _ := obj(lost.JSON(t))["regression_comment_id"].(string)
	if reg == "" {
		t.Fatalf("a lost keep wrote no regression comment: %s", lost.Body)
	}
	var kind, state, src, body, origin, track string
	if err := l.Store.R.QueryRow(`SELECT kind, state, source, body, origin_callback_id, track_id FROM comments WHERE id = ?`, reg).Scan(&kind, &state, &src, &body, &origin, &track); err != nil {
		t.Fatal(err)
	}
	if kind != "fix" || state != "open" || src != "callback" || body != "Lost from parent: the verse melody is perfect" || origin != keep1["id"] || track != children[0] {
		t.Errorf("regression comment = %s %s %s %q origin %s on %s", kind, state, src, body, origin, track)
	}
	if s, _ := l.commentState(t, keep); s != "open" {
		t.Errorf("the keep comment moved to %s", s)
	}
	if r := l.verdict(t, h, keep2["id"].(string), "kept", nil); r.Status != 201 || obj(r.JSON(t))["regression_comment_id"] != nil {
		t.Errorf("kept: %d %s", r.Status, r.Body)
	}
	if n := count(t, l.Rig, `SELECT COUNT(*) FROM comments WHERE source = 'callback'`); n != 1 {
		t.Errorf("%d regression comments, want 1", n)
	}
	// the regression is visible to the agent on the child's comments and in its next actions
	found := false
	for _, c := range arr(obj(callTool(t, l.agent, "list_comments", map[string]any{"id": children[0]}, true))["items"]) {
		found = found || obj(c)["id"] == reg
	}
	if !found {
		t.Error("the agent cannot see the regression comment on the child")
	}

	// 8. the patch tallies the verdicts
	end := obj(obj(h.OK(t, "GET", "/api/patches/"+pid, nil))["callbacks"])
	if end["total"] != 4.0 || end["answered"] != 4.0 || end["not_yet"] != 1.0 || end["fixed"] != 1.0 || end["kept"] != 1.0 || end["lost"] != 1.0 {
		t.Errorf("patch callbacks = %v", end)
	}
}
