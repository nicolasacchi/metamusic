package mcpgen_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// l2StubStatus is the status of an L2 stub's answer: 501 NOT_IMPLEMENTED naming its lane. While a stub stands, its parity case
// expects that failure, the same on REST, MCP and stdio; the lane that fills the handler removes the Status of its cases and
// tunes Args and Masks against the L2 fixture (C132.2b, the final cases are lane INT-B's).
const l2StubStatus = 501

// l2ParityIDs are the 15 operations of C129 with an MCP face (5 tools and 10 generic). publish_record and
// set_curation_settings are human-only: they have REST-only tests (TestPublish_*, TestSettings_HumanOnly,
// TestOps_HumanOnly_NoMCP_L2).
var l2ParityIDs = []string{
	"list_records", "get_record", "create_record", "update_record", "edit_record_slots", "get_record_changes", "propose_record",
	"export_record_tape", "get_tape", "list_crates", "get_crate", "create_crate", "update_crate", "edit_crate_items", "get_curation_settings",
}

// l2RecordMasks hide the values that differ per face because the faces replay reads through call_operation and so draw ids from
// different points of the sequence: the ids, the signed cover links (their key holds the Record id) and the token-derived refs.
var l2RecordMasks = []string{"id", "slot_id", "created_by", "actor", "cover", "by"}

