package engineadopt_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engineadopt"
	"github.com/nicolasacchi/metamusic/internal/generation"
)

// *Service is the AdoptionGate generation consumes: the freeze keeps the two sides compiling against each other.
var _ generation.AdoptionGate = (*engineadopt.Service)(nil)

func TestStateAdopted(t *testing.T) {
	t.Parallel()
	if !(engineadopt.State{State: engineadopt.StateAdopted}).Adopted() || (engineadopt.State{State: engineadopt.StateNone}).Adopted() {
		t.Error("Adopted")
	}
}
