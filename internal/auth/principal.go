// Package auth implements principals, path classification, API tokens,
// local password auth and the authentication middleware.
package auth

import "context"

type Scope string

const (
	ScopeRead     Scope = "read"
	ScopeWrite    Scope = "write"
	ScopeGenerate Scope = "generate"
	ScopeAdmin    Scope = "admin"
)

var allScopes = []Scope{ScopeRead, ScopeWrite, ScopeGenerate, ScopeAdmin}

type Kind string

const (
	KindAnonymous Kind = "anonymous"
	KindHuman     Kind = "human"
	KindAgent     Kind = "agent"
)

type Principal struct {
	Kind   Kind
	ID     string  // human: username; agent: token id
	Name   string  // human: username; agent: token name
	Scopes []Scope // human: all four
	Via    string  // "proxy" | "local" | "token"
}

func (p Principal) Has(s Scope) bool {
	for _, have := range p.Scopes {
		if have == s {
			return true
		}
	}
	return false
}

type ctxKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the request principal, or an anonymous one when absent.
func FromContext(ctx context.Context) Principal {
	if p, ok := ctx.Value(ctxKey{}).(Principal); ok {
		return p
	}
	return Principal{Kind: KindAnonymous}
}

func humanPrincipal(username, via string) Principal {
	return Principal{Kind: KindHuman, ID: username, Name: username, Scopes: append([]Scope(nil), allScopes...), Via: via}
}
