package store

// SetOwner records the Owner's principal key ("human:<name>") that Owner-side listening readers filter by. App wiring
// calls it once at startup, before any request is served; an empty name (local mode, tests with no user map) means there
// is no other user, so every human event is the Owner's.
func (s *Store) SetOwner(name string) {
	if name == "" {
		s.ownerKey = ""
		return
	}
	s.ownerKey = "human:" + name
}

// OwnerEventsSQL is the one filter that selects the Owner side's listen events (C306.7): the Owner's own plays and those of
// devices and Subsonic clients. A Listener's or Editor's plays must not move the Owner's heat, completion, last_played or
// unheard, so no reader spells principal_kind IN ('human','device') itself (archtest TestNoRawListenEventOwnerFilter).
// alias is the listen_events alias ("" for none). The returned arguments are bound where the fragment sits.
//
// Devices: until lane D1 adds devices.owner_key, no Editor or Listener can pair a device or a Subsonic client (pairing
// is OwnerOnly), so every device event is the Owner's. D1 replaces the device clause below, and only it, by a join on the
// owner key; the call sites do not change.
func (s *Store) OwnerEventsSQL(alias string) (string, []any) {
	col := "principal_kind"
	id := "principal_id"
	if alias != "" {
		col, id = alias+".principal_kind", alias+".principal_id"
	}
	if s.ownerKey == "" {
		return col + " IN ('human', 'device')", nil
	}
	return "(" + col + " = 'device' OR (" + col + " = 'human' AND " + id + " = ?))", []any{s.ownerKey}
}
