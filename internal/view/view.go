// Package view is the one place that turns a principal's role into what it may see of the library (C308.1). It keys
// on Role, never on Kind, so a Listener's paired device or Subsonic client gets the Listener view (D-U1-14). Lane V1
// uses HideDiscarded; the redaction flags are filled here for lane V2, which owns the per-type shapers.
package view

import (
	"context"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// Viewer is what a principal may see.
type Viewer struct {
	Role auth.Role
	User string
	// HideDiscarded: discarded takes do not exist for this viewer (404 by id, absent from lists, counts, trees).
	HideDiscarded bool
	SeeCost       bool // money fields (Owner side only)
	SeeMuse       bool // Muse text and structure
	SeePrivate    bool // private notes, recipe internals, other users' identities
}

// For derives the Viewer of p. A principal with no role (agent, connector grant, system, the Owner's device, a bare
// context) is an Owner delegate and sees what it saw before U1.
func For(p auth.Principal) Viewer {
	return Viewer{
		Role:          p.Role,
		User:          p.User,
		HideDiscarded: !p.CanSeePrivate(),
		SeeCost:       p.IsOwnerSide(),
		SeeMuse:       p.CanSeePrivate(),
		SeePrivate:    p.CanSeePrivate(),
	}
}

// FromContext is For(auth.FromContext(ctx)).
func FromContext(ctx context.Context) Viewer { return For(auth.FromContext(ctx)) }

// NotDiscarded is the SQL fragment that hides discarded takes, for a tracks row aliased alias (empty: the bare
// column). It is the empty string for a viewer that sees them, so every query is byte-identical for the Owner.
// The fragment starts with " AND " and has no placeholders.
func (v Viewer) NotDiscarded(alias string) string {
	if !v.HideDiscarded {
		return ""
	}
	if alias != "" {
		alias += "."
	}
	return " AND " + alias + "status <> 'discarded'"
}

// Listener reports whether the viewer is the read-only role (the Listener query rules of C308.3b apply).
func (v Viewer) Listener() bool { return v.Role == auth.RoleListener }

// NotDiscardedBy is the same filter for a table that only carries a track id column (a style term, a tag link, a
// playlist item): " AND col NOT IN (discarded ids)". Empty for a viewer that sees discarded takes.
func (v Viewer) NotDiscardedBy(col string) string {
	if !v.HideDiscarded {
		return ""
	}
	return " AND " + col + " NOT IN (SELECT id FROM tracks WHERE status = 'discarded')"
}
