package records

import (
	"errors"
	"fmt"
)

// SeqSpec is the payload of the slot op "sequence" and of propose_record.sequence (L3 C138.2, C138.3). An empty Strategy
// means the default (arc); the endpoint Song ids are optional.
type SeqSpec struct {
	Strategy    string
	FirstSongID string
	LastSongID  string
}

// errSequenceUnsupported is what the sequence paths answer until lane SR implements them; the API maps it to 501.
func errSequenceUnsupported() error {
	return fmt.Errorf("records: sequence: %w", errors.ErrUnsupported)
}
