package records_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/records"
)

// The signatures of C138 that lane SR must keep.
var _ func(*records.Service, context.Context, auth.Principal, string, int, string, *records.SeqSpec) (records.Record, error) = (*records.Service).ProposeWith

// TestProposeWith_SequenceUnsupported: a sequence payload answers ErrUnsupported before the store is touched (lane SR replaces
// it); the slot op does the same through EditSlots. This test goes with the stub.
func TestProposeWith_SequenceUnsupported(t *testing.T) {
	t.Parallel()
	s := records.New(nil, nil, nil)
	_, err := s.ProposeWith(context.Background(), auth.Principal{}, "x", 1, "r", &records.SeqSpec{Strategy: "arc"})
	if !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("ProposeWith(seq) = %v, want ErrUnsupported until lane SR", err)
	}
}
