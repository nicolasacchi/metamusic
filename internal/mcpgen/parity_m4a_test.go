package mcpgen_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/connectors"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// m4aCases covers the eight M4a operations (C113, C117.2): all are MCP generic. The three read ops and the five
// admin ones run on the admin-scope PAT of every face. Until the owning lane replaces a stub, the REST answer is
// 501 NOT_IMPLEMENTED with details.task = "M4a-<task>" and the runner skips the case by name (m4aStubTask); B3's
// TestNoPendingM4aStubs turns the skip into a failure. The management cases mask ids, timestamps, keys and the
// one-time secrets; the owning lane tunes the masks when it fills the handler.
var m4aCases = []ParityCase{
	{Op: "get_offline_set", Masks: []string{"updated_at"}},
	{Op: "get_download_link", Args: fixedArgs(map[string]any{"id": lt.TrackLuna})},
	{Op: "get_prompt", Variant: "critique", Args: fixedArgs(map[string]any{"name": "critique_track", "track_id": lt.TrackLuna})},
	{Op: "list_connector_grants", Masks: []string{"id", "created_at", "updated_at", "last_used_at", "updated_by", "fetched_at", "keys"}},
	{Op: "set_connector_grant", Args: fixedArgs(map[string]any{"id": lt.ConnectorGrantID, "scopes": []string{"read", "write", "generate"}}), SkipGeneric: true,
		Masks: []string{"id", "created_at", "updated_at", "last_used_at", "updated_by", "revoked_by"}},
	{Op: "list_subsonic_clients", Masks: []string{"id", "created_at", "created_by", "last_seen_at", "api_key_prefix"}},
	{Op: "create_subsonic_client", Args: fixedArgs(map[string]any{"name": "Parity phone"}), SkipGeneric: true,
		Masks: []string{"id", "created_at", "created_by", "api_key", "password", "api_key_prefix", "client"}},
	{Op: "revoke_subsonic_client", Args: fixedArgs(map[string]any{"id": lt.SubsonicClientID}), SkipGeneric: true,
		Masks: []string{"id", "created_at", "created_by", "revoked_at", "api_key_prefix"}},
}

func init() {
	parityCases = append(parityCases, m4aCases...)
}

// m4aStubTask returns the task a REST body names when it is an M4a stub's 501 NOT_IMPLEMENTED problem, else "".
func m4aStubTask(status int, body []byte) string {
	if status != 501 {
		return ""
	}
	var p struct {
		Code    string `json:"code"`
		Details struct {
			Task string `json:"task"`
		} `json:"details"`
	}
	if json.Unmarshal(body, &p) != nil || p.Code != "NOT_IMPLEMENTED" || !strings.HasPrefix(p.Details.Task, "M4a-") {
		return ""
	}
	return p.Details.Task
}

// TestConnectorOps_ScriptedAndParity: the two connector operations have a parity case and answer on every face; they are
// no longer a stub (the pending skip of B2 does not apply to them).
func TestConnectorOps_ScriptedAndParity(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, Files: true, M2c: true, M3: true, M4a: true, Backend: "fake"})
	admin, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeAdmin)
	for _, id := range []string{"list_connector_grants", "set_connector_grant"} {
		found := false
		for _, c := range parityCases {
			found = found || c.Op == id
		}
		if !found {
			t.Errorf("no parity case for %s", id)
		}
	}
	if resp := admin.Do(t, "GET", "/api/connectors", nil); resp.Status != 200 || m4aStubTask(resp.Status, resp.Body) != "" {
		t.Errorf("list_connector_grants: %d %s", resp.Status, resp.Body)
	}
	if resp := admin.Do(t, "PUT", "/api/connectors/"+lt.ConnectorGrantID, map[string]any{"revoked": false}); resp.Status != 200 {
		t.Errorf("set_connector_grant: %d %s", resp.Status, resp.Body)
	}
}

// oauthSubset is the fixed list the fourth face runs (C117.2): REST on a read+write PAT against MCP over OAuth.
var oauthSubset = []struct {
	op      string
	args    map[string]any
	masks   []string
	generic bool
	status  int // an expected failure, the same on both faces
}{
	{op: "whoami", masks: []string{"id", "name", "via", "client"}},
	{op: "search_tracks", args: map[string]any{"q": "luna", "limit": 10}},
	{op: "get_track_context", args: map[string]any{"id": lt.TrackLuna}, masks: []string{"chars"}}, // chars counts the URLs, whose origins differ in length
	{op: "add_comment", args: map[string]any{"id": lt.TrackLuna, "body": "second chorus drags", "kind": "fix", "at_second": 5.04}, masks: []string{"id", "author", "source"}},
	{op: "get_offline_set", masks: []string{"updated_at"}, generic: true},
	{op: "estimate_variant", args: map[string]any{"id": lt.TrackLuna, "mode": "new_seed", "n_seeds": 1}, status: 403},
}

