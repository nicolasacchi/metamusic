package mcpgen_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/api"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// BULK-P6 (C398.5): list_pods is generic over MCP and answers the same body on REST and through call_operation; an agent's estimate that
// names the pod backend (or asks for the pod canary) is refused the same way on every face. The rig has no pod backend (the production
// shape with MM_POD_BACKEND=0), so the refusal is BACKEND_UNAVAILABLE.
var bulkCases = []ParityCase{
	{Op: "list_pods", Variant: "defaults", Args: fixedArgs(map[string]any{})},
	{Op: "list_pods", Variant: "all of one batch", Args: fixedArgs(map[string]any{"batch_id": lt.BatchDone, "state": "all", "limit": 10})},
	{Op: "list_pods", Variant: "unknown state", Args: fixedArgs(map[string]any{"state": "dead"}), Status: 422},
	{Op: "estimate_generation", Variant: "pod backend refused", Status: 422, Args: fixedArgs(map[string]any{
		"items":   []map[string]any{{"mode": "new_seed", "parent_track_id": lt.TrackLuna, "n_seeds": 3}},
		"options": map[string]any{"backend": "runpod-pod"}})},
	{Op: "estimate_generation", Variant: "pod canary refused", Status: 422, Args: fixedArgs(map[string]any{
		"items":   []map[string]any{{"mode": "new_seed", "parent_track_id": lt.TrackLuna, "n_seeds": 1}},
		"options": map[string]any{"pod_canary": "self_terminate"}})},
	{Op: "estimate_generation", Variant: "unknown pod canary value", Status: 422, Args: fixedArgs(map[string]any{
		"items":   []map[string]any{{"mode": "new_seed", "parent_track_id": lt.TrackLuna, "n_seeds": 1}},
		"options": map[string]any{"pod_canary": "other"}})},
	{Op: "estimate_generation", Variant: "options.pod is not an option", Status: 422, Args: fixedArgs(map[string]any{
		"items":   []map[string]any{{"mode": "new_seed", "parent_track_id": lt.TrackLuna, "n_seeds": 1}},
		"options": map[string]any{"pod": true}})},
}

func init() { parityCases = append(parityCases, bulkCases...) }

// TestParity_Bulk: list_pods is registered, generic over MCP, Owner-only and never a first-class tool, and has its cases.
func TestParity_Bulk(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	found := false
	for _, op := range api.Operations(r.api) {
		if op.ID != "list_pods" {
			continue
		}
		found = true
		if op.Meta.MCP != "generic" || !op.Meta.OwnerOnly || op.Meta.HumanOnly || op.Meta.UI != "/jobs" {
			t.Errorf("list_pods meta: mcp %q owner-only %v human-only %v ui %q", op.Meta.MCP, op.Meta.OwnerOnly, op.Meta.HumanOnly, op.Meta.UI)
		}
	}
	if !found {
		t.Fatal("list_pods is not registered")
	}
	sess := r.client(t, r.token(t, "bulk-read", "read"))
	res, err := sess.ListTools(callCtx(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		if tl.Name == "list_pods" {
			t.Error("list_pods is listed as a tool")
		}
	}
}
