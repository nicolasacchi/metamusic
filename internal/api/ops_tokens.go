package api

import (
	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

func registerTokens(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_tokens", Method: "GET", Path: "/api/tokens",
		Summary:     "List API tokens",
		Description: "Token names, prefixes, scopes and last use. Never the tokens themselves.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/human/tokens"},
		stub[empty, tokenListOut]("M2a-B9")) // STUB(M2a-B9)

	Register(a, huma.Operation{
		OperationID: "revoke_own_token", Method: "POST", Path: "/api/tokens/self/revoke",
		Summary:     "Revoke the calling token",
		Description: "Revokes the token that made this call. Needs only read scope so any token can retire itself; other callers get 403 NOT_A_TOKEN.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "none:agent-self-service"},
		stub[empty, tokenOut]("M2a-B9")) // STUB(M2a-B9)

	Register(a, withBody(created(huma.Operation{
		OperationID: "create_token", Method: "POST", Path: "/human/api/tokens",
		Summary:     "Create an API token",
		Description: "Mints a token with the given scopes. The token is shown once. Operator only.",
	}), bodySmall), Meta{Scope: auth.ScopeAdmin, Cost: "free", MCP: "exclude:human-only", UI: "/human/tokens", HumanOnly: true,
		Idempotency: "create", SecretFields: []string{"token"}},
		stub[createTokenIn, createdTokenOut]("M2a-B9")) // STUB(M2a-B9)

	Register(a, huma.Operation{
		OperationID: "revoke_token", Method: "POST", Path: "/human/api/tokens/{id}/revoke",
		Summary:     "Revoke an API token",
		Description: "Revokes any token by id. Repeating the call returns the same result. Operator only.",
	}, Meta{Scope: auth.ScopeAdmin, Cost: "free", MCP: "exclude:human-only", UI: "/human/tokens", HumanOnly: true},
		stub[tokenIDIn, tokenOut]("M2a-B9")) // STUB(M2a-B9)
}
