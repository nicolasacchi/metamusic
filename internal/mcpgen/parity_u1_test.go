package mcpgen_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestEditorListenerMcp401 (C304.1): /mcp is served to agent principals only. An Editor, a Listener, the Owner's own
// login and a device get 401 with the Bearer challenge, so Editors and Listeners have no MCP path in v1 and the tool
// list is never filtered per role.
func TestEditorListenerMcp401(t *testing.T) {
	t.Parallel()
	rig := servertest.New(t, servertest.Options{})
	for name, c := range map[string]*servertest.Client{
		"editor":   rig.User(t, servertest.UserEditor, config.RoleEditor),
		"listener": rig.User(t, servertest.UserListener, config.RoleListener),
		"owner":    rig.Human(t),
		"device":   rig.Device(t),
	} {
		for _, path := range []string{"/mcp", "/mcp/"} {
			resp := c.Do(t, "POST", path, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
				"Content-Type", "application/json", "Accept", "application/json, text/event-stream")
			wa := resp.Header.Get("Www-Authenticate")
			if resp.Status != 401 || !strings.HasPrefix(wa, "Bearer") || !strings.Contains(string(resp.Body), "UNAUTHENTICATED") {
				t.Errorf("%s %s: status %d, challenge %q, body %s", name, path, resp.Status, wa, resp.Body)
			}
		}
	}
	// The same rig still serves an agent: the refusal is about the principal, not the endpoint.
	sess := rig.MCP(t, auth.ScopeRead)
	if _, err := sess.ListTools(callCtx(), nil); err != nil {
		t.Fatalf("agent tools/list: %v", err)
	}
}

// TestOwnerAgentToolsListGolden (C304.2): the tool list of an Owner-delegate agent with every legacy scope equals the
// committed golden, byte for byte, so applying Permitted to agents neither hides nor adds a tool. (The golden changes
// only where an operation's schema changes; U1-F2 re-blessed it once for the whoami role fields.)
func TestOwnerAgentToolsListGolden(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sess := r.client(t, r.token(t, "owner-agent", auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate, auth.ScopeAdmin))
	res, err := sess.ListTools(callCtx(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(res.Tools, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	want, err := os.ReadFile(filepath.Join("testdata", "tools_list.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("admin agent tools/list differs from the read agent golden (-want +got):\n%s", diff)
	}
}

// An agent's whoami carries null role and user (it is an Owner delegate) and an allowed_ops list; the tool's output
// schema must accept that.
func TestAgentWhoamiNullRole(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sess := r.client(t, r.token(t, "who", auth.ScopeRead))
	res, err := sess.CallTool(callCtx(), &mcp.CallToolParams{Name: "whoami"})
	if err != nil || res.IsError {
		t.Fatalf("whoami: %v %+v", err, res)
	}
	m, _ := res.StructuredContent.(map[string]any)
	if v, ok := m["role"]; !ok || v != nil {
		t.Errorf("role = %v (present %v), want null", v, ok)
	}
	if ops, _ := m["allowed_ops"].([]any); len(ops) == 0 {
		t.Errorf("allowed_ops empty: %v", m)
	}
}
