package api

import (
	"context"
	"errors"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/notify"
)

func tokenInfo(ti auth.TokenInfo) TokenInfo {
	out := TokenInfo{ID: ti.ID, Name: ti.Name, Prefix: ti.Prefix, CreatedAt: ti.CreatedAt, Scopes: make([]string, len(ti.Scopes))}
	for i, s := range ti.Scopes {
		out.Scopes[i] = string(s)
	}
	if ti.LastUsedAt != "" {
		out.LastUsedAt = &ti.LastUsedAt
	}
	if ti.RevokedAt != "" {
		out.RevokedAt = &ti.RevokedAt
	}
	if ti.ExpiresAt != "" {
		out.ExpiresAt = &ti.ExpiresAt
	}
	return out
}

func tokenErr(err error) error {
	switch {
	case errors.Is(err, auth.ErrNotFound):
		return Err(404, "NOT_FOUND", "no such token", nil)
	case errors.Is(err, auth.ErrInvalidScope):
		return Err(422, "VALIDATION_FAILED", "invalid token name or scopes", nil)
	}
	return err
}

func registerTokens(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "list_tokens", Method: "GET", Path: "/api/tokens",
		Summary:     "List API tokens",
		Description: "Token names, prefixes, scopes, expiry and last use. Never the tokens themselves.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/human/tokens"},
		func(ctx context.Context, _ *empty) (*tokenListOut, error) {
			if d.Tokens == nil {
				return nil, unavailable("tokens")
			}
			list, err := d.Tokens.List(ctx)
			if err != nil {
				return nil, err
			}
			out := &tokenListOut{}
			out.Body.Items = make([]TokenInfo, len(list))
			for i, ti := range list {
				out.Body.Items[i] = tokenInfo(ti)
			}
			return out, nil
		})

	Register(a, huma.Operation{
		OperationID: "revoke_own_token", Method: "POST", Path: "/api/tokens/self/revoke",
		Summary:     "Revoke the calling token",
		Description: "Revokes the token that made this call. Needs only read scope so any token can retire itself; other callers get 403 NOT_A_TOKEN.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "none:agent-self-service"},
		func(ctx context.Context, _ *empty) (*tokenOut, error) {
			p := auth.FromContext(ctx)
			if p.Kind != auth.KindAgent || p.Via != "token" {
				return nil, Err(403, "NOT_A_TOKEN", "this call was not made with an API token", nil)
			}
			if d.Tokens == nil {
				return nil, unavailable("tokens")
			}
			if err := d.Tokens.Revoke(ctx, p.ID); err != nil {
				return nil, tokenErr(err)
			}
			ti, err := d.Tokens.Get(ctx, p.ID)
			if err != nil {
				return nil, tokenErr(err)
			}
			return &tokenOut{Body: tokenInfo(ti)}, nil
		})

	Register(a, withBody(created(huma.Operation{
		OperationID: "create_token", Method: "POST", Path: "/human/api/tokens",
		Summary:     "Create an API token",
		Description: "Mints a token with the given scopes. It expires after ttl_days (default 90; 0 = never). The token is shown once. Operator only.",
	}), bodySmall), Meta{Scope: auth.ScopeAdmin, Cost: "free", MCP: "exclude:human-only", UI: "/human/tokens", HumanOnly: true,
		Idempotency: "create", SecretFields: []string{"token"}},
		func(ctx context.Context, in *createTokenIn) (*createdTokenOut, error) {
			if d.Tokens == nil {
				return nil, unavailable("tokens")
			}
			scopes := make([]auth.Scope, len(in.Body.Scopes))
			for i, s := range in.Body.Scopes {
				scopes[i] = auth.Scope(s)
			}
			ttl := auth.DefaultTokenTTL
			if in.Body.TTLDays != nil {
				ttl = time.Duration(*in.Body.TTLDays) * 24 * time.Hour
			}
			plain, id, err := d.Tokens.CreateTTL(ctx, in.Body.Name, scopes, ttl)
			if err != nil {
				return nil, tokenErr(err)
			}
			ti, err := d.Tokens.Get(ctx, id)
			if err != nil {
				return nil, tokenErr(err)
			}
			if d.Notify != nil {
				_ = d.Notify.Notify(context.WithoutCancel(ctx), notify.TokenCreated(ti.Name, in.Body.Scopes))
			}
			return &createdTokenOut{Body: CreatedToken{TokenInfo: tokenInfo(ti), Token: &plain}}, nil
		})

	Register(a, huma.Operation{
		OperationID: "revoke_token", Method: "POST", Path: "/human/api/tokens/{id}/revoke",
		Summary:     "Revoke an API token",
		Description: "Revokes any token by id. Repeating the call returns the same result. Operator only.",
	}, Meta{Scope: auth.ScopeAdmin, Cost: "free", MCP: "exclude:human-only", UI: "/human/tokens", HumanOnly: true},
		func(ctx context.Context, in *tokenIDIn) (*tokenOut, error) {
			if d.Tokens == nil {
				return nil, unavailable("tokens")
			}
			if err := d.Tokens.Revoke(ctx, in.ID); err != nil {
				return nil, tokenErr(err)
			}
			ti, err := d.Tokens.Get(ctx, in.ID)
			if err != nil {
				return nil, tokenErr(err)
			}
			return &tokenOut{Body: tokenInfo(ti)}, nil
		})
}
