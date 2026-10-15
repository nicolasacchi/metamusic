package connectors_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

func problemOf(t *testing.T, resp *servertest.Response) (string, map[string]any) {
	t.Helper()
	m, _ := resp.JSON(t).(map[string]any)
	return fmt.Sprint(m["code"]), asMap(m["details"])
}

func dbCount(t *testing.T, r *servertest.Rig, q string, args ...any) int {
	t.Helper()
	var n int
	if err := r.Store.R.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func ntfyCount(r *servertest.Rig, title string) int {
	n := 0
	for _, q := range r.Ntfy.Requests() {
		if strings.Contains(q.Body, title) {
			n++
		}
	}
	return n
}

// TestSetConnectorGrant_Table: the validation, the spend_cap_changes record, revoke and restore, who may call, the
// masking for an agent, the audit row and the push of an admin agent's call.
func TestSetConnectorGrant_Table(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, Canary: true, OIDC: true})
	sess, grant := r.Connector(t, servertest.ConnectorOpts{})
	h := r.Human(t)
	path := "/api/connectors/" + grant

	// validation: scopes without read, admin, unknown ones
	for _, bad := range []any{
		map[string]any{"scopes": []string{"write"}}, map[string]any{"scopes": []string{"read", "admin"}}, map[string]any{"scopes": []string{}},
		map[string]any{}, map[string]any{"limits": map[string]any{"daily_budget_micro_usd": 1}},
	} {
		if resp := h.Do(t, "PUT", path, bad); resp.Status != 422 {
			t.Errorf("%v: %d %s, want 422", bad, resp.Status, resp.Body)
		}
	}
	// the human sees everything; the view is complete
	resp := h.Do(t, "PUT", path, map[string]any{"scopes": []string{"generate", "read"}})
	view := asMap(resp.JSON(t))
	if resp.Status != 200 || cmp.Diff([]any{"read", "generate"}, view["scopes"]) != "" || view["generate_enabled"] != true ||
		view["username"] != servertest.OIDCUser || view["subject_hint"] != servertest.OIDCSubject[:8] || view["label"] != "Claude Test" {
		t.Fatalf("scopes change: %d %s", resp.Status, resp.Body)
	}
	// limits: one spend_cap_changes row with before/after JSON and the connector target
	lim := map[string]any{"daily_budget_micro_usd": 200_000, "auto_approve_takes": 0, "auto_approve_micro_usd": nil}
	resp = h.Do(t, "PUT", path, map[string]any{"limits": lim})
	view = asMap(resp.JSON(t))
	eff := asMap(view["effective"])
	if resp.Status != 200 || asMap(view["limits"])["daily_budget_micro_usd"] != float64(200_000) || asMap(view["limits"])["daily_budget_usd"] != "0.2" ||
		eff["daily_budget_micro_usd"] != float64(200_000) || eff["auto_approve_takes"] != float64(0) || eff["auto_approve_micro_usd"] != float64(spend.DefaultCaps.AutoApproveMicro) {
		t.Fatalf("limits: %d %s", resp.Status, resp.Body)
	}
	var target, before, after, by string
	if err := r.Store.R.QueryRow(`SELECT target, before_json, after_json, principal_id FROM spend_cap_changes WHERE target LIKE 'connector:%'`).Scan(&target, &before, &after, &by); err != nil {
		t.Fatalf("spend_cap_changes: %v", err)
	}
	if target != "connector:"+grant || by != "human:"+"canary-human" ||
		before != `{"daily_budget_micro_usd":null,"auto_approve_takes":null,"auto_approve_micro_usd":null}` ||
		after != `{"daily_budget_micro_usd":200000,"auto_approve_takes":0,"auto_approve_micro_usd":null}` {
		t.Errorf("cap change = %s %s %s %s", target, by, before, after)
	}
	// revoke / restore
	resp = h.Do(t, "PUT", path, map[string]any{"revoked": true})
	view = asMap(resp.JSON(t))
	if resp.Status != 200 || view["revoked_at"] == nil || asMap(view["revoked_by"])["id"] != "human:canary-human" {
		t.Fatalf("revoke: %d %s", resp.Status, resp.Body)
	}
	if n := dbCount(t, r, `SELECT COUNT(*) FROM connector_grants WHERE revoked_by = 'human:canary-human'`); n != 1 {
		t.Errorf("revoked_by rows = %d", n)
	}
	resp = h.Do(t, "PUT", path, map[string]any{"revoked": false})
	if view = asMap(resp.JSON(t)); resp.Status != 200 || view["revoked_at"] != nil || view["revoked_by"] != nil {
		t.Fatalf("restore: %d %s", resp.Status, resp.Body)
	}
	if resp := h.Do(t, "PUT", "/api/connectors/00000000-0000-7000-8000-00000000dead", map[string]any{"revoked": true}); resp.Status != 404 {
		t.Errorf("unknown id: %d %s", resp.Status, resp.Body)
	}

	// who may call: a read-scope agent, the connector itself and a paired device are refused
	reader, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	if resp := reader.Do(t, "PUT", path, map[string]any{"revoked": true}); resp.Status != 403 {
		t.Errorf("read-scope agent: %d %s", resp.Status, resp.Body)
	} else if code, _ := problemOf(t, resp); code != "FORBIDDEN_SCOPE" {
		t.Errorf("read-scope agent code = %s", code)
	}
	if resp := reader.Do(t, "GET", "/api/connectors", nil); resp.Status != 403 {
		t.Errorf("read-scope list: %d", resp.Status)
	}
	if resp := r.Device(t).Do(t, "PUT", path, map[string]any{"revoked": true}); resp.Status != 403 {
		t.Errorf("paired device: %d %s", resp.Status, resp.Body)
	}
	if v, res := generic(t, sess, "set_connector_grant", map[string]any{"id": grant, "revoked": true}); !res.IsError || !strings.Contains(errText(res), "FORBIDDEN_SCOPE") {
		t.Errorf("the connector itself: %v %+v", v, res)
	}
	if n := dbCount(t, r, `SELECT COUNT(*) FROM connector_grants WHERE revoked_at IS NOT NULL`); n != 0 {
		t.Errorf("a refused caller revoked the grant")
	}

	// an admin-scope PAT agent: 200, one audit row, one push, masked view
	admin, adminID := r.Agent(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeAdmin)
	resp = admin.Do(t, "PUT", path, map[string]any{"scopes": []string{"read", "write"}})
	view = asMap(resp.JSON(t))
	if resp.Status != 200 || view["username"] != nil || view["subject_hint"] != nil || view["label"] != "Claude Test" {
		t.Fatalf("admin agent: %d %s", resp.Status, resp.Body)
	}
	if ub := asMap(view["updated_by"]); ub["kind"] != "agent" || ub["name"] != "agent-1" {
		t.Errorf("updated_by = %v", ub)
	}
	if n := dbCount(t, r, `SELECT COUNT(*) FROM audit_log WHERE op = 'set_connector_grant' AND principal_id = ?`, "agent:"+adminID); n != 1 {
		t.Errorf("audit rows of the admin agent's call = %d", n)
	}
	eventually(t, "the Connector grant changed push", func() bool { return ntfyCount(r, "agent-1 changed Claude Test") == 1 })
	for _, q := range r.Ntfy.Requests() {
		if strings.Contains(q.Body, servertest.OIDCUser) || strings.Contains(q.Body, servertest.OIDCSubject) || strings.Contains(q.Body, "canary-human") {
			t.Errorf("a push leaks the username or subject: %s", q.Body)
		}
	}
	// its list is masked too and pushes "Connector grants listed"
	resp = admin.Do(t, "GET", "/api/connectors", nil)
	items := asMap(resp.JSON(t))["items"].([]any)
	if resp.Status != 200 || len(items) != 1 || asMap(items[0])["username"] != nil || asMap(items[0])["subject_hint"] != nil {
		t.Errorf("agent list: %d %s", resp.Status, resp.Body)
	}
	eventually(t, "the Connector grants listed push", func() bool { return ntfyCount(r, "agent-1 listed connector grants") == 1 })
	// a human's own list sends no push
	h.OK(t, "GET", "/api/connectors", nil)
	if n := ntfyCount(r, "Connector grants listed"); n != 1 {
		t.Errorf("list pushes after the human's own list = %d", n)
	}
}

