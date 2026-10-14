package jobs

import (
	"errors"
	"fmt"
)

// Transition is one legal job move of the C75.2 table; Rows names the table rows that use it.
type Transition struct {
	From, To string
	Rows     []string
}

// Transitions is the 34-pair job transition table. It equals the job_transitions table of migration 0006
// (TestTransitions_GoEqualsSQL); the Go guard and the SQL trigger refuse everything else.
var Transitions = []Transition{
	{StateAwaiting, StateQueued, []string{"1"}},
	{StateAwaiting, StateCancelled, []string{"2"}},
	{StateAwaiting, StateExpired, []string{"3"}},
	{StateQueued, StateSubmitting, []string{"4"}},
	{StateQueued, StateStarting, []string{"8b"}},
	{StateQueued, StateCancelled, []string{"5"}},
	{StateQueued, StateCancelling, []string{"5b"}},
	{StateQueued, StateFailed, []string{"6", "6b"}},
	{StateQueued, StateTransferring, []string{"7"}},
	{StateSubmitting, StateStarting, []string{"8"}},
	{StateSubmitting, StateQueued, []string{"9"}},
	{StateSubmitting, StateFailed, []string{"10"}},
	{StateSubmitting, StateCancelling, []string{"11"}},
	{StateSubmitting, StateTransferring, []string{"7"}},
	{StateStarting, StateGenerating, []string{"12"}},
	{StateStarting, StateQueued, []string{"13"}},
	{StateStarting, StateTransferring, []string{"7"}},
	{StateStarting, StateCancelling, []string{"14"}},
	{StateStarting, StateFailing, []string{"15", "16b"}},
	{StateStarting, StateFailed, []string{"16"}},
	{StateGenerating, StateTransferring, []string{"7", "17"}},
	{StateGenerating, StateCancelling, []string{"14"}},
	{StateGenerating, StateFailing, []string{"15", "16b"}},
	{StateGenerating, StateFailed, []string{"16"}},
	{StateTransferring, StateIngesting, []string{"18"}},
	{StateTransferring, StateFailed, []string{"19"}},
	{StateTransferring, StateFailing, []string{"19b"}},
	{StateTransferring, StateCancelling, []string{"14"}},
	{StateIngesting, StateCompleted, []string{"20"}},
	{StateIngesting, StateFailed, []string{"21"}},
	{StateCancelling, StateCancelled, []string{"22"}},
	{StateCancelling, StateTransferring, []string{"23"}},
	{StateFailing, StateFailed, []string{"24"}},
	{StateFailing, StateTransferring, []string{"23"}},
}

var legal = func() map[[2]string]bool {
	m := make(map[[2]string]bool, len(Transitions))
	for _, t := range Transitions {
		m[[2]string{t.From, t.To}] = true
	}
	return m
}()

// Errors of the Go guard. The messages equal the SQL trigger messages after the "jobs: " prefix, so a
// refusal reads the same whichever layer catches it.
var (
	// ErrTerminalImmutable: the source state is terminal.
	ErrTerminalImmutable = errors.New("jobs: terminal jobs are immutable")
	// ErrIllegalTransition: the pair is not in the table.
	ErrIllegalTransition = errors.New("jobs: illegal transition")
)

// CanTransition is the Go guard: nil when from -> to is a row of the table.
func CanTransition(from, to string) error {
	if from == to {
		return nil
	}
	if IsTerminal(from) {
		return ErrTerminalImmutable
	}
	if !legal[[2]string{from, to}] {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, from, to)
	}
	return nil
}
