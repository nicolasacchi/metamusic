package api_test

import (
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

func taxRig(t *testing.T) *servertest.Rig {
	t.Helper()
	return soundsRig(t, servertest.Options{})
}

func skankProposal() map[string]any {
	return map[string]any{"rationale": "synthetic rationale", "mappings": []map[string]any{
		{"term": skankTerm, "facet": "genre_family", "value": "reggae", "op": "add"},
		{"term": "synthetic zouk term", "facet": "genre_family", "value": "world", "op": "add"},
	}}
}

// TestPropose_REST: an agent proposes (201 with the snapshot), an identical call is the same row (200, reused), an
// Idempotency-Key replay returns the stored answer, a human is refused, and list shows the row.
func TestPropose_REST(t *testing.T) {
	t.Parallel()
	r := taxRig(t)
	agent, agentID := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	human := r.Human(t)
	resp := agent.Do(t, "POST", "/api/taxonomy-proposals", skankProposal(), "Idempotency-Key", "tp-1")
	if resp.Status != 201 {
		t.Fatalf("propose: %d %s", resp.Status, resp.Body)
	}
	p := obj(t, resp.JSON(t))
	imp := obj(t, p["impact"])
	if p["state"] != "open" || p["reused"] != false || p["decided_at"] != nil || p["applied_version"] != nil || p["accepted"] != nil || obj(t, p["proposed_by"])["id"] != "agent:"+agentID ||
		len(p["mappings"].([]any)) != 2 || imp["songs_scanned"].(float64) < 1 || len(imp["gain"].([]any)) == 0 || imp["loss"] == nil || imp["unmapped_cleared"] == nil {
		t.Fatalf("proposal %v", p)
	}
	if again := agent.Do(t, "POST", "/api/taxonomy-proposals", skankProposal(), "Idempotency-Key", "tp-1"); again.Status != 201 || obj(t, again.JSON(t))["id"] != p["id"] {
		t.Errorf("idempotent replay: %d %s", again.Status, again.Body)
	}
	reused := agent.Do(t, "POST", "/api/taxonomy-proposals", skankProposal(), "Idempotency-Key", "tp-2")
	if reused.Status != 200 || obj(t, reused.JSON(t))["id"] != p["id"] || obj(t, reused.JSON(t))["reused"] != true {
		t.Errorf("reused: %d %s", reused.Status, reused.Body)
	}
	if n := mustCount(t, r, `SELECT COUNT(*) FROM taxonomy_proposals`); n != 1 {
		t.Errorf("%d rows", n)
	}
	if x := human.Do(t, "POST", "/api/taxonomy-proposals", skankProposal(), "Idempotency-Key", "tp-3"); x.Status != 403 || obj(t, x.JSON(t))["code"] != "HUMAN_ONLY" {
		t.Errorf("human propose: %d %s", x.Status, x.Body)
	}
	// ids-only errors: a refused proposal does not echo the rationale or the term
	bad := agent.Do(t, "POST", "/api/taxonomy-proposals", map[string]any{"rationale": "secret-rationale-canary", "mappings": []map[string]any{{"term": "secret-term-canary", "facet": "tempo_band", "value": "fast", "op": "add"}}}, "Idempotency-Key", "tp-4")
	if bad.Status != 422 || strings.Contains(string(bad.Body), "canary") {
		t.Errorf("bad proposal: %d %s", bad.Status, bad.Body)
	}
	list := obj(t, agent.OK(t, "GET", "/api/taxonomy-proposals?state=open&include_impact=true", nil))
	items := list["items"].([]any)
	if len(items) != 1 || list["total"].(float64) != 1 || obj(t, items[0])["id"] != p["id"] {
		t.Errorf("list %v", list)
	}
	if x := agent.Do(t, "GET", "/api/taxonomy-proposals?state=bogus", nil); x.Status != 422 {
		t.Errorf("bad state: %d", x.Status)
	}
}

// TestDecide_REST: agents are refused before any read (403, audit row 403, no state change), a device decides, accept
// applies one version and the Songs move at once.
func TestDecide_REST(t *testing.T) {
	t.Parallel()
	r := taxRig(t)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	human := r.Human(t)
	device := r.Device(t)
	p := obj(t, agent.Do(t, "POST", "/api/taxonomy-proposals", skankProposal(), "Idempotency-Key", "d-1").JSON(t))
	id := p["id"].(string)
	path := "/api/taxonomy-proposals/" + id + "/decision"

	versions := mustCount(t, r, `SELECT COUNT(*) FROM sound_taxonomy_versions`)
	for _, target := range []string{path, "/api/taxonomy-proposals/unknown-id/decision"} {
		if x := agent.Do(t, "POST", target, map[string]any{"decision": "accept"}); x.Status != 403 || obj(t, x.JSON(t))["code"] != "HUMAN_ONLY" {
			t.Errorf("agent decide %s: %d %s", target, x.Status, x.Body)
		}
	}
	if mustCount(t, r, `SELECT COUNT(*) FROM sound_taxonomy_versions`) != versions || mustCount(t, r, `SELECT COUNT(*) FROM taxonomy_proposals WHERE state = 'open'`) != 1 {
		t.Errorf("a refused decision changed state")
	}
	if st := auditStatus(t, r, "decide_taxonomy_proposal"); len(st) != 2 || st[0] != 403 || st[1] != 403 {
		t.Errorf("audit rows: %v", st)
	}
	if x := human.Do(t, "POST", "/api/taxonomy-proposals/unknown-id/decision", map[string]any{"decision": "accept"}); x.Status != 404 || obj(t, x.JSON(t))["code"] != "NOT_FOUND" {
		t.Errorf("unknown id: %d %s", x.Status, x.Body)
	}
	if x := human.Do(t, "POST", path, map[string]any{"decision": "accept", "accept_mappings": []int{5}}); x.Status != 422 {
		t.Errorf("bad index: %d %s", x.Status, x.Body)
	}

	resp := device.Do(t, "POST", path, map[string]any{"decision": "accept", "accept_mappings": []int{0}, "note": "just the first"})
	if resp.Status != 200 {
		t.Fatalf("device accept: %d %s", resp.Status, resp.Body)
	}
	res := obj(t, resp.JSON(t))
	prop, applied := obj(t, res["proposal"]), obj(t, res["applied"])
	if prop["state"] != "accepted" || prop["applied_version"].(float64) != 2 || applied["version"].(float64) != 2 || applied["mappings_applied"].(float64) != 1 ||
		obj(t, prop["decided_by"])["kind"] != "device" || prop["decided_note"] != "just the first" || res["skipped"] == nil || len(res["skipped"].([]any)) != 0 {
		t.Errorf("result %v", res)
	}
	if got := obj(t, agent.OK(t, "GET", "/api/sounds/search?limit=0&sound=genre_family:reggae", nil))["total"].(float64); got < 1 {
		t.Errorf("no Song under reggae after accept: %v", got)
	}
	if x := human.Do(t, "POST", path, map[string]any{"decision": "reject"}); x.Status != 409 || obj(t, x.JSON(t))["code"] != "TAXONOMY_PROPOSAL_CLOSED" {
		t.Errorf("closed: %d %s", x.Status, x.Body)
	}
	// the agent reads the outcome with the operator anonymised
	got := obj(t, agent.OK(t, "GET", "/api/taxonomy-proposals?state=accepted", nil))["items"].([]any)
	if len(got) != 1 || !strings.HasPrefix(obj(t, obj(t, got[0])["decided_by"])["id"].(string), "device:") || obj(t, obj(t, got[0])["decided_by"])["name"] != "device" {
		t.Errorf("accepted list %v", got)
	}
}

// TestDecide_REST_Stale: a mapping that became effective meanwhile makes the decision 409 TAXONOMY_STALE; the proposal is
// expired afterwards (the 409 does not roll the expiry back) and no version was written.
func TestDecide_REST_Stale(t *testing.T) {
	t.Parallel()
	r := taxRig(t)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	human := r.Human(t)
	p := obj(t, agent.Do(t, "POST", "/api/taxonomy-proposals", skankProposal(), "Idempotency-Key", "s-1").JSON(t))
	id := p["id"].(string)
	human.OK(t, "POST", "/api/sound-terms", map[string]any{"mappings": []map[string]any{{"term": skankTerm, "facet": "genre_family", "value": "reggae", "op": "add"}}})
	versions := mustCount(t, r, `SELECT COUNT(*) FROM sound_taxonomy_versions`)
	x := human.Do(t, "POST", "/api/taxonomy-proposals/"+id+"/decision", map[string]any{"decision": "accept", "accept_mappings": []int{0}})
	if x.Status != 409 || obj(t, x.JSON(t))["code"] != "TAXONOMY_STALE" || strings.Contains(string(x.Body), skankTerm) {
		t.Fatalf("stale: %d %s", x.Status, x.Body)
	}
	if mustCount(t, r, `SELECT COUNT(*) FROM taxonomy_proposals WHERE id = '`+id+`' AND state = 'expired'`) != 1 || mustCount(t, r, `SELECT COUNT(*) FROM sound_taxonomy_versions`) != versions {
		t.Errorf("expired row missing or a version was written")
	}
	// the second mapping is still effective-able but the proposal is closed now
	if y := human.Do(t, "POST", "/api/taxonomy-proposals/"+id+"/decision", map[string]any{"decision": "accept"}); y.Status != 409 || obj(t, y.JSON(t))["code"] != "TAXONOMY_PROPOSAL_CLOSED" {
		t.Errorf("after expiry: %d %s", y.Status, y.Body)
	}
}
