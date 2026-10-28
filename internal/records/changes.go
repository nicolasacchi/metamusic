package records

import "encoding/json"

// Change is one row of record_changes, the append-only changelog (C122). Titles are never stored: ChangeText splices them in
// at read time through a lookup, and Detail holds ids and numbers only.
type Change struct {
	ID          string          `json:"id"`
	Seq         int             `json:"seq"`
	At          string          `json:"at"`
	Kind        string          `json:"kind"`
	SlotID      *string         `json:"slot_id"`
	Position    *int            `json:"position"`
	FromTakeID  *string         `json:"from_take_id"`
	ToTakeID    *string         `json:"to_take_id"`
	Reason      *string         `json:"reason"`
	PickEventID *string         `json:"pick_event_id"`
	Evidence    json.RawMessage `json:"evidence"`
	Detail      json.RawMessage `json:"detail"`
	Actor       string          `json:"actor"`
	ActorKind   string          `json:"actor_kind"`
	ActorName   string          `json:"actor_name"`
}

// ChangeFilter selects changelog rows. Cursor is the seq of the last item of the previous page (exclusive; the direction
// follows Order); Limit is 1 to MaxChangesPage (default 50).
type ChangeFilter struct {
	RecordID string
	Order    string // "desc" (default) or "asc"
	Cursor   int
	Kinds    []string
	Limit    int
}

// ChangePage is one page of the changelog.
type ChangePage struct {
	Items      []Change
	NextCursor *int
}

// SyncReport is what one Syncer pass did.
type SyncReport struct{ Slots, Rows, Skipped, Followed, Removed int }

// Anomaly is one Verify finding; Severity is "error" or "warn".
type Anomaly struct{ Code, Severity, Subject, Detail string }

// The change kinds of record_changes.kind, in the order of the CHECK list of 0010_curation.sql.
const (
	KindCreated          = "created"
	KindRenamed          = "renamed"
	KindRetexted         = "retexted"
	KindCoverRerolled    = "cover_rerolled"
	KindHidden           = "hidden"
	KindUnhidden         = "unhidden"
	KindSlotAdded        = "slot_added"
	KindSlotRemoved      = "slot_removed"
	KindSlotMoved        = "slot_moved"
	KindSlotSide         = "slot_side"
	KindSlotPinned       = "slot_pinned"
	KindSlotUnpinned     = "slot_unpinned"
	KindSlotUpgraded     = "slot_upgraded"
	KindSlotFilled       = "slot_filled"
	KindSlotEmptied      = "slot_emptied"
	KindSlotSongFollowed = "slot_song_followed"
	KindProposed         = "proposed"
	KindProposalDismiss  = "proposal_dismissed"
	KindPublished        = "published"
	KindUnpublished      = "unpublished"
)

// Kinds lists every change kind (a drift test compares it with the schema).
var Kinds = []string{
	KindCreated, KindRenamed, KindRetexted, KindCoverRerolled, KindHidden, KindUnhidden,
	KindSlotAdded, KindSlotRemoved, KindSlotMoved, KindSlotSide, KindSlotPinned, KindSlotUnpinned,
	KindSlotUpgraded, KindSlotFilled, KindSlotEmptied, KindSlotSongFollowed,
	KindProposed, KindProposalDismiss, KindPublished, KindUnpublished,
}

// SyncerKinds are the kinds only the Syncer writes (C119.2); slot_removed is the Service's, except with reason duplicate.
var SyncerKinds = []string{KindSlotUpgraded, KindSlotFilled, KindSlotEmptied, KindSlotSongFollowed}

// The reasons of record_changes.reason, in the order of the CHECK list. The first nine are the reasons of pick_events.
const (
	ReasonInitial       = "initial"
	ReasonPin           = "pin"
	ReasonUnpin         = "unpin"
	ReasonPinIneligible = "pin_ineligible"
	ReasonPinRestored   = "pin_restored"
	ReasonLadder        = "ladder"
	ReasonStars         = "stars"
	ReasonNewest        = "newest"
	ReasonEmptied       = "emptied"
	ReasonRetired       = "retired"
	ReasonDuplicate     = "duplicate"
)

// Reasons lists every reason (a drift test compares it with the schema).
var Reasons = []string{ReasonInitial, ReasonPin, ReasonUnpin, ReasonPinIneligible, ReasonPinRestored, ReasonLadder,
	ReasonStars, ReasonNewest, ReasonEmptied, ReasonRetired, ReasonDuplicate}
