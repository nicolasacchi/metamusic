// Package auth implements principals, path classification, API tokens,
// local password auth and the authentication middleware.
package auth

import (
	"context"
	"time"

	"github.com/nicolasacchi/metamusic/internal/config"
)

// Role is a user's role (D-U1-1); it is defined in config (which auth imports) and aliased here.
type Role = config.Role

const (
	RoleOwner    = config.RoleOwner
	RoleEditor   = config.RoleEditor
	RoleListener = config.RoleListener
)

type Scope string

const (
	// ScopeListen and ScopeRate are U1's narrower browse and rate scopes: Has(listen) holds for listen or read, Has(rate)
	// for rate or write. Tokens and connector grants carry only the four legacy scopes below.
	ScopeListen   Scope = "listen"
	ScopeRate     Scope = "rate"
	ScopeRead     Scope = "read"
	ScopeWrite    Scope = "write"
	ScopeGenerate Scope = "generate"
	ScopeAdmin    Scope = "admin"
)

var allScopes = []Scope{ScopeRead, ScopeWrite, ScopeGenerate, ScopeAdmin}

// ScopesFor is the scope set of a role (C301.6). The returned slice is fresh.
func ScopesFor(r Role) []Scope {
	switch r {
	case RoleOwner:
		return []Scope{ScopeListen, ScopeRead, ScopeRate, ScopeWrite, ScopeGenerate, ScopeAdmin}
	case RoleEditor:
		return []Scope{ScopeListen, ScopeRead, ScopeRate, ScopeWrite, ScopeGenerate}
	case RoleListener:
		return []Scope{ScopeListen, ScopeRate}
	}
	return nil
}

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
	Kind Kind
	ID   string // human: username; agent: token id; device: device id; system: "cli"
	Name string // human: username; agent: token name; device: device name
	// Role is set for human principals (from the configured user map, never stored) and, from lane D1, inherited by a
	// device from its owner; it is empty for agents, connector grants and system, which are Owner delegates (D-U1-6).
	Role     Role
	User     string    // the lowercase username of a human (equals ID); a device's owner (lane D1)
	Scopes   []Scope   // human: ScopesFor(Role); device: read, write; agent: token scopes
	Via      string    // "proxy" | "local" | "token" | "device" | "oauth" | "subsonic" | "cli"
	StepUpAt time.Time // local sessions, and proxy requests when the app-side step-up is on (zero otherwise)
	// StepUpGate marks a proxy principal whose /human/ access the app gates itself (MM_STEPUP_OIDC_CLIENT set).
	StepUpGate bool
	// Client: Via "oauth" ⇒ the OAuth client_id; Via "subsonic" ⇒ the sanitised `c` parameter; else "".
	Client string
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

// IsPerson reports whether p is a person's own session or a paired device: what ActsAsHuman meant before U1. It says
// nothing about the person's role; use IsOwner, IsOwnerSide or CanSeePrivate for privileges (C303).
func (p Principal) IsPerson() bool { return p.Kind == KindHuman || p.Kind == KindDevice }

// IsHuman reports whether p is a person's own login session (not a paired device, an agent or system). Used where the
// rule is about the session kind itself, such as `human_only` operations.
func (p Principal) IsHuman() bool { return p.Kind == KindHuman }

// IsOwner reports whether p is the Owner's own human session.
func (p Principal) IsOwner() bool { return p.Kind == KindHuman && p.Role == RoleOwner }

// IsOwnerSide reports whether p is not an Editor or a Listener, whatever its kind: the Owner, the Owner's devices,
// agents, connector grants and system. Service guards use it where a device or agent legitimately acts (D-U1-7).
func (p Principal) IsOwnerSide() bool { return p.Role != RoleEditor && p.Role != RoleListener }

// CanSeePrivate reports whether p may read private notes, Muse text and draft reference text: any role but Listener.
// Callers that must also exclude agents combine it with IsPerson.
func (p Principal) CanSeePrivate() bool { return p.Role != RoleListener }

// IsAgent reports whether p is an agent (API token, connector grant or stdio). Rules that mean "the agent" use it;
// "not an agent" is never a synonym for the operator once Editors exist (C303).
func (p Principal) IsAgent() bool { return p.Kind == KindAgent }

// Has reports whether p holds scope s, with the U1 implication: read implies listen and write implies rate.
func (p Principal) Has(s Scope) bool {
	for _, have := range p.Scopes {
		if have == s || (s == ScopeListen && have == ScopeRead) || (s == ScopeRate && have == ScopeWrite) {
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

func humanPrincipal(username string, role Role, via string) Principal {
	return Principal{Kind: KindHuman, ID: username, Name: username, User: username, Role: role, Scopes: ScopesFor(role), Via: via}
}

// RaterKey is the rater a principal's ratings attribute to (C301.7): "owner" for the Owner and the Owner's devices and
// every other Owner-side person, the username for other humans, "critic" for agents, "" for anyone who cannot rate.
func RaterKey(p Principal) string {
	switch {
	case p.IsAgent():
		return "critic"
	case p.Kind == KindHuman && p.Role != RoleOwner && p.User != "":
		return p.User
	case p.IsPerson(): // lane D1 hand-off: a device has no role yet; D1 must return the device owner key here
		return "owner"
	}
	return ""
}