// l2Songs returns the ids of the first n Songs of the rig, in id order, leaving out the Songs of the takes other cases look at
// (a Record's slots appear in a take's context, and Record ids differ per face).
func l2Songs(r *servertest.Rig, n int) []string {
	rows, err := r.Store.R.Query(`SELECT id FROM songs WHERE retired_at IS NULL AND COALESCE(root_track_id, '') NOT IN (?, ?) ORDER BY id LIMIT ?`,
		lt.TrackLuna, lt.TrackCitta, n)
	if err != nil {
		panic(err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			panic(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// l2RecordOf is the record object of the previous case's RecordView.
func l2RecordOf(prev map[string]any) map[string]any {
	rec, _ := prev["record"].(map[string]any)
	return rec
}

// l2FirstSlot is the id of the first slot of the previous case's RecordView.
func l2FirstSlot(prev map[string]any) string {
	slots, _ := prev["slots"].([]any)
	if len(slots) == 0 {
		return ""
	}
	s, _ := slots[0].(map[string]any)
	id, _ := s["id"].(string)
	return id
}

// l2Cases: ids are placeholders until INT-B points them at FixtureL2; the stubs answer before they look.
var l2Cases = []ParityCase{
	// L2-V2: one Record walked through the Records operations in order; each case reads the id and revision from the previous
	// case's response (the mutations are not replayed through call_operation).
	{Op: "create_record", SkipGeneric: true, Masks: l2RecordMasks, Args: func(r *servertest.Rig, _ map[string]any) map[string]any {
		return map[string]any{"title": "Synthetic Record 001", "song_ids": l2Songs(r, 2)}
	}},
	{Op: "get_record", Masks: l2RecordMasks, Args: func(_ *servertest.Rig, prev map[string]any) map[string]any {
		return map[string]any{"id": l2RecordOf(prev)["id"], "include": []string{"liner", "energy", "changes", "preview"}}
	}},
	{Op: "update_record", SkipGeneric: true, Masks: l2RecordMasks, Args: func(_ *servertest.Rig, prev map[string]any) map[string]any {
		rec := l2RecordOf(prev)
		return map[string]any{"id": rec["id"], "revision": rec["revision"], "title": "Synthetic Record 002"}
	}},
	{Op: "edit_record_slots", SkipGeneric: true, Masks: l2RecordMasks, Args: func(_ *servertest.Rig, prev map[string]any) map[string]any {
		rec := l2RecordOf(prev)
		return map[string]any{"id": rec["id"], "revision": rec["revision"], "ops": []map[string]any{{"op": "pin_all"}, {"op": "set_side", "slot_id": l2FirstSlot(prev), "side": "A"}}}
	}},
	{Op: "propose_record", SkipGeneric: true, Masks: l2RecordMasks, Args: func(_ *servertest.Rig, prev map[string]any) map[string]any {
		rec := l2RecordOf(prev)
		return map[string]any{"id": rec["id"], "revision": rec["revision"], "rationale": "ready to hear"}
	}},
	{Op: "get_record_changes", Masks: l2RecordMasks, Args: func(_ *servertest.Rig, prev map[string]any) map[string]any {
		return map[string]any{"id": l2RecordOf(prev)["id"], "order": "asc"}
	}},
	{Op: "list_records", Masks: l2RecordMasks},
	{Op: "export_record_tape", Args: fixedArgs(map[string]any{"id": "rec-stub"}), SkipGeneric: true, Status: l2StubStatus},
	{Op: "get_tape", Args: fixedArgs(map[string]any{"id": "tape-stub"}), Status: l2StubStatus},
	{Op: "list_crates"},
	{Op: "get_crate", Args: fixedArgs(map[string]any{"id": "crate-stub"}), Status: 404},
	{Op: "create_crate", Args: fixedArgs(map[string]any{"name": "Synthetic Crate 001", "kind": "manual"}), SkipGeneric: true, Masks: []string{"id", "created_by", "created_at", "updated_at"}},
	{Op: "update_crate", Args: fixedArgs(map[string]any{"id": "crate-stub", "revision": 1, "name": "Synthetic Crate 002"}), Status: 404},
	{Op: "edit_crate_items", Args: fixedArgs(map[string]any{"id": "crate-stub", "add": []map[string]any{{"song_id": "song-stub"}}}), Status: 404},
	{Op: "get_curation_settings"},
}

func init() {
	parityCases = append(parityCases, l2Cases...)
}

// TestParity_L2Cases: a parity case exists for each of the 15 L2 operations with an MCP face, and for no other L2 operation;
// the runner (TestParityEveryOperation) fails by name on a missing one.
func TestParity_L2Cases(t *testing.T) {
	t.Parallel()
	if len(l2Cases) != 15 || len(l2ParityIDs) != 15 {
		t.Fatalf("%d cases and %d ids, want 15 and 15", len(l2Cases), len(l2ParityIDs))
	}
	r := newRig(t)
	var withFace []string
	for _, op := range api.Operations(r.api) {
		l2 := slices.Contains(l2ParityIDs, op.ID) || op.ID == "publish_record" || op.ID == "set_curation_settings"
		if !l2 {
			continue
		}
		if op.Meta.MCP == "tool" || op.Meta.MCP == "generic" {
			withFace = append(withFace, op.ID)
		}
	}
	slices.Sort(withFace)
	want := slices.Clone(l2ParityIDs)
	slices.Sort(want)
	if !slices.Equal(withFace, want) {
		t.Errorf("L2 operations with an MCP face = %v, want %v", withFace, want)
	}
	for _, id := range l2ParityIDs {
		if !slices.ContainsFunc(l2Cases, func(c ParityCase) bool { return c.Op == id }) {
			t.Errorf("no parity case for %s", id)
		}
	}
}

// l2Operations are the 17 operations of C129. D-L2-21 planned five dedicated tools; they measured 15 645 compact bytes, and
// after the demotion order the last two still took 10 940, so the operator decided (2026-10-28) that L2 adds no tool: every
// L2 operation is reachable through call_operation.
var l2Operations = append(slices.Clone(l2ParityIDs), "publish_record", "set_curation_settings")

// TestToolsList_L2Ceiling: no L2 operation is a first-class tool, and tools/list stays within toolsCeilingBytes. It prints the
// measurement.
func TestToolsList_L2Ceiling(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sess := r.client(t, r.token(t, "l2-budget", auth.ScopeRead))
	res, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		if slices.Contains(l2Operations, tl.Name) {
			t.Errorf("%s is listed as a tool: L2 adds no dedicated MCP tool (D-L2-21 as decided)", tl.Name)
		}
	}
	all, err := json.Marshal(res.Tools)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("tools/list is %d compact bytes over %d tools (ceiling %d)", len(all), len(res.Tools), toolsCeilingBytes)
	if len(all) > toolsCeilingBytes {
		t.Errorf("tools/list is %d bytes, over the %d ceiling", len(all), toolsCeilingBytes)
	}
}

// TestOps_HumanOnly_NoMCP_L2: publish_record and set_curation_settings are neither tools nor reachable through call_operation
// (publishing is a human act, D-L2-4; the opt-ins are human-only, D-L2-16), on every MCP face.
func TestOps_HumanOnly_NoMCP_L2(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sess := r.client(t, r.token(t, "l2-human-only", auth.ScopeRead, auth.ScopeWrite, auth.ScopeAdmin))
	tools, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools.Tools {
		if tl.Name == "publish_record" || tl.Name == "set_curation_settings" {
			t.Errorf("%s must not be listed", tl.Name)
		}
	}
	for _, op := range []string{"publish_record", "set_curation_settings"} {
		res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "call_operation", Arguments: map[string]any{"operation_id": op, "arguments": map[string]any{"id": "x"}}})
		if err != nil {
			t.Fatal(err)
		}
		var txt string
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				txt += tc.Text
			}
		}
		if !res.IsError || !strings.Contains(txt, "NOT_FOUND") {
			t.Errorf("call_operation(%s) must answer NOT_FOUND: %+v %s", op, res.IsError, txt)
		}
	}
}
