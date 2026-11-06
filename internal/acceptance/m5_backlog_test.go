package acceptance_test

import (
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// problemOf decodes the problem body of an MCP tool error.
func problemOf(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	if !res.IsError {
		t.Fatalf("expected a tool error, got %v", decodeStructured(t, res))
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(errText(res)), &p); err != nil {
		t.Fatalf("not a problem body: %q", errText(res))
	}
	return p
}

// otherRootMother returns a lossless known-recipe take of a different lineage root than root.
func otherRootMother(t *testing.T, l *m5Loop, root string) string {
	t.Helper()
	var id string
	if err := l.Store.R.QueryRow(`SELECT t.id FROM tracks t JOIN recipes rc ON rc.id = t.recipe_id
		WHERE rc.engine <> 'unknown' AND t.lossy = 0 AND t.root_track_id <> ? ORDER BY t.id LIMIT 1`, root).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestAcceptance_M5_BacklogPacesAgent: an agent cannot outrun the operator's ears (D-M5-R3-3). Once the unanswered callbacks of a
// lineage root reach MM_CALLBACK_BACKLOG_MAX (6), an agent's estimate and create of a Song PR or of any variant in that root are
// refused 409 CALLBACKS_BACKLOG, with no batch, no reservation and no backend call; a take of another root is untouched; the operator
// is exempt; a critic verdict does not drain the backlog, a human answer does; and the agent's next render refills it.
func TestAcceptance_M5_BacklogPacesAgent(t *testing.T) {
	big := spend.Caps{MonthlyMicro: 90_000_000, BatchMicro: 50_000_000, HumanDailyMicro: 50_000_000, AgentDailyMicro: 50_000_000, AutoApproveTakes: 100, AutoApproveMicro: 50_000_000}
	l := newM5Loop(t, servertest.Options{Caps: &big})
	h := l.h
	var root string
	if err := l.Store.R.QueryRow(`SELECT root_track_id FROM tracks WHERE id = ?`, l.mother).Scan(&root); err != nil {
		t.Fatal(err)
	}
	other := otherRootMother(t, l, root)
	keep := l.humanComment(t, l.mother, "keep", "the verse is perfect", 0.5)
	fix := l.agentComment(t, l.mother, "fix", "the hook arrives too late", 1.5)
	_ = keep
	pid := l.proposeChorus(t, []string{fix}, "the hook arrives right on time")["id"].(string)

	// the agent estimates while the backlog is empty (a token in its pocket), then the operator renders three takes: 3 fix + 3 keep
	// (a style variant of the mother: no Song PR in the item, only a parent in the root; a Song PR token would collide with the operator's
	// render on the base seed and go stale for another reason)
	styleEst := rawTool(t, l.agent, "estimate_variant", map[string]any{"id": l.mother, "mode": "style_edit", "patch": map[string]any{"style": motherStyle(t, l.Rig, l.mother) + ", slow"}}, false)
	if styleEst.IsError {
		t.Fatalf("estimate before the backlog: %s", errText(styleEst))
	}
	staleEst := obj(decodeStructured(t, styleEst))
	_, children := l.renderHuman(t, pid, 3)
	if n := count(t, l.Rig, `SELECT COUNT(*) FROM callbacks`); n != 6 {
		t.Fatalf("%d callbacks after three children, want 6", n)
	}
	batches, calls := count(t, l.Rig, `SELECT COUNT(*) FROM batches`), l.Fake.Counters().Total()

	refused := func(what string, res *mcp.CallToolResult) {
		t.Helper()
		p := problemOf(t, res)
		d := obj(p["details"])
		if p["code"] != "CALLBACKS_BACKLOG" || d["root_track_id"] != root || d["pending"] != 6.0 || d["limit"] != 6.0 {
			t.Errorf("%s: %v", what, p)
		}
	}
	// the stale token cannot create (the gate is re-run in the create transaction), a fresh Song PR and any variant in the root cannot be estimated
	refused("create_variant with a token from before the backlog", rawTool(t, l.agent, "create_variant", map[string]any{"id": l.mother, "confirmation_token": staleEst["confirmation_token"], "rationale": "late"}, false))
	refused("song_pr estimate", l.estimateAgent(t, pid, 1))
	refused("variant of the mother", rawTool(t, l.agent, "estimate_variant", map[string]any{"id": l.mother, "mode": "new_seed", "n_seeds": 1}, false))
	refused("variant of a child", rawTool(t, l.agent, "estimate_variant", map[string]any{"id": children[0], "mode": "new_seed", "n_seeds": 1}, false))
	refused("generation mixing a free root and a full one", rawTool(t, l.agent, "estimate_generation", map[string]any{"items": []any{
		map[string]any{"mode": "new_seed", "parent_track_id": other, "n_seeds": 1}, map[string]any{"mode": "new_seed", "parent_track_id": l.mother, "n_seeds": 1}}}, false))
	if got, c := count(t, l.Rig, `SELECT COUNT(*) FROM batches`), l.Fake.Counters().Total(); got != batches || c != calls {
		t.Errorf("refusals left %d new batches and %d backend calls", got-batches, c-calls)
	}

	// no collateral: another root is estimated and created by the same agent
	otherEst := rawTool(t, l.agent, "estimate_variant", map[string]any{"id": other, "mode": "new_seed", "n_seeds": 1}, false)
	if otherEst.IsError {
		t.Errorf("a take of another root is gated: %s", errText(otherEst))
	}
	// the operator is exempt on the same root
	if resp := h.Do(t, "POST", "/api/generations/estimate", map[string]any{"items": []any{map[string]any{"mode": "song_pr", "patch_id": pid, "n_seeds": 1}}}); resp.Status != 200 {
		t.Errorf("the operator is paced too: %d %s", resp.Status, resp.Body)
	}

	// a critic verdict does not drain the backlog; one human answer does, and then the agent proceeds
	cbs := l.callbacksOf(t, children[0])
	critic := nil2agent(t, l)
	if r := l.verdict(t, critic, callbackOf(t, cbs, "fix")["id"].(string), "fixed", nil); r.Status != 201 {
		t.Fatalf("critic verdict: %d %s", r.Status, r.Body)
	}
	refused("estimate after a critic verdict", l.estimateAgent(t, pid, 1))
	if r := l.verdict(t, h, callbackOf(t, cbs, "keep")["id"].(string), "kept", nil); r.Status != 201 {
		t.Fatalf("human verdict: %d %s", r.Status, r.Body)
	}
	// fix comment unaffected by a keep answer: the patch is still renderable (addressed). 5 pending: the agent may estimate and create one more
	res := l.estimateAgent(t, pid, 1)
	if res.IsError {
		t.Fatalf("estimate at 5 pending: %s", errText(res))
	}
	cres := l.createAgent(t, obj(decodeStructured(t, res)))
	if cres.IsError {
		t.Fatalf("create at 5 pending: %s", errText(cres))
	}
	l.finish(t, obj(decodeStructured(t, cres)))
	// the new child brought its own callbacks: 7 pending, the agent is paced again until the operator listens
	if n := count(t, l.Rig, `SELECT COUNT(*) FROM callbacks c WHERE NOT EXISTS (SELECT 1 FROM callback_verdicts v WHERE v.callback_id = c.id AND v.source = 'human')`); n < 6 {
		t.Fatalf("%d pending after the agent's render", n)
	}
	var pending float64
	p := problemOf(t, l.estimateAgent(t, pid, 1))
	if pending, _ = obj(p["details"])["pending"].(float64); p["code"] != "CALLBACKS_BACKLOG" && p["code"] != "PATCH_RENDER_LIMIT" || pending > 0 && pending < 6 {
		t.Errorf("after the agent's render: %v", p)
	}
}
