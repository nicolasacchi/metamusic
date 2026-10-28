package records

import (
	"context"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// TestStubsA_FrozenMethodSet pins the method set of C120 and C121 at compile time and checks that the stubs answer
// errors.ErrUnsupported. Lane R-a deletes stub_a.go and this file together; the compile-time pin moves into its tests.
func TestStubsA_FrozenMethodSet(t *testing.T) {
	t.Parallel()
	var _ interface {
		Create(context.Context, auth.Principal, NewRecord) (Record, error)
		Get(context.Context, string) (Record, error)
		List(context.Context, ListFilter) (Page, error)
		Update(context.Context, auth.Principal, string, Patch) (Record, error)
		EditSlots(context.Context, auth.Principal, string, int, []SlotOp) (EditResult, error)
		Propose(context.Context, auth.Principal, string, int, string) (Record, error)
		Publish(context.Context, auth.Principal, string, int, bool) (Record, error)
		Slots(context.Context, string) ([]Slot, error)
		Resolved(context.Context, string) ([]ResolvedSlot, error)
	} = (*Service)(nil)
	s, ctx, p := New(nil, nil, func() string { return "id" }), context.Background(), auth.Principal{Kind: auth.KindHuman}
	for name, err := range map[string]error{
		"Create":    second(s.Create(ctx, p, NewRecord{})),
		"Get":       second(s.Get(ctx, "x")),
		"List":      second(s.List(ctx, ListFilter{})),
		"Update":    second(s.Update(ctx, p, "x", Patch{})),
		"EditSlots": second(s.EditSlots(ctx, p, "x", 1, nil)),
		"Propose":   second(s.Propose(ctx, p, "x", 1, "r")),
		"Publish":   second(s.Publish(ctx, p, "x", 1, true)),
		"Slots":     second(s.Slots(ctx, "x")),
		"Resolved":  second(s.Resolved(ctx, "x")),
	} {
		if !errors.Is(err, errors.ErrUnsupported) {
			t.Errorf("%s: %v, want errors.ErrUnsupported", name, err)
		}
	}
}

func second[T any](_ T, err error) error { return err }
