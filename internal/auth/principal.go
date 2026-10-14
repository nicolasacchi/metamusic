// Package auth implements principals, path classification, API tokens,
// local password auth and the authentication middleware.
package auth

import (
	"context"
	"time"
)

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
	KindDevice    Kind = "device"
	// KindSystem marks CLI actions in audit rows only; the middleware never produces it.
	KindSystem Kind = "system"
)

type Principal struct {
	Kind     Kind
	ID       string    // human: username; agent: token id; device: device id; system: "cli"
	Name     string    // human: username; agent: token name; device: device name
	Scopes   []Scope   // human: all four; device: read, write; agent: token scopes
	Via      string    // "proxy" | "local" | "token" | "device" | "cli"
	StepUpAt time.Time // local sessions only (zero otherwise)
}

// CLI is the principal of command-line actions (audit rows only).
var CLI = Principal{Kind: KindSystem, ID: "cli", Name: "cli", Via: "cli"}

// System is the principal of background actors such as "runner", "reconciler"
// and "job" (audit and ledger rows only; it carries no scopes, M3 D-M3-22).
// Key() is "system:<name>".
func System(name string) Principal {
	return Principal{Kind: KindSystem, ID: name, Name: name, Via: "system"}
}

// Key is the stable identity stored in principal_id columns, rate-limit keys
// and idempotency keys: "human:<id>", "device:<id>", "agent:<id>", "system:cli";
// "" for anonymous.
func (p Principal) Key() string {
	switch p.Kind {
	case KindHuman, KindDevice, KindAgent, KindSystem:
		return string(p.Kind) + ":" + p.ID
	}
	return ""
}

// ActsAsHuman reports whether judgments from p count as the operator's own.
func (p Principal) ActsAsHuman() bool { return p.Kind == KindHuman || p.Kind == KindDevice }

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
