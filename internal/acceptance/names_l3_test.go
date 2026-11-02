package acceptance_test

import (
	"encoding/json"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestAgentsNeverSeeHumanNames_L3: the operator's username is a canary; the operator decides a taxonomy proposal (decided_by),
// sequences and publishes a Record (the actor of the changelog and of the stored sequence run), and no L3 response an agent
// receives over REST, MCP or stdio contains the name or a human row with an id other than "human".
func TestAgentsNeverSeeHumanNames_L3(t *testing.T) {
	r := l3Rig(t, servertest.Options{Canary: true})
	h := r.Human(t)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	songs := []string{l3Song(t, r, "T1"), l3Song(t, r, "T2"), l3Song(t, r, "T3")}

	rec := obj(obj(h.OK(t, "POST", "/api/records", map[string]any{"title": "Names Record", "song_ids": songs}, "Idempotency-Key", "nl3-rec"))["record"])
	rid := rec["id"].(string)
	seq := obj(h.OK(t, "POST", "/api/records/"+rid+"/slots", map[string]any{"revision": rec["revision"], "ops": []map[string]any{{"op": "sequence", "strategy": "arc"}}}))
	h.OK(t, "POST", "/human/api/records/"+rid+"/publish", map[string]any{"revision": obj(seq["record"])["revision"], "published": true})
	tp := obj(agent.OK(t, "POST", "/api/taxonomy-proposals", map[string]any{"rationale": "synthetic rationale",
		"mappings": []map[string]any{{"term": "skank", "facet": "mood", "value": "chill", "op": "add"}}}, "Idempotency-Key", "nl3-tax"))
	h.OK(t, "POST", "/api/taxonomy-proposals/"+tp["id"].(string)+"/decision", map[string]any{"decision": "accept", "note": "ok"})

	reads := []struct {
		op   string
		args map[string]any
		path string
	}{
		{"list_taxonomy_proposals", map[string]any{"state": []string{"accepted"}}, "/api/taxonomy-proposals?state=accepted"},
		{"get_record", map[string]any{"id": rid, "include": []string{"sequence", "changes"}}, "/api/records/" + rid + "?include=sequence&include=changes"},
		{"get_record_changes", map[string]any{"id": rid}, "/api/records/" + rid + "/changes"},
		{"suggest_sequence", map[string]any{"record_id": rid}, "/api/sequence?record_id=" + rid},
		{"list_next_actions", map[string]any{"limit": 50}, "/api/next-actions?limit=50"},
		{"get_song_atlas", map[string]any{"x": "tempo", "y": "energy"}, "/api/atlas?x=tempo&y=energy"},
		{"get_adherence", map[string]any{}, "/api/atlas/adherence"},
		{"get_audit", map[string]any{"limit": 200}, "/api/audit?limit=200"},
	}
	check := func(face, op string, body []byte) {
		t.Helper()
		if hit := containsAny(string(body), canaryHuman, canaryDevice); hit != "" {
			t.Errorf("%s %s: response contains the %s canary", face, op, canaryKind(hit))
		}
		var v any
		if json.Unmarshal(body, &v) == nil {
			walkHumans(v, func(string) { t.Errorf("%s %s: a human row carries an id other than \"human\"", face, op) })
		}
	}
	// positive control: the operator's own view names the operator (the decided proposal and the audit)
	if body := h.Do(t, "GET", "/api/audit?limit=200", nil).Body; containsAny(string(body), canaryHuman) == "" {
		t.Fatal("control: the operator's audit view should name the operator")
	}
	for _, rd := range reads {
		resp := agent.Do(t, "GET", rd.path, nil)
		if resp.Status/100 != 2 {
			t.Fatalf("rest %s: status %d: %s", rd.op, resp.Status, resp.Body)
		}
		check("rest", rd.op, resp.Body)
	}
	rw := []auth.Scope{auth.ScopeRead, auth.ScopeWrite}
	m, s := r.MCP(t, rw...), r.Stdio(t, rw...)
	for _, rd := range reads {
		for _, f := range []struct {
			name string
			call func() ([]byte, bool)
		}{
			{"mcp", func() ([]byte, bool) {
				res := rawTool(t, m, rd.op, rd.args, true)
				b, _ := json.Marshal(res)
				return b, res.IsError
			}},
			{"stdio", func() ([]byte, bool) {
				res := rawTool(t, s, rd.op, rd.args, true)
				b, _ := json.Marshal(res)
				return b, res.IsError
			}},
		} {
			b, isErr := f.call()
			if isErr {
				t.Fatalf("%s %s: tool error %s", f.name, rd.op, b)
			}
			check(f.name, rd.op, b)
		}
	}
}