// TestListConnectorGrants_OIDCStatus: disabled => enabled false and issuer null; enabled => clients, resource and a
// key count of at least one after Prefetch with no token verified.
func TestListConnectorGrants_OIDCStatus(t *testing.T) {
	t.Parallel()
	t.Run("disabled", func(t *testing.T) {
		t.Parallel()
		r := servertest.New(t, servertest.Options{Fixture: true})
		st := asMap(asMap(r.Human(t).OK(t, "GET", "/api/connectors", nil))["oidc"])
		if st["enabled"] != false || st["issuer"] != nil || st["alg"] != "RS256" || st["required_scope"] != "metamusic" || st["resource"] != "http://mm.example.com/mcp" {
			t.Errorf("status = %v", st)
		}
		if len(st["clients"].([]any)) != 0 || len(st["scopes_advertised"].([]any)) != 0 || asMap(st["keys"])["count"] != float64(0) {
			t.Errorf("status = %v", st)
		}
	})
	t.Run("enabled", func(t *testing.T) {
		t.Parallel()
		r := servertest.New(t, servertest.Options{Fixture: true, OIDC: true})
		eventually(t, "the JWKS prefetch", func() bool {
			st := asMap(asMap(r.Human(t).OK(t, "GET", "/api/connectors", nil))["oidc"])
			return asMap(st["keys"])["count"].(float64) >= 1
		})
		list := asMap(r.Human(t).OK(t, "GET", "/api/connectors", nil))
		st := asMap(list["oidc"])
		if st["enabled"] != true || st["issuer"] != r.OIDC.URL || st["resource"] != r.URL+"/mcp" {
			t.Errorf("status = %v", st)
		}
		if d := cmp.Diff([]any{map[string]any{"client_id": "test-claude", "label": "Claude Test"}, map[string]any{"client_id": "test-chatgpt", "label": "ChatGPT Test"}}, st["clients"]); d != "" {
			t.Errorf("clients (-want +got):\n%s", d)
		}
		if k := asMap(st["keys"]); k["fetched_at"] == nil || k["last_error"] != nil && k["last_error"] != "" {
			t.Errorf("keys = %v", k)
		}
		if n := len(list["items"].([]any)); n != 0 {
			t.Errorf("no token was verified, yet %d grants exist", n)
		}
	})
}

