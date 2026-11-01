package records

// SequenceOutcome is what a sequence step reports to its caller: the stored record_sequences id, the strategy, the slot
// order before and after, and whether the order changed (L3 C138.2). Lane SR fills it from EditSlots and ProposeWith.
type SequenceOutcome struct {
	ID       string
	Strategy string
	Before   []string
	After    []string
	Changed  bool
}
