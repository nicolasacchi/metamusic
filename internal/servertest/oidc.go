package servertest

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/oidc/oidctest"
)

// OIDC test constants (C117.1): the clients of the stub provider and the one user.
const (
	OIDCClaudeClient  = "test-claude"
	OIDCChatGPTClient = "test-chatgpt"
	OIDCProbeClient   = "test-probe"
	OIDCUser          = "e2e"
	OIDCSubject       = "subject-e2e-0001"
	claudeRedirect    = "https://claude.ai/api/mcp/auth_callback"
	chatgptRedirect   = "https://chatgpt.com/connector_platform_oauth_redirect"
	probeRedirect     = "http://127.0.0.1:8765/callback"
)

// chatgptSecret is the synthetic secret of the confidential client, built at run time so no literal looks like a credential.
func chatgptSecret() string { return "test-chatgpt-" + testKey[:8] }

// startOIDC starts the stub provider on the rig clock and points cfg at it: MM_OIDC_ISSUER, MM_OIDC_CLIENTS (the probe
// client is deliberately not allow-listed) and the trusted user e2e. cfg.PublicURL must already be the rig's origin.
func startOIDC(tb testing.TB, cfg *config.Config, clock *Clock) *oidctest.Provider {
	tb.Helper()
	resource := cfg.PublicURL + "/mcp"
	scopes := []string{"openid", "offline_access", "metamusic"}
	p := oidctest.New(tb, oidctest.Options{
		Clients: []oidctest.Client{
			{ID: OIDCClaudeClient, AuthMethod: "none", RedirectURIs: []string{claudeRedirect}, Audience: []string{resource}, Scopes: scopes},
			{ID: OIDCChatGPTClient, Secret: chatgptSecret(), AuthMethod: "client_secret_post", RedirectURIs: []string{chatgptRedirect}, Audience: []string{resource}, Scopes: scopes},
			{ID: OIDCProbeClient, AuthMethod: "none", RedirectURIs: []string{probeRedirect}, Audience: []string{resource}, Scopes: scopes},
		},
		Users: map[string]string{OIDCUser: OIDCSubject}, LoginAs: OIDCUser, Now: clock.Now, UserClaim: true, IssParam: true,
	})
	cfg.OIDCIssuer = p.URL
	cfg.OIDCClients = []config.OIDCClient{{ID: OIDCClaudeClient, Label: "Claude Test"}, {ID: OIDCChatGPTClient, Label: "ChatGPT Test"}}
	cfg.OIDCScopes = []string{"openid", "metamusic"}
	cfg.TrustedUsers = append(cfg.TrustedUsers, OIDCUser)
	return p
}
