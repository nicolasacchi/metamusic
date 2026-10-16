package connectors_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/oidc/oidctest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// oidcRig is an OIDC rig over the synthetic fixture library with the ntfy canary server (the human is "canary-human",
// the OIDC user e2e is trusted too).
func oidcRig(t *testing.T) *servertest.Rig {
	t.Helper()
	return servertest.New(t, servertest.Options{Fixture: true, Canary: true, OIDC: true})
}

func toolJSON(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) (any, *mcp.CallToolResult) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	b, _ := json.Marshal(res.StructuredContent)
	var v any
	_ = json.Unmarshal(b, &v)
	return v, res
}

func generic(t *testing.T, s *mcp.ClientSession, op string, args map[string]any) (any, *mcp.CallToolResult) {
	t.Helper()
	return toolJSON(t, s, "call_operation", map[string]any{"operation_id": op, "arguments": args})
}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }

func countReq(p *oidctest.Provider, endpoint, grant string) int {
	n := 0
	for _, r := range p.Requests() {
		if r.Endpoint == endpoint && (grant == "" || r.GrantType == grant) {
			n++
		}
	}
	return n
}

// eventually polls for the async ntfy publish.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAcceptance_M4a_HostedConnectorOAuth(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, client, label string }{
		{"claude_public_client", servertest.OIDCClaudeClient, "Claude Test"},
		{"chatgpt_confidential_post", servertest.OIDCChatGPTClient, "ChatGPT Test"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := oidcRig(t)
			sess, grant := r.Connector(t, servertest.ConnectorOpts{Client: c.client})

			// the chain: one authorize with the resource and PKCE-bound scopes, one code exchange
			var authz *oidctest.Recorded
			for _, q := range r.OIDC.Requests() {
				if q.Endpoint == "authorize" {
					q := q
					authz = &q
				}
			}
			if authz == nil || authz.ClientID != c.client || authz.Resource != r.URL+"/mcp" || !strings.Contains(authz.Scope, "metamusic") {
				t.Fatalf("authorize request = %+v", authz)
			}
			if n := countReq(r.OIDC, "authorize", ""); n != 1 {
				t.Errorf("authorize requests = %d, want 1", n)
			}
			if n := countReq(r.OIDC, "token", "authorization_code"); n != 1 {
				t.Errorf("code exchanges = %d, want 1", n)
			}

			// search_tracks equals the human's REST search
			h := r.Human(t)
			want := h.OK(t, "GET", "/api/tracks?q=luna&limit=10", nil)
			got, res := toolJSON(t, sess, "search_tracks", map[string]any{"q": "luna", "limit": 10})
			if res.IsError {
				t.Fatalf("search_tracks: %+v", res.Content)
			}
			if d := cmp.Diff(asMap(want)["items"], asMap(got)["items"]); d != "" {
				t.Errorf("search_tracks items differ from REST (-rest +mcp):\n%s", d)
			}

			// add_comment replays on an identical call: one comment
			args := map[string]any{"id": lt.TrackLuna, "body": "connector note " + c.client}
			first, res := toolJSON(t, sess, "add_comment", args)
			if res.IsError {
				t.Fatalf("add_comment: %+v", res.Content)
			}
			author := asMap(asMap(first)["author"])
			if author["kind"] != "agent" || author["name"] != c.label {
				t.Errorf("author = %v, want {agent %s}", author, c.label)
			}
			second, _ := toolJSON(t, sess, "add_comment", args)
			if d := cmp.Diff(first, second); d != "" {
				t.Errorf("replay differs (-first +second):\n%s", d)
			}
			n := 0
			list := asMap(h.OK(t, "GET", "/api/tracks/"+lt.TrackLuna+"/comments?limit=100", nil))["items"].([]any)
			for _, it := range list {
				if asMap(it)["body"] == args["body"] {
					n++
				}
			}
			if n != 1 {
				t.Errorf("comments with the body = %d, want 1", n)
			}

			// the audit log: rows of agent:<grant> over mcp with the oauth client label
			audit := asMap(h.OK(t, "GET", "/api/audit?principal_id=agent:"+grant+"&limit=100", nil))["items"].([]any)
			var sawComment, sawGrant bool
			for _, it := range audit {
				row := asMap(it)
				switch row["op"] {
				case "add_comment":
					sawComment = true
					if row["transport"] != "mcp" || !strings.HasPrefix(row["client"].(string), "oauth:"+c.client) {
						t.Errorf("audit row = %v", row)
					}
				case "auth:connector_grant":
					sawGrant = true
					if row["transport"] != "oauth" || row["status"] != float64(201) {
						t.Errorf("grant audit row = %v", row)
					}
				}
			}
			if !sawComment || !sawGrant {
				t.Errorf("audit rows: add_comment %v, auth:connector_grant %v in %v", sawComment, sawGrant, audit)
			}

			// the grant: read,write, limits null; ntfy "Connector connected" with the label and neither user nor subject
			list2 := asMap(h.OK(t, "GET", "/api/connectors", nil))["items"].([]any)
			if len(list2) != 1 || asMap(list2[0])["id"] != grant || asMap(list2[0])["client_id"] != c.client ||
				cmp.Diff([]any{"read", "write"}, asMap(list2[0])["scopes"]) != "" || asMap(list2[0])["generate_enabled"] != false {
				t.Errorf("grants = %v", list2)
			}
			eventually(t, "the Connector connected push", func() bool {
				for _, q := range r.Ntfy.Requests() {
					if strings.Contains(q.Body, "Connector connected") {
						return strings.Contains(q.Body, c.label) && !strings.Contains(q.Body, servertest.OIDCUser) && !strings.Contains(q.Body, servertest.OIDCSubject)
					}
				}
				return false
			})
		})
	}
}

