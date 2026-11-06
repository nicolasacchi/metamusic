package acceptance_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// agentFor opens an MCP session with a PAT of the given scopes over the T1 rig.
func (tr *t1Rig) agentFor(scopes ...auth.Scope) *l3Agent {
	tr.T.Helper()
	sess := tr.MCP(tr.T, scopes...)
	return newL3Agent(tr.T, tr.Rig, sess)
}

// TestAcceptance_T1_AgentFlow: an agent with a write-scope PAT drives a transform through the generic MCP face only: capabilities,
// the lyrics_of partner search, estimate, create, wait until done; get_audit lists the create and the terminal row in order. A
// read-only PAT is refused 403 on create, over REST and over MCP.
func TestAcceptance_T1_AgentFlow(t *testing.T) {
	tr := newT1Rig(t)
	a, b := tr.Info.Tx["A"], tr.Info.Tx["B"]
	agent := tr.agentFor(auth.ScopeRead, auth.ScopeWrite)
	for _, op := range []string{"get_transform_capabilities", "estimate_transform", "create_transform", "wait_for_transform", "get_transform", "list_transforms", "cancel_transform"} {
		if agent.mode[op] != "generic" {
			t.Errorf("%s has MCP mode %q, want generic (no dedicated tool)", op, agent.mode[op])
		}
	}
	caps := agent.call("get_transform_capabilities", nil)
	kinds := map[string]bool{}
	for _, k := range caps["kinds"].([]any) {
		kinds[obj(k)["kind"].(string)] = true
	}
	for _, k := range []string{"morph", "shepard", "glacier", "polytempo", "ensemble", "master", "widen"} {
		if !kinds[k] {
			t.Errorf("capabilities lack kind %s", k)
		}
	}
	found := false
	for _, it := range obj(agent.call("search_tracks", map[string]any{"lyrics_of": a, "limit": 100}))["items"].([]any) {
		found = found || obj(it)["id"] == b
	}
	if !found {
		t.Fatal("search_tracks lyrics_of did not offer the partner")
	}
	body := t1Bodies(tr)["morph"]
	est := agent.call("estimate_transform", body)
	if obj(est["cost"])["micro_usd"].(float64) != 0 || obj(est["quota"])["principal_kind"] != "agent" {
		t.Errorf("estimate = %v", est)
	}
	args := map[string]any{"idempotency_key": "agent-flow-1"}
	for k, v := range body {
		args[k] = v
	}
	created := agent.call("create_transform", args)
	id := created["id"].(string)
	if created["state"] != "queued" || created["take_id"] != id {
		t.Fatalf("create = %v", created)
	}
	tr.Drain()
	w := agent.call("wait_for_transform", map[string]any{"id": id, "since_state": "queued", "timeout_s": 5})
	if v := obj(w["transform"]); v["state"] != "done" || w["changed"] != true {
		t.Fatalf("wait = %v", w)
	}
	// the audit trail: create (the agent) then the terminal row (the runner), nothing paid
	audit := agent.call("get_audit", map[string]any{"limit": 20})
	var ops []string
	for _, it := range audit["items"].([]any) { // newest first
		e := obj(it)
		switch e["op"] {
		case "create_transform", "transform_done":
			for _, c := range e["created_ids"].([]any) {
				if c == id {
					ops = append([]string{e["op"].(string)}, ops...)
				}
			}
		}
	}
	if strings.Join(ops, ",") != "create_transform,transform_done" {
		t.Errorf("audit rows for the transform in order: %v", ops)
	}
	if n := tr.Count(`SELECT COUNT(*) FROM audit_log WHERE op IN ('create_transform', 'transform_done') AND cost_micro_usd <> 0`); n != 0 {
		t.Errorf("%d transform audit rows carry a cost", n)
	}
	// a read-only token is refused on create, over REST and over MCP
	ro, _ := tr.Agent(t, auth.ScopeRead)
	if resp := ro.Do(t, "POST", "/api/transforms", body, tr.idem()...); resp.Status != http.StatusForbidden {
		t.Errorf("read-only create over REST = %d", resp.Status)
	}
	roAgent := tr.agentFor(auth.ScopeRead)
	if res := rawTool(t, roAgent.sess, "create_transform", map[string]any{"idempotency_key": "ro-1", "kind": "master", "master": map[string]any{"source_id": a}}, true); !res.IsError {
		t.Errorf("read-only create over MCP succeeded: %+v", res.Content)
	}
	if n := tr.Count(`SELECT COUNT(*) FROM transforms`); n != 1 {
		t.Errorf("%d transform rows, want 1", n)
	}
	// an agent cancels only its own transform; a human any
	other := tr.CreateOK(masterBody(a, "bright")) // the human's
	if resp := tr.writer().Do(t, "POST", "/api/transforms/"+other["id"].(string)+"/cancel", nil); resp.Status != http.StatusForbidden && resp.Status != http.StatusNotFound {
		t.Errorf("an agent cancelling the human's transform = %d", resp.Status)
	}
	tr.H.OK(t, "POST", "/api/transforms/"+other["id"].(string)+"/cancel", nil)
}
