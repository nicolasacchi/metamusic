package mcpgen_test

import (
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
)

// M6N-C4 (C383): the five generic Morning Crate operations answer the same body on every face; the two person-only ones
// (submit_morning_crate_events, reveal_morning_crate) are not reachable over MCP at all. The rig holds no media tools, so a build
// only starts (its state, time and error code race the background build and are masked); `Last` keeps it after every read.
var morningParityMasks = []string{"state", "built_at", "error_code", "id", "created_at", "updated_at", "updated_by", "sha256"}

var morningCases = []ParityCase{
	{Op: "update_morning_crate_settings", Args: fixedArgs(map[string]any{"minutes": 35, "wait_minutes": 20}), Masks: morningParityMasks},
	{Op: "get_morning_crate", Variant: "unknown crate", Args: fixedArgs(map[string]any{"id": "01900000-0000-7000-8000-00000000dead"}), Status: 404},
	{Op: "list_morning_crates", Variant: "filtered", Args: fixedArgs(map[string]any{"state": "ready", "limit": 5})},
	{Op: "build_morning_crate", Args: fixedArgs(map[string]any{"minutes": 20}), Masks: morningParityMasks, SkipGeneric: true, Last: true},
}

func init() { parityCases = append(parityCases, morningCases...) }

// TestParity_M6NMorning: the five operations are generic (never dedicated tools) and have cases; the two person-only operations are
// excluded from MCP and absent from the tool list.
func TestParity_M6NMorning(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	generic := []string{"get_morning_crate_settings", "update_morning_crate_settings", "list_morning_crates", "get_morning_crate", "build_morning_crate"}
	personOnly := []string{"submit_morning_crate_events", "reveal_morning_crate"}
	found := map[string]string{}
	for _, op := range api.Operations(r.api) {
		found[op.ID] = op.Meta.MCP
	}
	for _, id := range generic {
		if found[id] != "generic" {
			t.Errorf("%s is %q over MCP, want generic", id, found[id])
		}
	}
	for _, id := range personOnly {
		if found[id] != "exclude:person-only" {
			t.Errorf("%s is %q over MCP, want exclude:person-only", id, found[id])
		}
	}
	sess := r.client(t, r.token(t, "morning-write", auth.ScopeRead, auth.ScopeWrite))
	res, err := sess.ListTools(callCtx(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		if slices.Contains(generic, tl.Name) || slices.Contains(personOnly, tl.Name) {
			t.Errorf("%s is listed as a tool", tl.Name)
		}
	}
	for _, id := range []string{"update_morning_crate_settings", "get_morning_crate", "build_morning_crate"} {
		if !slices.ContainsFunc(morningCases, func(c ParityCase) bool { return c.Op == id }) {
			t.Errorf("no parity case for %s", id)
		}
	}
	// the person-only operations are not offered through call_operation either
	for _, id := range personOnly {
		raw := callTool(t, sess, "call_operation", map[string]any{"operation_id": id, "arguments": map[string]any{"id": "x"}})
		if !raw.IsError {
			t.Errorf("call_operation(%s) succeeded", id)
		}
	}
}
