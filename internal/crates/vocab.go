// Package crates holds Crates, the working sets of curation: a manual Crate lists Songs or Takes, a smart Crate saves a
// filter (C125). This file is the filter vocabulary of mm.crate-filter/1 as data: the keys of a saved filter are exactly the
// query parameters of the list operation of its subject, minus the parameters that make no sense in a saved question.
package crates

// ValueKind is the JSON type of a filter value.
type ValueKind string

// The value kinds of a filter key.
const (
	KindString  ValueKind = "string"
	KindStrings ValueKind = "string[]" // a JSON array of strings (a repeatable query parameter)
	KindBool    ValueKind = "bool"
	KindInt     ValueKind = "int"
	KindNumber  ValueKind = "number"
	KindInstant ValueKind = "instant" // an RFC 3339 date or date-time string
)

// Key is one key a saved filter may carry.
type Key struct {
	Name string
	Kind ValueKind
	// Enum lists the allowed values of a string or string[] key; empty means free text.
	Enum []string
	// MaxItems bounds a string[] key (0 = unbounded).
	MaxItems int
	// Min and Max bound a numeric key when Ranged.
	Min, Max float64
	Ranged   bool
	// New marks a predicate L2 adds to the shipped list operation (D-L2-13); every other key is a shipped parameter.
	New bool
}

var (
	songStates   = []string{"draft", "rendered", "judged", "picked"}
	trackStatus  = []string{"draft", "to_relisten", "final", "discarded"}
	nonNegative  = func(k Key) Key { k.Min, k.Ranged = 0, true; k.Max = 1e9; return k }
	stars1to5    = func(k Key) Key { k.Min, k.Max, k.Ranged = 1, 5, true; return k }
	newPredicate = func(k Key) Key { k.New = true; return k }
)

// SongKeys is the vocabulary of a Crate whose subject is a Song (the list_songs parameters).
var SongKeys = []Key{
	{Name: "muse_id", Kind: KindString},
	{Name: "no_muse", Kind: KindBool},
	{Name: "angle", Kind: KindString},
	{Name: "state", Kind: KindStrings, Enum: songStates, MaxItems: len(songStates)},
	{Name: "queued", Kind: KindBool},
	{Name: "session_id", Kind: KindString},
	{Name: "sound", Kind: KindStrings, MaxItems: 30},
	{Name: "tempo_mismatch", Kind: KindBool},
	{Name: "has_pick", Kind: KindBool}, // unchanged: a Pick row with a take, any source (D-L2-36)
	{Name: "has_open_proposals", Kind: KindBool},
	{Name: "q", Kind: KindString},
	newPredicate(Key{Name: "pick_decided", Kind: KindBool}),
	newPredicate(Key{Name: "unheard", Kind: KindBool}),
	newPredicate(nonNegative(Key{Name: "min_duration_s", Kind: KindNumber})),
	newPredicate(nonNegative(Key{Name: "max_duration_s", Kind: KindNumber})),
	newPredicate(stars1to5(Key{Name: "min_stars", Kind: KindInt})),
	newPredicate(Key{Name: "unrated", Kind: KindBool}),
	newPredicate(Key{Name: "tag", Kind: KindStrings, MaxItems: 5}),
	newPredicate(Key{Name: "lossy", Kind: KindBool}),
	newPredicate(Key{Name: "recipe_known", Kind: KindBool}),
	newPredicate(Key{Name: "in_record", Kind: KindBool}),
}

// TakeKeys is the vocabulary of a Crate whose subject is a Take (the search_tracks parameters).
var TakeKeys = []Key{
	{Name: "q", Kind: KindString},
	{Name: "status", Kind: KindStrings, Enum: trackStatus, MaxItems: len(trackStatus)},
	{Name: "style", Kind: KindStrings, MaxItems: 5},
	nonNegative(Key{Name: "min_duration_s", Kind: KindNumber}),
	nonNegative(Key{Name: "max_duration_s", Kind: KindNumber}),
	{Name: "recorded_from", Kind: KindInstant},
	{Name: "recorded_to", Kind: KindInstant},
	{Name: "has_score", Kind: KindBool},
	{Name: "truncated", Kind: KindBool},
	stars1to5(Key{Name: "min_stars", Kind: KindInt}),
	{Name: "unrated", Kind: KindBool},
	{Name: "tag", Kind: KindStrings, MaxItems: 5},
	{Name: "has_open_comments", Kind: KindBool},
	{Name: "recipe_known", Kind: KindBool},
	{Name: "lossy", Kind: KindBool},
	{Name: "is_pick", Kind: KindBool},
	{Name: "session_id", Kind: KindString},
	newPredicate(Key{Name: "unheard", Kind: KindBool}),
	newPredicate(Key{Name: "in_record", Kind: KindBool}),
	newPredicate(Key{Name: "sound", Kind: KindStrings, MaxItems: 30}),
}

// NonSaved are the query parameters of the two list operations that a saved filter never carries: paging and ordering,
// visibility switches, single-subject selectors, and crate_id (a Crate never nests another).
var NonSaved = []string{"limit", "cursor", "sort", "include_hidden", "include_archived", "draft_id", "track_id", "song_id", "root_track_id", "crate_id"}

// IsNonSaved reports whether name is a parameter a saved filter must not carry.
func IsNonSaved(name string) bool {
	for _, n := range NonSaved {
		if n == name {
			return true
		}
	}
	return false
}

// Keys returns the vocabulary of a subject ("song" or "take"); nil for any other.
func Keys(subject string) []Key {
	switch subject {
	case "song":
		return SongKeys
	case "take":
		return TakeKeys
	}
	return nil
}

// Lookup finds a key of a subject's vocabulary.
func Lookup(subject, name string) (Key, bool) {
	for _, k := range Keys(subject) {
		if k.Name == name {
			return k, true
		}
	}
	return Key{}, false
}
