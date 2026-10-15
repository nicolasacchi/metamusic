package acceptance_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// TestSongwritingSettings_Surfaces: the operator sets the duration targets over the human-only route; agents read
// them (get_songwriting_settings), lint warns against them (with the values in details) and the track context shows them.
func TestSongwritingSettings_Surfaces(t *testing.T) {
	t.Parallel()
	r := m3Rig(t)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeAdmin)
	human := r.Human(t)
	targets := func(c interface {
		OK(testing.TB, string, string, any, ...string) any
	}) map[string]any {
		return obj(c.OK(t, "GET", "/api/settings/songwriting", nil))
	}
	want := map[string]any{"min_duration_s": 180.0, "target_min_duration_s": 240.0, "target_max_duration_s": 300.0}
	for k, v := range want {
		if got := targets(agent)[k]; got != v {
			t.Errorf("default %s = %v, want %v", k, got, v)
		}
	}

	// the write is human-only: agents get 403 HUMAN_ONLY, the agent path does not exist
	body := map[string]any{"min_duration_s": 200}
	resp := agent.Do(t, "PUT", "/human/api/settings/songwriting", body, "Idempotency-Key", "sw-agent")
	if p := obj(resp.JSON(t)); resp.Status != 403 || p["code"] != "HUMAN_ONLY" {
		t.Errorf("agent write: %d %s", resp.Status, resp.Body)
	}
	if resp := human.Do(t, "PUT", "/api/settings/songwriting", body, "Idempotency-Key", "sw-off"); resp.Status != 404 && resp.Status != 405 {
		t.Errorf("write off the human prefix: %d %s", resp.Status, resp.Body)
	}

	// validation
	for name, b := range map[string]map[string]any{
		"empty":        {},
		"below floor":  {"min_duration_s": 5},
		"min>target":   {"min_duration_s": 260},
		"target order": {"target_min_duration_s": 400},
	} {
		resp := human.Do(t, "PUT", "/human/api/settings/songwriting", b, "Idempotency-Key", "sw-bad-"+name)
		if p := obj(resp.JSON(t)); resp.Status != 422 || p["code"] != "VALIDATION_FAILED" {
			t.Errorf("%s: %d %s", name, resp.Status, resp.Body)
		}
	}

	// the operator's write is seen by the agent read, by lint and by the context bundle
	resp = human.Do(t, "PUT", "/human/api/settings/songwriting", map[string]any{"min_duration_s": 200, "target_max_duration_s": 330}, "Idempotency-Key", "sw-ok")
	if resp.Status != 200 {
		t.Fatalf("human write: %d %s", resp.Status, resp.Body)
	}
	got := targets(agent)
	if got["min_duration_s"] != 200.0 || got["target_min_duration_s"] != 240.0 || got["target_max_duration_s"] != 330.0 {
		t.Errorf("after write: %v", got)
	}
	lint := obj(agent.OK(t, "POST", "/api/recipes/lint", map[string]any{"items": []map[string]any{
		{"request": map[string]any{"id": "sw1", "style": "english, 100 bpm", "lyrics": "[Verse]\noro kala\nvenu tilo\nmira dosa\nlano pera\n"}}}}))
	item := obj(lint["items"].([]any)[0])
	var found map[string]any
	for _, w := range item["warnings"].([]any) {
		if obj(w)["code"] == "duration_under_target" {
			found = obj(w)
		}
	}
	if found == nil {
		t.Fatalf("lint has no duration_under_target: %v", item["warnings"])
	}
	d := obj(found["details"])
	if d["min_s"] != 200.0 || d["target_min_s"] != 240.0 || d["target_max_s"] != 330.0 || d["estimated_s"] == nil {
		t.Errorf("details: %v", d)
	}
	ctxb := obj(agent.OK(t, "GET", "/api/tracks/"+lt.TrackLuna+"/context", nil))
	sw := obj(ctxb["songwriting"])
	if sw["min_duration_s"] != 200.0 || sw["target_max_duration_s"] != 330.0 {
		t.Errorf("context songwriting: %v", sw)
	}
}