const (
	claudeRedirect = "https://claude.ai/api/mcp/auth_callback"
	probeRedirect  = "http://127.0.0.1:8765/callback"
)

// manualConnect connects a go-sdk client the way Rig.Connector does, with a caller-chosen first token (nil => none, so
// the first request authorizes) and no rig-clock token source (these tests never advance the clock past a token).
func manualConnect(t *testing.T, r *servertest.Rig, client, redirect string, initial *oauth2.Token) (*mcp.ClientSession, error) {
	t.Helper()
	cfg := &auth.AuthorizationCodeHandlerConfig{
		PreregisteredClient: &oauthex.ClientCredentials{ClientID: client}, RedirectURL: redirect,
		AuthorizationCodeFetcher: r.OIDC.Fetcher(t), RequestRefreshToken: true, Client: &http.Client{},
	}
	if initial != nil {
		cfg.InitialTokenSource = oauth2.StaticTokenSource(initial)
	}
	h, err := auth.NewAuthorizationCodeHandler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "connector-test-" + client, Version: "0"}, nil)
	sess, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: r.URL + "/mcp", HTTPClient: &http.Client{}, MaxRetries: -1, OAuthHandler: h, DisableStandaloneSSE: true,
	}, nil)
	if err == nil {
		t.Cleanup(func() { _ = sess.Close() })
	}
	return sess, err
}

