package auth

import "errors"

// OwnerKeyOwner is the owner key of the configured Owner's devices, Subsonic clients and pairing codes (C301.11). Any other
// user's key is the lowercase username; "owner" is a reserved name, so the two cannot collide.
const OwnerKeyOwner = "owner"

// MaxUserDevices is the per-user limit of active devices and of active Subsonic clients for an Editor or Listener
// (Q21 default); the Owner is unlimited.
const MaxUserDevices = 5

// ErrDeviceLimit is returned when a non-owner user already holds MaxUserDevices active devices or Subsonic clients (422 DEVICE_LIMIT).
var ErrDeviceLimit = errors.New("device limit reached")

// OwnerKeyOf is the owner key a device, Subsonic client or pairing code created by p is bound to: "owner" for the Owner's
// own login, the username for an Editor or Listener. Only a human login qualifies (a device or agent never pairs), and the
// answer comes from the server-side principal, never from a request field. ok is false for anyone else, including a human
// with no role (fail closed).
func OwnerKeyOf(p Principal) (key string, ok bool) {
	if p.Kind != KindHuman {
		return "", false
	}
	switch p.Role {
	case RoleOwner:
		return OwnerKeyOwner, true
	case RoleEditor, RoleListener:
		if p.User != "" {
			return p.User, true
		}
	}
	return "", false
}

// SeesAllDevices reports whether p lists and revokes every user's devices and clients: the Owner's own login.
func SeesAllDevices(p Principal) bool { return p.IsOwner() }

// ownerIdentity resolves the Role, User and effective scopes of a device or Subsonic client from its owner key against the
// user map fixed at startup (C301.3, C312.2). The Owner's key keeps today's principal: no role, no user and the stored
// scopes, which makes it an Owner delegate exactly as before. Any other key must name a configured Editor or Listener; a
// key missing from the map (a removed user), an unknown role or a key naming the Owner yields ok=false, which callers
// answer as an invalid credential, never as the Owner. The scopes are { x in ScopesFor(role) : stored.Has(x) }: a device's
// stored scopes never imply generate or admin, so neither is granted.
func ownerIdentity(users map[string]Role, key string, stored []Scope) (role Role, user string, scopes []Scope, ok bool) {
	if key == OwnerKeyOwner {
		return "", "", stored, true
	}
	r, found := users[key]
	if !found || (r != RoleEditor && r != RoleListener) {
		return "", "", nil, false
	}
	probe := Principal{Scopes: stored}
	for _, s := range ScopesFor(r) {
		if probe.Has(s) {
			scopes = append(scopes, s)
		}
	}
	return r, key, scopes, true
}
