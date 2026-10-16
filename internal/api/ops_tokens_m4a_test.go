package api_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// A hosted connector is an agent principal whose ID is a grant id, not a token id: revoke_own_token must
// refuse it before it can reach Tokens.Revoke (C104).
func TestRevokeOwnToken_RefusesOAuthPrincipal(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	oauth := auth.Principal{Kind: auth.KindAgent, ID: "grant-1", Name: "Claude", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "oauth", Client: "test-claude"}
	r.problem(t, oauth, "POST", "/api/tokens/self/revoke", "", 403, "NOT_A_TOKEN")
	sub := auth.Principal{Kind: auth.KindDevice, ID: "c1", Name: "Amperfy", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "subsonic", Client: "amperfy"}
	r.problem(t, sub, "POST", "/api/tokens/self/revoke", "", 403, "NOT_A_TOKEN")
	// a token principal gets past the check (this rig has no token store: 503, not 403)
	tok := auth.Principal{Kind: auth.KindAgent, ID: "tok-1", Name: "t", Scopes: []auth.Scope{auth.ScopeRead}, Via: "token"}
	r.problem(t, tok, "POST", "/api/tokens/self/revoke", "", 503, "UNAVAILABLE")
}
