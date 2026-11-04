package acceptance_test

import (
	"encoding/json"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// M5-F2: the scripted agent session (every_ui_action_test.go) gets its M5 entries from one file per lane
// (m5_<lane>_script_test.go), each adding to uiScript and uiOrder from an init, so parallel lanes never edit the same lines.
// While a lane has not landed its operations answer the 501 stub and the loop skips them.

// m5StubOf returns the task of an M5 stub's tool error (the problem 501 NOT_IMPLEMENTED with details.task "M5-..."), else "".
func m5StubOf(res *mcp.CallToolResult) string {
	if res == nil || !res.IsError || len(res.Content) == 0 {
		return ""
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		return ""
	}
	var p struct {
		Code    string `json:"code"`
		Details struct {
			Task string `json:"task"`
		} `json:"details"`
	}
	if json.Unmarshal([]byte(tc.Text), &p) != nil || p.Code != "NOT_IMPLEMENTED" || !strings.HasPrefix(p.Details.Task, "M5-") {
		return ""
	}
	return p.Details.Task
}
