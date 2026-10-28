package records

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestStubsB_FrozenMethodSet pins the method set of C122 and checks the stubs: the process-wide calls (Settle, Pending, Run,
// Verify, catchUpTx) are harmless no-ops, Changes answers errors.ErrUnsupported. Lane R-b deletes stub_b.go and this file.
func TestStubsB_FrozenMethodSet(t *testing.T) {
	t.Parallel()
	s := New(nil, nil, nil)
	y := NewSyncer(s, nil)
	ctx := context.Background()
	var _ interface {
		Settle(context.Context) (SyncReport, error)
		Run(context.Context)
		Pending(context.Context) (int, error)
	} = y
	if _, err := s.Changes(ctx, ChangeFilter{}); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Changes: %v", err)
	}
	if an, err := s.Verify(ctx); an != nil || err != nil {
		t.Errorf("Verify: %v %v", an, err)
	}
	if err := s.catchUpTx(ctx, nil, "r"); err != nil {
		t.Errorf("catchUpTx: %v", err)
	}
	if rep, err := y.Settle(ctx); rep != (SyncReport{}) || err != nil {
		t.Errorf("Settle: %+v %v", rep, err)
	}
	if n, err := y.Pending(ctx); n != 0 || err != nil {
		t.Errorf("Pending: %d %v", n, err)
	}
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { y.Run(rctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
}
