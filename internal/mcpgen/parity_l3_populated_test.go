package mcpgen_test

import (
	"encoding/json"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestParityRig_L3AnswersPopulated: the parity rig carries the L3 fixture and the real feature step, so the three L3 reads the
// parity cases compare are not vacuous (an empty answer would make REST = MCP = stdio trivially true).
func TestParityRig_L3AnswersPopulated(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, parityRigOptions())
	r.Settle(t)
	c, _ := r.Agent(t, auth.ScopeRead, auth.ScopeGenerate)
	get := func(path string) map[string]any {
		resp := c.Do(t, "GET", path, nil)
		if resp.Status != 200 {
			t.Fatalf("%s: %d %s", path, resp.Status, resp.Body)
		}
		var m map[string]any
		if err := json.Unmarshal(resp.Body, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if pts, _ := get("/api/atlas?x=tempo&y=energy&limit=50")["points"].([]any); len(pts) < 5 {
		t.Errorf("get_song_atlas answers %d points", len(pts))
	}
	if it, _ := get("/api/next-actions?limit=5")["items"].([]any); len(it) == 0 {
		t.Error("list_next_actions answers no item")
	}
	ad := get("/api/atlas/adherence?min_n=3")
	if sum, _ := ad["summary"].(map[string]any); sum["n"] == nil || sum["n"].(float64) < 3 {
		t.Errorf("get_adherence compares %v pairs", sum["n"])
	}
}
