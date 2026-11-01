package sequencer

import "errors"

// Plan is still a stub: lane SQ2 replaces it and deletes this file with its test. Lane SQ1 took over TransitionMicro,
// Distance, ExplainText and Digest (cost.go, explain.go, digest.go). The signature is frozen by L3-F2.

// Plan orders songs for the strategy (C137.4).
func Plan(songs []Song, o Options) (Result, error) {
	return Result{}, errors.ErrUnsupported
}
