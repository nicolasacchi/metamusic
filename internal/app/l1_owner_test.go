package app_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/provenance"
)

// TestL1_TwoServersDoNotShareTheLease: two Builds on one database get distinct default owners, so a second settler
// started while the first holds the lease gets ErrSettleBusy (D-L1-4, D-L1-35).
func TestL1_TwoServersDoNotShareTheLease(t *testing.T) {
	t.Parallel()
	e := newM3Env(t, nil)
	var (
		mu     sync.Mutex
		second *app.Services
		got    error
		ran    bool
	)
	first := e.build(t, app.Options{L1: app.L1Options{BeforeSettle: func(ctx context.Context) {
		mu.Lock()
		defer mu.Unlock()
		if second == nil || ran {
			return
		}
		ran = true
		_, got = second.Provenance.Settle(ctx)
	}}})
	mu.Lock()
	second = e.build(t, app.Options{})
	mu.Unlock()
	if _, err := first.Provenance.Settle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !ran || !errors.Is(got, provenance.ErrSettleBusy) {
		t.Fatalf("second Settle while the first holds the lease: ran=%v err=%v, want ErrSettleBusy", ran, got)
	}
}
