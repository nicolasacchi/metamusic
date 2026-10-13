package acceptance_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

const (
	canaryHuman  = "canary-human"
	canaryDevice = "canary-phone-name"
)

// walkHumans visits every object that names a principal (kind and id keys) and
// reports human rows whose id is not the neutral "human".
func walkHumans(v any, bad func(id string)) {
	switch x := v.(type) {
	case map[string]any:
		if k, _ := x["kind"].(string); k == "human" {
			if id, ok := x["id"].(string); ok && id != "human" {
				bad(id)
			}
		}
		for _, e := range x {
			walkHumans(e, bad)
		}
	case []any:
		for _, e := range x {
			walkHumans(e, bad)
		}
	}
}

// TestAgentsNeverSeeHumanNames: the operator's username and a device's label are
// canaries; no response an agent receives over REST, MCP or stdio contains them.
func TestAgentsNeverSeeHumanNames(t *testing.T) {
	r := archiveRig(t, servertest.Options{Canary: true})
	h := r.Human(t)
	dev, _ := r.DeviceWithCookie(t, canaryDevice)
	tr := track(t, h, true)

	// The operator and a device leave judgments, drafts, playlists and exports behind.
	h.OK(t, "PUT", "/api/tracks/"+tr+"/rating", map[string]any{"stars": 4, "verdict": "keeper"})
	h.OK(t, "PATCH", "/api/tracks/"+tr+"/tags", map[string]any{"add": []string{"ballad"}})
	h.OK(t, "PATCH", "/api/tracks/"+tr, map[string]any{"status": "final", "title": "Renamed"})
	h.OK(t, "POST", "/api/tracks/"+tr+"/comments", map[string]any{"body": "operator note", "at_second": 2.0}, "Idempotency-Key", "n-1")
	dev.OK(t, "POST", "/api/tracks/"+tr+"/comments", map[string]any{"body": "device note", "kind": "fix"}, "Idempotency-Key", "n-2")
	dev.OK(t, "POST", "/api/listen-events", map[string]any{"events": []map[string]any{{"id": "01900000-0000-7000-8000-0000000000c1",
		"track_id": tr, "session_id": "s1", "kind": "start", "client_at": "2026-10-06T11:59:00Z"}}})
	draft := obj(h.OK(t, "POST", "/api/drafts", map[string]any{"request": map[string]any{"id": "names_d", "style": "pop", "lyrics": "[Verse]\nla", "cot": "full", "seed": 3}}, "Idempotency-Key", "n-3"))["id"].(string)
	pl := obj(h.OK(t, "POST", "/api/playlists", map[string]any{"name": "Names", "track_ids": []string{tr}}, "Idempotency-Key", "n-4"))["id"].(string)
	exp := obj(h.OK(t, "POST", "/api/playlists/"+pl+"/exports", nil, "Idempotency-Key", "n-5"))["id"].(string)
	h.OK(t, "POST", "/api/exports/jsonl", map[string]any{"selection": "tracks", "tracks": map[string]any{"track_ids": []string{tr}}}, "Idempotency-Key", "n-6")

	reads := []struct {
		op   string
		args map[string]any
		path string
	}{
		{"get_track", map[string]any{"id": tr}, "/api/tracks/" + tr},
		{"search_tracks", map[string]any{"limit": 100}, "/api/tracks?limit=100"},
		{"list_comments", map[string]any{"id": tr, "limit": 50}, "/api/tracks/" + tr + "/comments?limit=50"},
		{"get_track_context", map[string]any{"id": tr}, "/api/tracks/" + tr + "/context"},
		{"list_tags", nil, "/api/tags"},
		{"get_library_facets", nil, "/api/library/facets"},
		{"list_drafts", map[string]any{"limit": 100}, "/api/drafts?limit=100"},
		{"get_draft", map[string]any{"id": draft}, "/api/drafts/" + draft},
		{"list_playlists", nil, "/api/playlists"},
		{"get_playlist", map[string]any{"id": pl}, "/api/playlists/" + pl},
		{"get_export", map[string]any{"id": exp}, "/api/exports/" + exp},
		{"get_stats", nil, "/api/stats"},
		{"list_tokens", nil, "/api/tokens"},
		{"get_audit", map[string]any{"limit": 200}, "/api/audit?limit=200"},
		{"get_audit", map[string]any{"limit": 200, "principal_kind": []string{"human", "device"}}, "/api/audit?limit=200&principal_kind=human&principal_kind=device"},
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

	// Positive control: the operator's own view does carry the canaries.
	if body := h.Do(t, "GET", "/api/audit?limit=200", nil).Body; containsAny(string(body), canaryHuman) == "" {
		t.Fatal("control: the operator's audit view should name the operator")
	}
	if body := h.Do(t, "GET", "/human/api/devices", nil).Body; containsAny(string(body), canaryDevice) == "" {
		t.Fatal("control: the operator's device list should show the device label")
	}

	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	for _, rd := range reads {
		resp := agent.Do(t, "GET", rd.path, nil)
		if resp.Status/100 != 2 {
			t.Fatalf("rest %s: status %d: %s", rd.op, resp.Status, resp.Body)
		}
		check("rest", rd.op, resp.Body)
	}
	modes := map[string][]bool{} // first-class tools are also called through call_operation
	for _, op := range api.Operations(r.API) {
		if op.Meta.MCP == "tool" {
			modes[op.ID] = []bool{false, true}
		} else {
			modes[op.ID] = []bool{true}
		}
	}
	m, s := r.MCP(t, rw...), r.Stdio(t, rw...)
	for _, rd := range reads {
		for _, f := range []struct {
			name string
			sess *mcp.ClientSession
		}{{"mcp", m}, {"stdio", s}} {
			for _, generic := range modes[rd.op] {
				res := rawTool(t, f.sess, rd.op, rd.args, generic)
				if res.IsError {
					t.Fatalf("%s %s: tool error %+v", f.name, rd.op, res.Content)
				}
				b, _ := json.Marshal(res)
				check(f.name, rd.op, b)
			}
		}
	}
}

func canaryKind(hit string) string {
	if strings.Contains(hit, "phone") {
		return "device-name"
	}
	return "human-username"
}