// rebase replaces from with to in every string of v (the OAuth rig serves on its own loopback origin).
func rebase(t *testing.T, v any, from, to string) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal([]byte(strings.ReplaceAll(string(b), from, to)), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// dropHint removes the connector-only details.hint of a FORBIDDEN_SCOPE problem (REST on a PAT has none).
func dropHint(v any) any {
	if m, ok := v.(map[string]any); ok {
		if d, ok := m["details"].(map[string]any); ok {
			delete(d, "hint")
		}
	}
	return v
}

// TestParity_OAuthConnector: the fourth face. Rig D is an OIDC rig; its grant is created outside the id sequence
// (the first token finds it: no id consumed, no audit row, no push) and an OAuth connector session runs a fixed subset.
// Each normalised response equals REST on rig A with the masks above.
func TestParity_OAuthConnector(t *testing.T) {
	t.Parallel()
	opts := servertest.Options{Fixture: true, Files: true, M2c: true, M3: true, M4a: true, Backend: "fake", M2b: servertest.M2bOptions{Fixture: true}}
	ra := servertest.New(t, opts)
	od := opts
	od.OIDC = true
	rd := servertest.New(t, od)
	restClient, _ := ra.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	_, _ = rd.Agent(t, auth.ScopeRead, auth.ScopeWrite) // D runs A's setup steps (the PAT it never uses) so the id sequences stay aligned

	const grantID = "00000000-0000-7000-8000-0000000000d4"
	cs := connectors.NewStore(rd.Store, rd.Clock.Now, func() string { return grantID })
	g, created, err := cs.Ensure(context.Background(), servertest.OIDCClaudeClient, servertest.OIDCSubject, servertest.OIDCUser, "Claude Test")
	if err != nil || !created || g.ID != grantID {
		t.Fatalf("pre-created grant: %+v created=%v err=%v", g, created, err)
	}
	if _, err := cs.Set(context.Background(), auth.Principal{Kind: auth.KindSystem, ID: "test", Name: "test", Via: "system"}, grantID,
		connectors.SetIn{Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}); err != nil {
		t.Fatal(err)
	}
	sess, id := rd.Connector(t, servertest.ConnectorOpts{})
	if id != grantID {
		t.Fatalf("connector grant = %s, want the pre-created one", id)
	}
	ops := map[string]api.OpInfo{}
	for _, op := range api.Operations(ra.API) {
		ops[op.ID] = op
	}
	for _, c := range oauthSubset {
		op, ok := ops[c.op]
		if !ok {
			t.Fatalf("unknown operation %s", c.op)
		}
		path, body, headers := restRequest(t, ra.API, op, c.args)
		if op.Meta.Idempotency == "create" {
			headers = append(headers, "Idempotency-Key", "oauth-subset-"+c.op)
		}
		resp := restClient.Do(t, op.Method, path, body, headers...)
		if task := m4aStubTask(resp.Status, resp.Body); task != "" { // another lane's pending stub: skipped by name like runParity
			t.Logf("oauth subset %s: skipped by name, stub of %s", c.op, task)
			continue
		}
		if c.status != 0 && resp.Status != c.status || c.status == 0 && resp.Status/100 != 2 {
			t.Fatalf("%s: REST status %d: %s", c.op, resp.Status, resp.Body)
		}
		want := servertest.Normalize(rebase(t, resp.JSON(t), ra.URL, "http://mm.example.com"), c.masks...)
		res, name := mcpRaw(t, sess, op, c.args, c.generic)
		var got any
		if c.status != 0 {
			if !res.IsError {
				t.Fatalf("%s: expected a tool error, got %+v", c.op, res)
			}
			var v any
			if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &v); err != nil {
				t.Fatalf("%s: error text is not JSON: %v", c.op, err)
			}
			if !strings.Contains(fmt.Sprint(v), "/human/connectors") {
				t.Errorf("%s: the error lacks the /human/connectors hint: %v", c.op, v)
			}
			got = servertest.Normalize(dropHint(v), c.masks...)
		} else {
			got = normalizeResult(t, res, op, name, c.masks, false)
		}
		got = rebase(t, got, rd.URL, "http://mm.example.com")
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("%s: REST (PAT) vs MCP (OAuth connector) mismatch (-rest +oauth):\n%s", c.op, diff)
		}
	}
	// the connector's whoami says so, and its own caps view lists exactly its grant
	who, _ := mcpRaw(t, sess, ops["whoami"], nil, false)
	w := rawResult(t, who)
	if w["via"] != "oauth" || w["kind"] != "agent" || w["name"] != "Claude Test" {
		t.Errorf("whoami = %v", w)
	}
	bud, _ := mcpRaw(t, sess, ops["get_budget"], nil, true)
	if cc, _ := asAnyMap(rawResult(t, bud)["caps"])["connectors"].([]any); len(cc) != 1 || asAnyMap(cc[0])["grant_id"] != grantID {
		t.Errorf("get_budget caps.connectors = %v", cc)
	}
	// the grant was found, not created: no extra audit row came from the verifier
	var auditRows int
	if err := rd.Store.R.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE op = 'auth:connector_grant'`).Scan(&auditRows); err != nil || auditRows != 0 {
		t.Errorf("auth:connector_grant rows = %d (%v)", auditRows, err)
	}
}

func asAnyMap(v any) map[string]any { m, _ := v.(map[string]any); return m }
