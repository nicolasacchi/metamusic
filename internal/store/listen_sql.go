package store

import "strings"

// SetOwner records the Owner's principal key ("human:<name>") that Owner-side listening readers filter by. App wiring
// calls it once at startup, before any request is served; an empty name (local mode, tests with no user map) means there
// is no other user, so every human event is the Owner's.
func (s *Store) SetOwner(name string) {
	if name == "" {
		s.ownerKey = ""
		return
	}
	s.ownerKey = "human:" + strings.ToLower(name)
}

// OwnerEventsSQL is the one filter that selects the Owner side's listen events (C306.7): the Owner's own plays and those of
// devices and Subsonic clients. A Listener's or Editor's plays must not move the Owner's heat, completion, last_played or
// unheard, so no reader spells principal_kind IN ('human','device') itself (archtest TestNoRawListenEventOwnerFilter).
// The principal id is compared case-insensitively: config names are lowercased but events recorded before U1 carry the
// name as the proxy sent it. alias is the listen_events alias ("" for none). The returned arguments are bound where the fragment sits.
//
// Devices (D-U1-14): a device or Subsonic client counts as the Owner's unless its owner_key names another user, so the Owner's
// phones and clients (every row before U1, and any id with no row) count exactly as before and an Editor's or Listener's
// phone never moves the Owner's heat, completion, last_played or unheard. The device principal key is "device:<row id>".
func (s *Store) OwnerEventsSQL(alias string) (string, []any) {
	col := "principal_kind"
	id := "principal_id"
	if alias != "" {
		col, id = alias+".principal_kind", alias+".principal_id"
	}
	if s.ownerKey == "" {
		return "(" + col + " = 'human' OR (" + col + " = 'device' AND " + id + " NOT IN " + otherUsersDevices + "))", nil
	}
	return "((" + col + " = 'device' AND " + id + " NOT IN " + otherUsersDevices + ") OR (" + col + " = 'human' AND lower(" + id + ") = ?))", []any{s.ownerKey}
}

// otherUsersDevices lists the principal keys of the devices and Subsonic clients owned by a user other than the Owner.
const otherUsersDevices = `(SELECT 'device:' || id FROM devices WHERE owner_key <> 'owner'
	UNION ALL SELECT 'device:' || id FROM subsonic_clients WHERE owner_key <> 'owner')`
