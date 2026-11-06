package mcpgen_test

import (
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// t1ParityIDs are the seven transform operations (C212.1): all generic, none human-only (D-T1-21).
var t1ParityIDs = []string{"estimate_transform", "create_transform", "get_transform", "list_transforms", "wait_for_transform", "cancel_transform", "get_transform_capabilities"}

// t1Cases are the parity cases of the seven operations (lane B4 owns this list). They run in this order on each rig, so a case
// reads the transform id from the previous one (wait from create, cancel from wait, get from cancel). The Take is the plain
// fixture Take; REST, MCP and stdio must answer the same JSON.
var t1Cases = []ParityCase{
	{Op: "get_transform_capabilities"},
	{Op: "estimate_transform", Args: fixedArgs(t1Glacier())},
	{Op: "create_transform", Args: fixedArgs(t1Glacier()), Masks: t1Masks},
	{Op: "wait_for_transform", Args: func(_ *servertest.Rig, prev map[string]any) map[string]any {
		return map[string]any{"id": prev["id"], "timeout_s": 0, "since_state": "running"}
	}, Masks: t1Masks},
	{Op: "cancel_transform", Args: func(_ *servertest.Rig, prev map[string]any) map[string]any {
		return map[string]any{"id": prev["transform"].(map[string]any)["id"]}
	}, Masks: t1Masks},
	{Op: "get_transform", Args: func(_ *servertest.Rig, prev map[string]any) map[string]any { return map[string]any{"id": prev["id"]} }, Masks: t1Masks},
	{Op: "list_transforms", Args: fixedArgs(map[string]any{"limit": 10}), Masks: t1Masks},
}

var t1Masks = []string{"id", "take_id"}

func t1Glacier() map[string]any {
	return map[string]any{"kind": "glacier", "glacier": map[string]any{"source_id": lt.TrackLuna, "factor": 20, "length_s": 10}}
}

func init() { parityCases = append(parityCases, t1Cases...) }

// TestParity_T1Cases: a parity case exists for each of the seven operations and for no other; all seven are generic MCP
// operations (no dedicated tool, tools/list unchanged, D-T1-21) and none is human-only.
func TestParity_T1Cases(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	var found []string
	for _, op := range api.Operations(r.api) {
		if !slices.Contains(t1ParityIDs, op.ID) {
			continue
		}
		found = append(found, op.ID)
		if op.Meta.MCP != "generic" || op.Meta.HumanOnly {
			t.Errorf("%s: MCP %q human-only %v, want generic and not human-only", op.ID, op.Meta.MCP, op.Meta.HumanOnly)
		}
	}
	if len(found) != 7 {
		t.Errorf("found %d transform operations, want 7", len(found))
	}
	for _, id := range t1ParityIDs {
		if !slices.ContainsFunc(t1Cases, func(c ParityCase) bool { return c.Op == id }) {
			t.Errorf("no parity case for %s", id)
		}
	}
	for _, c := range t1Cases {
		if !slices.Contains(t1ParityIDs, c.Op) {
			t.Errorf("parity case for %s, which is not a transform operation", c.Op)
		}
	}
}

// TestToolsList_T1NoTools: none of the seven is a first-class tool (operator decision of 2026-11-03).
func TestToolsList_T1NoTools(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sess := r.client(t, r.token(t, "t1-budget", auth.ScopeRead))
	res, err := sess.ListTools(callCtx(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		if slices.Contains(t1ParityIDs, tl.Name) {
			t.Errorf("%s is listed as a tool: T1 adds no dedicated MCP tool", tl.Name)
		}
	}
}
