package mcpgen_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
)

// l3ParityIDs are the 6 operations of C142 with an MCP face (all generic: the operator decided on 2026-11-01 that L3 adds no
// dedicated tool). decide_taxonomy_proposal is human judgment and has REST-only tests. map_sound_term keeps its L1 parity case
// because D-L3-22 is not approved.
var l3ParityIDs = []string{"suggest_sequence", "list_next_actions", "propose_taxonomy", "list_taxonomy_proposals", "get_song_atlas", "get_adherence"}

// l3Operations are the seven L3 operation ids.
var l3Operations = append(slices.Clone(l3ParityIDs), "decide_taxonomy_proposal")

func init() {
	for _, cs := range [][]ParityCase{l3SeqCases, l3NextActionsCases, l3TaxonomyCases, l3AtlasCases, l3AdherenceCases} {
		parityCases = append(parityCases, cs...)
	}
}

// l3Cases collects the lane-owned case lists (the aggregator of C146.2).
func l3Cases() []ParityCase {
	var all []ParityCase
	for _, cs := range [][]ParityCase{l3SeqCases, l3NextActionsCases, l3TaxonomyCases, l3AtlasCases, l3AdherenceCases} {
		all = append(all, cs...)
	}
	return all
}

// TestParity_L3Cases: a parity case exists for each of the six L3 operations with an MCP face and for no other L3 operation; the
// runner (TestParityEveryOperation) fails by name on a missing one. map_sound_term keeps its case (D-L3-22 is not approved).
func TestParity_L3Cases(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	var withFace []string
	for _, op := range api.Operations(r.api) {
		if slices.Contains(l3Operations, op.ID) && (op.Meta.MCP == "tool" || op.Meta.MCP == "generic") {
			withFace = append(withFace, op.ID)
		}
	}
	slices.Sort(withFace)
	want := slices.Clone(l3ParityIDs)
	slices.Sort(want)
	if !slices.Equal(withFace, want) {
		t.Errorf("L3 operations with an MCP face = %v, want %v", withFace, want)
	}
	cases := l3Cases()
	for _, id := range l3ParityIDs {
		if !slices.ContainsFunc(cases, func(c ParityCase) bool { return c.Op == id }) {
			t.Errorf("no parity case for %s", id)
		}
	}
	for _, c := range cases {
		if !slices.Contains(l3ParityIDs, c.Op) {
			t.Errorf("parity case for %s, which has no L3 MCP face", c.Op)
		}
	}
	if !slices.ContainsFunc(parityCases, func(c ParityCase) bool { return c.Op == "map_sound_term" }) {
		t.Error("map_sound_term lost its parity case: D-L3-22 is not approved, so agents keep the operation")
	}
}

// TestToolsList_L3Budget: no L3 operation is a first-class tool (the operator decided on 2026-11-01, as for L2), none is missing
// from call_operation's catalogue except the human-judgment one, and the measurement is printed.
func TestToolsList_L3Budget(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sess := r.client(t, r.token(t, "l3-budget", auth.ScopeRead))
	res, err := sess.ListTools(callCtx(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		if slices.Contains(l3Operations, tl.Name) {
			t.Errorf("%s is listed as a tool: L3 adds no dedicated MCP tool (operator decision of 2026-11-01)", tl.Name)
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

// TestOps_HumanJudgment_NoMCP_L3: decide_taxonomy_proposal is neither a tool nor reachable through call_operation, on every
// MCP face; the other six L3 operations are reachable (map_sound_term stays reachable: D-L3-22 is not approved).
func TestOps_HumanJudgment_NoMCP_L3(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sess := r.client(t, r.token(t, "l3-human-judgment", auth.ScopeRead, auth.ScopeWrite))
	tools, err := sess.ListTools(callCtx(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools.Tools {
		if tl.Name == "decide_taxonomy_proposal" {
			t.Error("decide_taxonomy_proposal is listed as a tool")
		}
	}
	res, err := sess.CallTool(callCtx(), &mcp.CallToolParams{Name: "call_operation", Arguments: map[string]any{"operation_id": "decide_taxonomy_proposal", "arguments": map[string]any{"id": "x", "decision": "reject"}}})
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
		t.Errorf("call_operation(decide_taxonomy_proposal) must answer NOT_FOUND: %+v %s", res.IsError, txt)
	}
	for _, op := range api.Operations(r.api) {
		if op.ID == "decide_taxonomy_proposal" && op.Meta.MCP != "exclude:human-judgment" {
			t.Errorf("decide_taxonomy_proposal MCP = %q", op.Meta.MCP)
		}
		if op.ID == "map_sound_term" && op.Meta.MCP != "generic" {
			t.Errorf("map_sound_term MCP = %q, want generic (D-L3-22 is not approved)", op.Meta.MCP)
		}
	}
}
