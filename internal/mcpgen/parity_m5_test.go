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

// m5ParityIDs are the 8 operations of C260 with an MCP face (all generic: M5 adds no dedicated tool, the L2/L3 operator decision).
// create_voice_note is a human recording (box B5) and has REST-only tests.
var m5ParityIDs = []string{"propose_patch", "get_patch", "list_patches", "list_callbacks", "get_callback", "submit_callback_verdict", "get_listen_heat", "suggest_duel_pairs"}

// m5Operations are the nine M5 operation ids.
var m5Operations = append(slices.Clone(m5ParityIDs), "create_voice_note")

// m5Cases collects the lane-owned case lists (one file per lane, so parallel lanes never edit the same lines).
func m5Cases() []ParityCase {
	var all []ParityCase
	for _, cs := range [][]ParityCase{m5SongPRCases, m5CallbacksCases, m5AttentionCases, m5DuelsCases} {
		all = append(all, cs...)
	}
	return all
}

func init() { parityCases = append(parityCases, m5Cases()...) }

// TestParity_M5Cases: a parity case exists for each of the eight M5 operations with an MCP face and for no other M5 operation;
// the runner (TestParityEveryOperation) fails by name on a missing one. create_voice_note is skipped on purpose (D-M5-23).
func TestParity_M5Cases(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	var withFace []string
	for _, op := range api.Operations(r.api) {
		if slices.Contains(m5Operations, op.ID) && (op.Meta.MCP == "tool" || op.Meta.MCP == "generic") {
			withFace = append(withFace, op.ID)
		}
	}
	slices.Sort(withFace)
	want := slices.Clone(m5ParityIDs)
	slices.Sort(want)
	if !slices.Equal(withFace, want) {
		t.Errorf("M5 operations with an MCP face = %v, want %v", withFace, want)
	}
	cases := m5Cases()
	for _, id := range m5ParityIDs {
		if !slices.ContainsFunc(cases, func(c ParityCase) bool { return c.Op == id }) {
			t.Errorf("no parity case for %s", id)
		}
	}
	for _, c := range cases {
		if !slices.Contains(m5ParityIDs, c.Op) {
			t.Errorf("parity case for %s, which has no M5 MCP face", c.Op)
		}
	}
}

// TestToolsList_M5Budget: no M5 operation is a first-class tool (operator decision, L2/L3 precedent), none but the human
// recording is missing from call_operation's catalogue, and the measurement is printed. The byte count of tools/list grew only
// by schema growth on six existing tools (see toolsCeilingBytes).
func TestToolsList_M5Budget(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sess := r.client(t, r.token(t, "m5-budget", auth.ScopeRead))
	res, err := sess.ListTools(callCtx(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		if slices.Contains(m5Operations, tl.Name) {
			t.Errorf("%s is listed as a tool: M5 adds no dedicated MCP tool", tl.Name)
		}
	}
	if len(res.Tools) != 39 {
		t.Errorf("tools/list has %d tools, want the 39 of L3", len(res.Tools))
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

// TestOps_HumanRecording_NoMCP_M5: create_voice_note is neither a tool nor reachable through call_operation, on every MCP face.
func TestOps_HumanRecording_NoMCP_M5(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sess := r.client(t, r.token(t, "m5-voice", auth.ScopeRead, auth.ScopeWrite))
	tools, err := sess.ListTools(callCtx(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools.Tools {
		if tl.Name == "create_voice_note" {
			t.Error("create_voice_note is listed as a tool")
		}
	}
	res, err := sess.CallTool(callCtx(), &mcp.CallToolParams{Name: "call_operation", Arguments: map[string]any{"operation_id": "create_voice_note", "arguments": map[string]any{"id": "x", "audio_base64": "AAAA"}}})
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
		t.Errorf("call_operation(create_voice_note) must answer NOT_FOUND: %+v %s", res.IsError, txt)
	}
	for _, op := range api.Operations(r.api) {
		if op.ID == "create_voice_note" && op.Meta.MCP != "exclude:human-recording" {
			t.Errorf("create_voice_note MCP = %q", op.Meta.MCP)
		}
	}
}
