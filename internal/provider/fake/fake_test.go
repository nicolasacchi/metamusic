package fake_test

import (
	"context"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
)

// TestFake_FrozenContract: the fake satisfies provider.Backend, counts every call and carries the C78.1
// defaults; the behaviour itself lands in M3-B7.
func TestFake_FrozenContract(t *testing.T) {
	t.Parallel()
	var b provider.Backend = fake.New(fake.Options{})
	if b.Name() != provider.BackendFake {
		t.Errorf("name %q", b.Name())
	}
	fb := b.(*fake.Backend)
	ctx := context.Background()
	_, _ = b.Quote(ctx)
	_, _ = b.Submit(ctx, provider.TakeSpec{})
	_, _ = b.Poll(ctx, provider.Ref{})
	_ = b.Cancel(ctx, provider.Ref{})
	_, _ = b.Sweep(ctx)
	_ = b.PurgeQueue(ctx)
	_ = b.ScaleToZero(ctx)
	_ = b.RevertDrift(ctx, nil)
	_, _ = b.Billing(ctx, time.Time{}, time.Time{})
	if c := fb.Counters(); c != (fake.Counters{Quote: 1, Submit: 1, Poll: 1, Cancel: 1, Sweep: 1, PurgeQueue: 1, ScaleToZero: 1, RevertDrift: 1, Billing: 1}) || c.Total() != 9 {
		t.Errorf("counters %+v", c)
	}
	if caps := b.Capabilities(); !caps.ScaleToZero || !caps.RevertDrift || caps.Billing != "hour" {
		t.Errorf("capabilities %+v", caps)
	}
	if got := fake.New(fake.Options{Permissions: fake.Permissions{PatchForbidden: true, BillingForbidden: true}}).Capabilities(); got.ScaleToZero || got.RevertDrift || got.Billing != "none" {
		t.Errorf("restricted capabilities %+v", got)
	}
}
