package records

import (
	"encoding/json"
	"strconv"
)

// WhyText is the single why-vocabulary (C122.3): the short reason a Take is the one a slot plays. ChangeText passes the
// event's reason, SlotView.why and liner.Track.Why pass the Song's current Pick source with its stored evidence. A field
// the evidence lacks renders the generic form without it, never an error. Only ids and numbers can reach the text, so no
// Muse text can. The sources that no human judged (newest, stars) end in " · undecided" (D-L2-30).
func WhyText(source string, evidence json.RawMessage) string {
	var ev struct {
		DuelsN *int `json:"duels_n"`
		Stars  *int `json:"stars"`
		Tied   *int `json:"tied"`
	}
	_ = json.Unmarshal(evidence, &ev) // broken or absent evidence leaves the zero value
	switch source {
	case "ladder":
		if ev.DuelsN == nil {
			return "won blind duels"
		}
		if *ev.DuelsN == 1 {
			return "won 1 blind duel"
		}
		return "won " + strconv.Itoa(*ev.DuelsN) + " blind duels"
	case "stars":
		s := "stars"
		if ev.Stars != nil {
			s = strconv.Itoa(*ev.Stars) + "★"
		}
		if ev.Tied != nil && *ev.Tied > 1 {
			s += " (newest of " + strconv.Itoa(*ev.Tied) + " tied)"
		}
		return s + " · undecided"
	case "newest":
		return "newest take · undecided"
	case "pin", "pinned":
		return "pinned as the song's Pick"
	case "unpin":
		return "pin removed"
	case "pin_ineligible":
		return "pinned take discarded, fell back"
	case "pin_restored":
		return "pinned take restored"
	case "initial":
		return "first take"
	case "emptied":
		return "no playable take"
	case ReasonRetired:
		return "follows the merged song"
	case ReasonDuplicate:
		return "duplicate"
	}
	return "" // none, empty or unknown
}
