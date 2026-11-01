package sequencer

import "errors"

// The bodies of this file are replaced by lanes SQ1 (TransitionMicro, Distance, ExplainText, Digest) and SQ2 (Plan); SQ1
// and SQ2 delete it with their last function. The signatures are frozen by L3-F2.

// Plan orders songs for the strategy (C137.4).
func Plan(songs []Song, o Options) (Result, error) {
	return Result{}, errors.ErrUnsupported
}

// TransitionMicro is the micro-cost of one transition under the steady weights' parts (C137.2).
func TransitionMicro(a, b Song) Parts { return Parts{} }

// Distance is one transition under the steady weights with absolute energies (C137.3), used by the Atlas neighbours.
func Distance(a, b Song) int64 { return 0 }

// ExplainText renders one step deterministically, with no titles (C137.5).
func ExplainText(s Step, strategy Strategy) string { return "" }

// Digest is the sha256 of the canonical inputs of a plan.
func Digest(songs []Song, o Options) string { return "" }
