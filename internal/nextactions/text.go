package nextactions

import "fmt"

// The texts of the rules (C139.2). A text is a pure function of the candidate and the names; it never contains a raw id except
// the Muse id prefix of Names.Muse, and it is never persisted or sent to a notification (C144.2). Muse is named by Names.Muse,
// which returns the whole noun phrase ("Muse 019a1b2c") so that one template serves the id variant and, once D-L3-16 is
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
