package nextactions

import "fmt"

// The texts of the rules (C139.2). A text is a pure function of the candidate and the names; it never contains a raw id except
// the Muse id tail of Names.Muse, and it is never persisted or sent to a notification (C144.2). Muse is named by Names.Muse,
// which returns the whole noun phrase ("Muse 0000a1b2") so that one template serves the id variant and, once D-L3-16 is
// approved, the title variant.

func textRecordProposed(c Candidate, n Names) string {
	agent := evidenceString(c, "_agent")
	if agent == "" {
		agent = "an agent"
	}
	return fmt.Sprintf("Record \"%s\" was proposed by %s: review it and publish", n.Record(subjectID(c.Subject)), agent)
}

func textMuseUnrendered(c Candidate, n Names) string {
	return fmt.Sprintf("%s has %d Song(s), none rendered — estimate the first (the estimate is free)", n.Muse(subjectID(c.Subject)), evidenceInt(c, "songs"))
}

func textTaxonomyOpen(c Candidate, _ Names) string {
	return fmt.Sprintf("%d taxonomy proposal(s) wait for your decision", evidenceInt(c, "open"))
}

func textMuseUnjudged(c Candidate, n Names) string {
	return fmt.Sprintf("%s has %d rendered Song(s) nobody has judged — ask the human to listen, or add a critique (add_comment)", n.Muse(subjectID(c.Subject)), evidenceInt(c, "rendered"))
}

// viewerNames is implemented by the engine's Names: a text that differs by audience asks it. A Names without it (a test double)
// reads as an agent's.
type viewerNames interface{ ViewerIsHuman() bool }

func textRecordUndecided(c Candidate, n Names) string {
	return fmt.Sprintf("Record \"%s\": %d slot(s) play an undecided Pick — duel them, pin them, or freeze the Record (pin_all)",
		n.Record(subjectID(c.Subject)), evidenceInt(c, "slots"))
}

func textMuseRecordCandidate(c Candidate, n Names) string {
	return fmt.Sprintf("%s has %d Songs rated 4★+ that are in no Record — build one: create_record, then propose_record with a sequence (arc)",
		n.Muse(subjectID(c.Subject)), evidenceInt(c, "songs"))
}

func textSongDuel(c Candidate, n Names) string {
	return fmt.Sprintf("Song \"%s\" has %d takes and %d blind duel(s) — 3 decide the Pick; start one between the two newest",
		n.Song(subjectID(c.Subject)), evidenceInt(c, "takes"), evidenceInt(c, "duels"))
}

func textMusePartialAngles(c Candidate, n Names) string {
	return fmt.Sprintf("%s: %s rendered, %s still draft — estimate %d more", n.Muse(subjectID(c.Subject)),
		angleList(c, "rendered"), angleList(c, "draft"), evidenceInt(c, "drafts"))
}

func textRecordEmptySlot(c Candidate, n Names) string {
	return fmt.Sprintf("Record \"%s\" slot %d has no take yet — render the Song or remove the slot", n.Record(subjectID(c.Subject)), evidenceInt(c, "slot"))
}

// textSongProposals reads "N agent proposal(s) wait" to a human and "your proposal is waiting" to an agent.
func textSongProposals(c Candidate, n Names) string {
	title := n.Song(subjectID(c.Subject))
	if v, ok := n.(viewerNames); ok && v.ViewerIsHuman() {
		return fmt.Sprintf("Song \"%s\": %d agent proposal(s) wait — accept or dismiss", title, evidenceInt(c, "proposals"))
	}
	return fmt.Sprintf("Song \"%s\": your proposal is waiting for the human", title)
}
