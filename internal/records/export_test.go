package records

import "context"

// Test seams of the Syncer for the external test package.

// SetProbe replaces the Pending call of Run.
func (y *Syncer) SetProbe(f func(ctx context.Context) (int, error)) { y.probe = f }

// SetSettle replaces the Settle call of Run.
func (y *Syncer) SetSettle(f func(ctx context.Context) error) { y.settle = f }

// SetFault installs a function that runs at the start of every slot transaction with the slot id; an error it returns fails that attempt.
func (y *Syncer) SetFault(f func(slotID string) error) { y.fault = f }

// Passes is the number of non-empty batches Settle has taken.
func (y *Syncer) Passes() int { return y.passes }

// The constants the external tests pin against the packages this one may not import.
const (
	TapeLeaseTTLSeconds = int(tapeLeaseTTL / 1e9)
)

// CurationKeys are the settings keys Verify R9 accepts.
func CurationKeys() []string { return append([]string(nil), curationKeys...) }