// TestConnector_GenerateOffByDefault: a connector cannot estimate a variant until the operator adds generate.
func TestConnector_GenerateOffByDefault(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, Backend: "fake", OIDC: true})
	sess, grant := r.Connector(t, servertest.ConnectorOpts{})
	args := map[string]any{"id": lt.TrackLuna, "mode": "new_seed", "n_seeds": 1}
	_, res := toolJSON(t, sess, "estimate_variant", args)
	if !res.IsError || !strings.Contains(errText(res), "FORBIDDEN_SCOPE") || !strings.Contains(errText(res), "/human/connectors") {
		t.Fatalf("estimate without generate: %+v", res)
	}
	if resp := r.Human(t).Do(t, "PUT", "/api/connectors/"+grant, map[string]any{"scopes": []string{"read", "write", "generate"}}); resp.Status != 200 {
		t.Fatalf("set scopes: %d %s", resp.Status, resp.Body)
	}
	if v, res := toolJSON(t, sess, "estimate_variant", args); res.IsError || asMap(v)["confirmation_token"] == nil {
		t.Fatalf("estimate with generate: %+v", res)
	}
}

// TestSetConnectorGrant_AgentCannotExceedGlobalCaps: an admin agent may raise a connector's daily budget to $1000, but
// the global batch ceiling and month still bind, and the global caps stay human-only.
func TestSetConnectorGrant_AgentCannotExceedGlobalCaps(t *testing.T) {
	t.Parallel()
	caps := spend.Caps{MonthlyMicro: 1_000, BatchMicro: 1_000, HumanDailyMicro: 1_000, AgentDailyMicro: 1_000, AutoApproveTakes: 100, AutoApproveMicro: 1_000_000_000}
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, Backend: "fake", OIDC: true, Caps: &caps})
	sess, grant := r.Connector(t, servertest.ConnectorOpts{Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate}})
	admin, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeAdmin)
	resp := admin.Do(t, "PUT", "/api/connectors/"+grant, map[string]any{"limits": map[string]any{
		"daily_budget_micro_usd": 1_000_000_000, "auto_approve_takes": 100, "auto_approve_micro_usd": 1_000_000_000}})
	if resp.Status != 200 {
		t.Fatalf("set limits: %d %s", resp.Status, resp.Body)
	}
	batches := dbCount(t, r, `SELECT COUNT(*) FROM batches`) // the M3 fixture holds some
	est, res := toolJSON(t, sess, "estimate_variant", map[string]any{"id": lt.TrackLuna, "mode": "new_seed", "n_seeds": 1})
	if res.IsError {
		t.Fatalf("estimate: %+v", res.Content)
	}
	_, res = toolJSON(t, sess, "create_variant", map[string]any{"id": lt.TrackLuna, "confirmation_token": asMap(est)["confirmation_token"]})
	text := errText(res)
	if !res.IsError || !strings.Contains(text, "BUDGET_EXCEEDED") || !(strings.Contains(text, `"cap":"batch"`) || strings.Contains(text, `"cap":"monthly"`)) {
		t.Fatalf("create under a $1000 grant daily: %s", text)
	}
	if n := dbCount(t, r, `SELECT COUNT(*) FROM batches`); n != batches {
		t.Errorf("batches = %d, want %d", n, batches)
	}
	// the global caps are human-only for the same admin agent
	resp = admin.Do(t, "PUT", "/human/api/spend/caps", map[string]any{"monthly_micro_usd": 50_000_000})
	if code, _ := problemOf(t, resp); resp.Status != 403 || code != "HUMAN_ONLY" {
		t.Errorf("set_spend_caps as an admin agent: %d %s", resp.Status, resp.Body)
	}
	var monthly int64
	if err := r.Store.R.QueryRow(`SELECT monthly_micro_usd FROM spend_caps`).Scan(&monthly); err != nil || monthly != 1_000 {
		t.Errorf("monthly cap = %d (%v)", monthly, err)
	}
}

// errText is the text of an MCP tool error.
func errText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
