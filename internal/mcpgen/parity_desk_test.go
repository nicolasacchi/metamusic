package mcpgen_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// deskParityIDs are the Composer's Desk operations that exist on this branch (grows with each Desk lane; all generic: D-DESK-14).
var deskParityIDs = []string{"list_plans", "get_plan", "check_abc", "create_plan_from_take", "edit_plan", "set_plan_verdict", "create_plan_audition", "get_plan_audition"}

// deskScore is the synthetic aligned score of the fixture take lt.TrackLuna (the library fixture records its hash but writes no file).
const deskScore = "X:1\nT:t\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\n% verse\nCDEF|CDEF|\n% chorus\nCDEF|CDEF|\n% outro\nCDEF|CDEF|]\n"

// deskMasks hides who made a plan (the REST face and the MCP faces use different agent tokens) and the plan ids.
// Plan ids differ between faces because each face draws a different number of ids before the step (idempotency rows, audit rows).
var deskMasks = []string{"created_by", "by", "id", "parent_plan_id", "plan_id"}

// deskSeed writes the fixture take's score file into the rig's data directory, as the ingest does for a real take.
// A parity Args builder has no *testing.T, so a failure panics: the script then fails loudly instead of at a confusing later step.
func deskSeed(r *servertest.Rig) {
	dir := filepath.Join(r.DataDir, "takes", lt.TrackLuna)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "score.abc"), []byte(deskScore), 0o444); err != nil && !os.IsExist(err) {
		panic(err)
	}
}

// deskPlanID reads the id of the previous case's response.
func deskPlanID(prev map[string]any) string {
	id, _ := prev["id"].(string)
	return id
}

// deskCases are the parity cases of the Desk operations. The script runs in order on every rig: open the fixture take's score, read the
// plan back, edit it, judge the edit, list. Each step answers the same body over REST, call_operation and stdio.
var deskCases = []ParityCase{
	{Op: "list_plans", Variant: "empty desk", Args: fixedArgs(map[string]any{"limit": 10})},
	{Op: "check_abc", Args: fixedArgs(map[string]any{"abc": deskScore}), Masks: deskMasks},
	{Op: "check_abc", Variant: "with a base", Args: fixedArgs(map[string]any{"abc": "X:1\nK:C\nCDEFGABc|\n", "base_abc": deskScore, "dialect": "standard"})},
	{Op: "get_plan", Variant: "unknown plan", Args: fixedArgs(map[string]any{"id": "nope"}), Status: 404},
	{Op: "create_plan_from_take", Variant: "unknown take", Args: fixedArgs(map[string]any{"id": "nope"}), Status: 404},
	{Op: "create_plan_from_take", Variant: "take without a score", Args: fixedArgs(map[string]any{"id": lt.TrackCitta}), Status: 422},
	{Op: "create_plan_from_take", Masks: deskMasks, Args: func(r *servertest.Rig, _ map[string]any) map[string]any {
		deskSeed(r)
		return map[string]any{"id": lt.TrackLuna}
	}},
	{Op: "get_plan", Variant: "with bars", Masks: deskMasks, Args: func(_ *servertest.Rig, prev map[string]any) map[string]any {
		return map[string]any{"id": deskPlanID(prev), "include": "abc,bars,display,auditions,renders"}
	}},
	{Op: "edit_plan", Masks: deskMasks, Args: func(_ *servertest.Rig, prev map[string]any) map[string]any {
		return map[string]any{"id": deskPlanID(prev), "rationale": "a brighter second bar",
			"ops": []map[string]any{{"op": "replace_bars", "voice": "1", "bar_from": 1, "bar_to": 1, "abc": "CDEFGABc"}}}
	}},
	{Op: "set_plan_verdict", Masks: deskMasks, SkipGeneric: true, Args: func(_ *servertest.Rig, prev map[string]any) map[string]any {
		return map[string]any{"id": deskPlanID(prev), "verdict": "liked", "note": "the second bar lifts"}
	}},
	{Op: "list_plans", Variant: "after edits", Masks: deskMasks, Args: fixedArgs(map[string]any{"source": "edit"})},
	{Op: "edit_plan", Variant: "no change", Args: func(_ *servertest.Rig, prev map[string]any) map[string]any {
		items, _ := prev["items"].([]any)
		id := ""
		if len(items) > 0 {
			id, _ = items[0].(map[string]any)["parent_plan_id"].(string)
		}
		return map[string]any{"id": id, "ops": []map[string]any{{"op": "replace_bars", "voice": "1", "bar_from": 1, "bar_to": 1, "abc": "CDEF"}}}
	}, Status: 422},
	// DESK-S2: the parity rig has no soundfont or fluidsynth, so an audition answers the same 503 on every face; a missing audition is 404
	{Op: "create_plan_audition", Variant: "pipeline unavailable", Args: fixedArgs(map[string]any{"id": "nope"}), Status: 503},
	{Op: "get_plan_audition", Variant: "unknown audition", Args: fixedArgs(map[string]any{"id": "nope", "aid": "nope"}), Status: 404},
}

func init() { parityCases = append(parityCases, deskCases...) }

// TestParity_DeskOps: every Desk operation has a parity case and is generic over MCP, never a dedicated tool (D-DESK-14).
func TestParity_DeskOps(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	for _, op := range api.Operations(r.api) {
		if slices.Contains(deskParityIDs, op.ID) && op.Meta.MCP != "generic" {
			t.Errorf("%s is %q over MCP, want generic (D-DESK-14)", op.ID, op.Meta.MCP)
		}
	}
	for _, id := range deskParityIDs {
		if !slices.ContainsFunc(deskCases, func(c ParityCase) bool { return c.Op == id }) {
			t.Errorf("no parity case for %s", id)
		}
	}
	sess := r.client(t, r.token(t, "desk-budget", auth.ScopeRead))
	res, err := sess.ListTools(callCtx(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		if slices.Contains(deskParityIDs, tl.Name) {
			t.Errorf("%s is listed as a tool", tl.Name)
		}
	}
}
