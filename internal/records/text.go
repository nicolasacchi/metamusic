package records

import (
	"encoding/json"
	"strconv"
)

// ChangeText renders one changelog row as the sentence the studio and get_record_changes show (C122.3); pure and
// deterministic. names looks up a Take's title at read time (titles are never stored); a nil or empty answer reads "a take".
// A row that lacks a field renders the generic form without it. The detail shapes it reads: slot_moved {"from": n},
// slot_side {"side": "A"|null}.
func ChangeText(c Change, names func(takeID string) string) string {
	title := func(id *string) string {
		if id == nil || names == nil {
			return "a take"
		}
		if t := names(*id); t != "" {
			return t
		}
		return "a take"
	}
	slot := "Slot"
	if c.Position != nil {
		slot += " " + strconv.Itoa(*c.Position)
	}
	why := ""
	if c.Reason != nil {
		why = WhyText(*c.Reason, c.Evidence)
	}
	withWhy := func(s string) string {
		if why == "" {
			return s
		}
		return s + " (" + why + ")"
	}
	switch c.Kind {
	case KindCreated:
		return "Record created"
	case KindRenamed:
		return "Title changed"
	case KindRetexted:
		return "Notes changed"
	case KindCoverRerolled:
		return "Cover re-rolled"
	case KindHidden:
		return "Hidden"
	case KindUnhidden:
		return "Unhidden"
	case KindProposed:
		return "Proposed for publishing"
	case KindProposalDismiss:
		return "Sent back to draft"
	case KindPublished:
		return "Published"
	case KindUnpublished:
		return "Unpublished"
	case KindSlotAdded:
		if c.ToTakeID == nil {
			return slot + " added (no take yet)"
		}
		return slot + " added: " + title(c.ToTakeID)
	case KindSlotRemoved:
		if c.FromTakeID == nil {
			return slot + " removed"
		}
		return slot + " removed (" + title(c.FromTakeID) + ")"
	case KindSlotMoved:
		var d struct {
			From *int `json:"from"`
		}
		_ = json.Unmarshal(c.Detail, &d)
		to := ""
		if c.Position != nil {
			to = strconv.Itoa(*c.Position)
		}
		switch {
		case d.From != nil && to != "":
			return "Slot moved " + strconv.Itoa(*d.From) + " → " + to
		case to != "":
			return "Slot moved to " + to
		}
		return "Slot moved"
	case KindSlotSide:
		var d struct {
			Side json.RawMessage `json:"side"`
		}
		_ = json.Unmarshal(c.Detail, &d)
		var side *string
		if json.Unmarshal(d.Side, &side) != nil || len(d.Side) == 0 {
			return slot + " side changed"
		}
		if side == nil {
			return slot + " side cleared"
		}
		return slot + " side " + *side
	case KindSlotPinned:
		return slot + " pinned to " + title(c.ToTakeID)
	case KindSlotUnpinned:
		if c.ToTakeID == nil {
			return slot + " unpinned: follows the Pick (no take yet)"
		}
		return slot + " unpinned: follows the Pick (" + title(c.ToTakeID) + ")"
	case KindSlotFilled:
		return withWhy(slot + " filled: " + title(c.ToTakeID))
	case KindSlotEmptied:
		if c.FromTakeID == nil {
			return slot + " emptied: no playable take"
		}
		return slot + " emptied: no playable take (was " + title(c.FromTakeID) + ")"
	case KindSlotSongFollowed:
		return slot + " follows the merged song"
	case KindSlotUpgraded:
		return withWhy(slot + " upgraded: " + title(c.FromTakeID) + " → " + title(c.ToTakeID))
	}
	return "Record changed"
}
