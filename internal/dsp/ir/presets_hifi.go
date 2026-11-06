package ir

// The two small rooms of the binaural operator (C221.1, C206 machinery): generated reverberation, never a dataset. They are
// kept apart from the four large presets of presetTable so Preset and PresetNames, and everything derived from them (the
// ensemble's space enum), are unchanged.
var roomTable = []struct {
	name string
	p    Params
}{
	{"studio", Params{RT60: 0.27, PredelayMS: 4, Damping: 0.6, SizeM: 8, Width: 0.8}},
	{"hall", Params{RT60: 1.4, PredelayMS: 18, Damping: 0.4, SizeM: 30, Width: 0.9}},
}

// Room returns the named binaural room preset: "studio" (rt60 about 0.3 s) or "hall" (about 1.4 s).
func Room(name string) (Params, bool) {
	for _, e := range roomTable {
		if e.name == name {
			return e.p, true
		}
	}
	return Params{}, false
}

// RoomNames lists the binaural rooms in canonical order.
func RoomNames() []string {
	out := make([]string, len(roomTable))
	for i, e := range roomTable {
		out[i] = e.name
	}
	return out
}
