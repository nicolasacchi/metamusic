package records

// SeqSpec is the payload of the slot op "sequence" and of propose_record.sequence (L3 C138.2, C138.3). An empty Strategy
// means the default (arc); the endpoint Song ids are optional.
type SeqSpec struct {
	Strategy    string
	FirstSongID string
	LastSongID  string
}