// rawMCP posts one JSON-RPC initialize to /mcp with the bearer and returns the status, headers and body.
func rawMCP(t *testing.T, r *servertest.Rig, bearer string) (int, http.Header, string) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"raw","version":"0"}}}`
	req, err := http.NewRequest(http.MethodPost, r.URL+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

func whoami(t *testing.T, s *mcp.ClientSession) map[string]any {
	t.Helper()
	v, res := toolJSON(t, s, "whoami", nil)
	if res.IsError {
		t.Fatalf("whoami: %+v", res.Content)
	}
	return asMap(v)
}

func grantRows(t *testing.T, r *servertest.Rig) int {
	t.Helper()
	var n int
	if err := r.Store.R.QueryRow(`SELECT COUNT(*) FROM connector_grants`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAcceptance_M4a_ConnectorTokenLifecycle(t *testing.T) {
	t.Parallel()
	t.Run("refresh_before_expiry", func(t *testing.T) {
		t.Parallel()
		r := oidcRig(t)
		sess, _ := r.Connector(t, servertest.ConnectorOpts{})
		if w := whoami(t, sess); w["via"] != "oauth" {
			t.Fatalf("whoami = %v", w)
		}
		if countReq(r.OIDC, "authorize", "") != 1 || countReq(r.OIDC, "token", "refresh_token") != 0 {
			t.Fatalf("before: authorize %d refresh %d", countReq(r.OIDC, "authorize", ""), countReq(r.OIDC, "token", "refresh_token"))
		}
		// still fresh at exp - 11 s: no refresh
		r.Clock.Advance(time.Hour - 11*time.Second)
		whoami(t, sess)
		if n := countReq(r.OIDC, "token", "refresh_token"); n != 0 {
			t.Fatalf("a token 11 s before expiry was refreshed (%d)", n)
		}
		// past exp - 10 s: exactly one refresh_token grant and no further authorize, and the call succeeds
		r.Clock.Advance(2 * time.Second)
		whoami(t, sess)
		whoami(t, sess)
		if n := countReq(r.OIDC, "token", "refresh_token"); n != 1 {
			t.Errorf("refresh_token grants = %d, want exactly 1", n)
		}
		if n := countReq(r.OIDC, "authorize", ""); n != 1 {
			t.Errorf("authorize requests = %d, want no additional one", n)
		}
	})
	t.Run("missing_scope_steps_up", func(t *testing.T) {
		t.Parallel()
		r := oidcRig(t)
		weak := r.OIDC.Mint(t, servertest.OIDCClaudeClient, servertest.OIDCUser, []string{"openid"}, nil)
		// the bare request: 403 insufficient_scope naming the scope and the metadata
		code, hdr, body := rawMCP(t, r, weak)
		wa := hdr.Get("WWW-Authenticate")
		if code != 403 || !strings.Contains(wa, `error="insufficient_scope"`) || !strings.Contains(wa, `scope="metamusic"`) || !strings.Contains(wa, "resource_metadata=") || !strings.Contains(body, "INSUFFICIENT_SCOPE") {
			t.Fatalf("raw: %d %q %s", code, wa, body)
		}
		if grantRows(t, r) != 0 {
			t.Errorf("a token without the scope created a grant")
		}
		sess, err := manualConnect(t, r, servertest.OIDCClaudeClient, claudeRedirect, &oauth2.Token{AccessToken: weak, TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatalf("connect with a step-up: %v", err)
		}
		if w := whoami(t, sess); w["via"] != "oauth" {
			t.Errorf("whoami = %v", w)
		}
		if n := countReq(r.OIDC, "authorize", ""); n != 1 {
			t.Errorf("authorize requests = %d, want exactly the step-up", n)
		}
	})
	t.Run("other_audience_is_401", func(t *testing.T) {
		t.Parallel()
		r := oidcRig(t)
		tok := r.OIDC.Mint(t, servertest.OIDCClaudeClient, servertest.OIDCUser, []string{"openid", "metamusic"}, []string{"https://other.example.com/mcp"})
		code, hdr, _ := rawMCP(t, r, tok)
		wa := hdr.Get("WWW-Authenticate")
		if code != 401 || !strings.Contains(wa, `error="invalid_token"`) || !strings.Contains(wa, "resource_metadata=") {
			t.Fatalf("%d %q", code, wa)
		}
		if grantRows(t, r) != 0 {
			t.Errorf("a token for another audience created a grant")
		}
	})
	t.Run("unlisted_client_is_403_without_step_up", func(t *testing.T) {
		t.Parallel()
		r := oidcRig(t)
		// the probe client is not allow-listed: the provider issues the token, the server refuses it
		_, err := manualConnect(t, r, servertest.OIDCProbeClient, probeRedirect, nil)
		if err == nil {
			t.Fatal("an unlisted client connected")
		}
		if n := countReq(r.OIDC, "authorize", ""); n > 1 {
			t.Errorf("authorize attempts = %d, want at most 1 and no step-up", n)
		}
		if grantRows(t, r) != 0 {
			t.Errorf("an unlisted client created a grant")
		}
		// even without the scope an unlisted client is CONNECTOR_FORBIDDEN, never insufficient_scope
		tok := r.OIDC.Mint(t, servertest.OIDCProbeClient, servertest.OIDCUser, []string{"openid"}, nil)
		code, hdr, body := rawMCP(t, r, tok)
		if code != 403 || !strings.Contains(body, "CONNECTOR_FORBIDDEN") || hdr.Get("WWW-Authenticate") != "" {
			t.Errorf("%d %q %s", code, hdr.Get("WWW-Authenticate"), body)
		}
	})
	t.Run("untrusted_user_is_403", func(t *testing.T) {
		t.Parallel()
		r := oidcRig(t)
		tok := r.OIDC.Mint(t, servertest.OIDCClaudeClient, "mallory", []string{"openid", "metamusic"}, nil)
		code, hdr, body := rawMCP(t, r, tok)
		if code != 403 || !strings.Contains(body, "CONNECTOR_FORBIDDEN") || hdr.Get("WWW-Authenticate") != "" {
			t.Errorf("%d %q %s", code, hdr.Get("WWW-Authenticate"), body)
		}
		if grantRows(t, r) != 0 {
			t.Errorf("an untrusted user created a grant")
		}
	})
	t.Run("revoked_grant_authorizes_at_most_once", func(t *testing.T) {
		t.Parallel()
		r := oidcRig(t)
		sess, grant := r.Connector(t, servertest.ConnectorOpts{})
		whoami(t, sess)
		h := r.Human(t)
		if resp := h.Do(t, "PUT", "/api/connectors/"+grant, map[string]any{"revoked": true}); resp.Status != 200 {
			t.Fatalf("revoke: %d %s", resp.Status, resp.Body)
		}
		before := countReq(r.OIDC, "authorize", "")
		res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "whoami"})
		if err == nil && !res.IsError {
			t.Fatal("a revoked grant answered a call")
		}
		if n := countReq(r.OIDC, "authorize", "") - before; n > 1 {
			t.Errorf("authorize attempts after the revocation = %d, want at most 1", n)
		}
		resp := h.Do(t, "PUT", "/api/connectors/"+grant, map[string]any{"revoked": false})
		if resp.Status != 200 {
			t.Fatalf("restore: %d %s", resp.Status, resp.Body)
		}
		// go-sdk closes a session whose call was refused with a 403; a new connection to the restored grant works
		sess2, grant2 := r.Connector(t, servertest.ConnectorOpts{})
		if grant2 != grant {
			t.Errorf("a restored grant keeps its id: %s vs %s", grant2, grant)
		}
		whoami(t, sess2)
	})
}

func TestAcceptance_M4a_ConnectorReauthOn401(t *testing.T) {
	t.Parallel()
	r := oidcRig(t)
	sess, _ := r.Connector(t, servertest.ConnectorOpts{})
	whoami(t, sess)
	if n := countReq(r.OIDC, "authorize", ""); n != 1 {
		t.Fatalf("authorize requests = %d", n)
	}
	// the provider rotates its key and unpublishes the old one; the server learns of it when a token of the new key arrives
	r.OIDC.RotateKey()
	r.OIDC.DropOldKeys()
	probe := r.OIDC.Mint(t, servertest.OIDCClaudeClient, servertest.OIDCUser, []string{"openid", "metamusic"}, nil)
	if code, _, body := rawMCP(t, r, probe); code == 401 || code == 403 {
		t.Fatalf("a token of the new key was refused: %d %s", code, body)
	}
	// the client's still-fresh token (old key) is now a 401: at most one re-authorization, then the call succeeds
	if w := whoami(t, sess); w["via"] != "oauth" {
		t.Fatalf("whoami = %v", w)
	}
	if n := countReq(r.OIDC, "authorize", ""); n < 2 || n > 2 {
		t.Errorf("authorize requests = %d, want the first plus at most one re-authorization", n)
	}
	if n := countReq(r.OIDC, "token", "authorization_code"); n != 2 {
		t.Errorf("code exchanges = %d, want 2", n)
	}
}

func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var v map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("%s: %v", url, err)
	}
	return v
}

// TestConnector_RefreshWithoutOfflineAccessInPRM: with the default MM_OIDC_SCOPES the protected-resource metadata lists no
// offline_access; the authorization server's metadata does, so go-sdk adds it to the authorize request, the provider
// answers a refresh token and a later refresh_token grant succeeds.
func TestConnector_RefreshWithoutOfflineAccessInPRM(t *testing.T) {
	t.Parallel()
	r := oidcRig(t)
	prm := getJSON(t, r.URL+"/.well-known/oauth-protected-resource/mcp")
	scopes, _ := prm["scopes_supported"].([]any)
	if len(scopes) == 0 || cmp.Diff([]any{"openid", "metamusic"}, scopes) != "" {
		t.Fatalf("PRM scopes_supported = %v", prm["scopes_supported"])
	}
	as := getJSON(t, r.OIDC.URL+"/.well-known/oauth-authorization-server")
	if !strings.Contains(strings.Join(anyStrings(as["scopes_supported"]), " "), "offline_access") {
		t.Fatalf("the provider does not advertise offline_access: %v", as["scopes_supported"])
	}
	sess, _ := r.Connector(t, servertest.ConnectorOpts{})
	var scope string
	for _, q := range r.OIDC.Requests() {
		if q.Endpoint == "authorize" {
			scope = q.Scope
		}
	}
	if !strings.Contains(" "+scope+" ", " offline_access ") {
		t.Errorf("authorize scope = %q, want offline_access added by the client", scope)
	}
	// the token response carried a refresh token: the rig-clock source can refresh, and the grant succeeds
	r.Clock.Advance(time.Hour)
	whoami(t, sess)
	if n := countReq(r.OIDC, "token", "refresh_token"); n != 1 {
		t.Errorf("refresh_token grants = %d, want 1", n)
	}
	// a second refresh (the rotated token) works as well
	r.Clock.Advance(time.Hour)
	whoami(t, sess)
	if n := countReq(r.OIDC, "token", "refresh_token"); n != 2 {
		t.Errorf("refresh_token grants = %d, want 2", n)
	}
}

func anyStrings(v any) []string {
	var out []string
	for _, e := range v.([]any) {
		out = append(out, e.(string))
	}
	return out
}
