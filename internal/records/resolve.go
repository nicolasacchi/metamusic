package records

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
)

// Slot is one live slot of a Record, in order (Position is 1-based and derived from records.order_json).
type Slot struct {
	ID, RecordID, SongID string
	Position             int
	Side                 *string
	PinnedTakeID         *string
	PinnedBy, PinnedAt   *string
	SyncedSeq            int
	AddedBy, AddedAt     string
	// SongRetired is true when the slot's Song is retired (the Syncer follows it, D-L2-8). The readers fill it from
	// songs.retired_at; ResolveSlot turns it into the retired_song warning.
	SongRetired bool
}

// SlotMode says whether a slot follows the Song's Pick or holds a pinned Take.
type SlotMode string

// The two modes.
const (
	ModeFloating SlotMode = "floating"
	ModePinned   SlotMode = "pinned"
)

// SlotState says whether the slot resolves to a playable Take.
type SlotState string

// The states: ok, empty (a floating slot whose Song has no Pick) and pin_ineligible (the pinned Take is discarded or absent).
const (
	StateOK            SlotState = "ok"
	StateEmpty         SlotState = "empty"
	StatePinIneligible SlotState = "pin_ineligible"
)

// The slot warnings, in the order ResolveSlot reports them.
const (
	WarnPinIneligible = "pin_ineligible"
	WarnEmpty         = "empty"
	WarnRetiredSong   = "retired_song"
	WarnUndecidedPick = "undecided_pick"
)

// TakeInfo is what resolution needs to know about a Take.
type TakeInfo struct {
	ID, Status string
	DurationS  float64
	Lossy      bool
}

// PickInput is the Song's song_picks row; a nil TakeID means the Song has no Pick.
type PickInput struct {
	TakeID *string
	Source string
}

// ResolvedSlot is a slot together with the Take it plays now.
type ResolvedSlot struct {
	Slot        Slot
	Take        *TakeInfo
	Mode        SlotMode
	State       SlotState
	Pick        PickInput
	PinDiffers  bool // the pinned Take differs from the Song's current Pick
	SongRetired bool
	Warnings    []string
}

// Undecided reports whether a Pick source is one no human judged: the Board's `picked` chip is pinned or ladder, so
// newest and stars are undecided (D-L2-30).
func Undecided(source string) bool { return source == "newest" || source == "stars" }

// ResolveSlot derives the Take a slot plays (C121.1, D-L2-6); pure. A pinned slot keeps its pinned Take even when that Take
// was discarded or is unknown (State pin_ineligible; the Take then carries Status "absent" when no row was found): a Record
// never silently changes a pinned slot. A floating slot plays pickTake, the Take of the Song's current Pick, and is empty
// when the Song has none. pinned and pickTake are the tracks rows of the pinned and the Pick Take (nil when missing).
// The warnings come in a fixed order: pin_ineligible, empty, retired_song, undecided_pick.
func ResolveSlot(s Slot, pinned *TakeInfo, pick PickInput, pickTake *TakeInfo) ResolvedSlot {
	r := ResolvedSlot{Slot: s, Pick: pick, SongRetired: s.SongRetired, State: StateOK, Mode: ModeFloating}
	var pinIneligible, empty, undecided bool
	if s.PinnedTakeID != nil {
		r.Mode = ModePinned
		id := *s.PinnedTakeID
		if pinned == nil {
			r.Take = &TakeInfo{ID: id, Status: "absent"}
		} else {
			t := *pinned
			r.Take = &t
		}
		pinIneligible = pinned == nil || pinned.Status == "discarded"
		r.PinDiffers = pick.TakeID == nil || *pick.TakeID != id
	} else if pick.TakeID == nil || pickTake == nil {
		empty = true
	} else {
		t := *pickTake
		r.Take = &t
		undecided = Undecided(pick.Source)
	}
	switch {
	case pinIneligible:
		r.State = StatePinIneligible
	case empty:
		r.State = StateEmpty
	}
	for _, w := range []struct {
		on   bool
		code string
	}{{pinIneligible, WarnPinIneligible}, {empty, WarnEmpty}, {s.SongRetired, WarnRetiredSong}, {undecided, WarnUndecidedPick}} {
		if w.on {
			r.Warnings = append(r.Warnings, w.code)
		}
	}
	return r
}

// SlotOp is one operation of an edit_record_slots call (C121.3). Op is add, remove, move, reorder, pin, unpin, set_side,
// pin_all or unpin_all; the other fields are read per Op: add takes SongID, an optional TakeID (a pinned add), Position and
// Side; remove, move, pin, unpin and set_side take SlotID; pin takes TakeID; reorder takes the complete new order in SlotIDs.
type SlotOp struct {
	Op                     string
	SongID, SlotID, TakeID string
	Position               int
	Side                   OptString
	SlotIDs                []string
}

// EditResult is what EditSlots returns: the Record row after the edit, the changelog rows it wrote and the warnings
// (slots that pin_all skipped because they hold no Take).
type EditResult struct {
	Record   Record
	Changes  []Change
	Warnings []string
}

// OptString is an optional nullable side letter: a JSON key that is absent leaves Set false, null sets Set and Null, and
// "A" to "D" sets Set and Value. Any other value fails to decode (the API maps that to 422 VALIDATION_FAILED on `side`).
type OptString struct {
	Set, Null bool
	Value     string
}

// Sides are the legal side letters.
var Sides = []string{"A", "B", "C", "D"}

// UnmarshalJSON implements json.Unmarshaler.
func (o *OptString) UnmarshalJSON(b []byte) error {
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		*o = OptString{Set: true, Null: true}
		return nil
	}
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return fmt.Errorf("records: side must be A, B, C, D or null")
	}
	if !slices.Contains(Sides, v) {
		return fmt.Errorf("records: side must be A, B, C, D or null")
	}
	*o = OptString{Set: true, Value: v}
	return nil
}
