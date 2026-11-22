package pods

import (
	"errors"
	"fmt"
)

// Pod states (C394). The legal moves are the rows of the pod_transitions table (migration 0026_pods.sql); Transitions is the Go
// mirror and TestPodTransitions_GoEqualsSQL pins the two together. Only the lifecycle loop moves a pod (single writer, D-M3-3).
const (
	StateIntent      = "intent"
	StateCreating    = "creating"
	StateBooting     = "booting"
	StateReady       = "ready"
	StateBusy        = "busy"
	StateOrphaned    = "orphaned"
	StateTerminating = "terminating"
	StateTerminated  = "terminated"
)

// Transitions maps a state to the states it may move to.
var Transitions = map[string][]string{
	StateIntent:      {StateCreating, StateTerminated},
	StateCreating:    {StateBooting, StateTerminated, StateOrphaned},
	StateBooting:     {StateReady, StateTerminating, StateOrphaned},
	StateReady:       {StateBusy, StateBooting, StateTerminating, StateOrphaned},
	StateBusy:        {StateReady, StateBooting, StateTerminating, StateOrphaned},
	StateOrphaned:    {StateTerminating, StateTerminated},
	StateTerminating: {StateTerminated},
}

// ErrIllegalTransition is returned for a move that is not in Transitions; the database refuses it too (trigger).
var ErrIllegalTransition = errors.New("pods: illegal state transition")

// CanMove reports whether from -> to is a legal move.
func CanMove(from, to string) bool {
	for _, t := range Transitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

func checkMove(from, to string) error {
	if !CanMove(from, to) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, from, to)
	}
	return nil
}

// Live reports whether the state is not terminal.
func Live(state string) bool { return state != StateTerminated }

// stopped states answer a poll with "stop" (C395.1).
func stopped(state string) bool {
	return state == StateTerminating || state == StateTerminated || state == StateOrphaned
}
