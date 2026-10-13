package providertest_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/providertest"
)

var errBoom = errors.New("boom")

func TestProvidertest_Script(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var b provider.Backend
	pt := providertest.New("fake", providertest.Script{
		Submit: func(ctx context.Context, s provider.TakeSpec) (provider.Ref, error) {
			if s.JobID == "bad" {
				return provider.Ref{}, errBoom
			}
			return provider.Ref{Backend: "fake", ID: "r-" + s.JobID}, nil
		},
	})
	b = pt
	if b.Name() != "fake" {
		t.Errorf("name %q", b.Name())
	}
	ref, err := b.Submit(ctx, provider.TakeSpec{JobID: "j1", AttemptID: "a1"})
	if err != nil || ref.ID != "r-j1" {
		t.Fatalf("submit: %+v %v", ref, err)
	}
	if _, err := b.Submit(ctx, provider.TakeSpec{JobID: "bad"}); !errors.Is(err, errBoom) {
		t.Errorf("scripted error lost: %v", err)
	}
	// defaults: a good quote, a queued status, no-op management calls
	q, err := b.Quote(ctx)
	if err != nil || q.ListPricePPH <= 0 || q.Endpoint.WorkersMax != 2 {
		t.Errorf("default quote %+v %v", q, err)
	}
	if st, err := b.Poll(ctx, ref); err != nil || st.State != provider.Queued {
		t.Errorf("default poll %+v %v", st, err)
	}
	if err := b.Cancel(ctx, ref); err != nil {
		t.Error(err)
	}
	if _, err := b.Sweep(ctx); err != nil {
		t.Error(err)
	}
	if err := b.PurgeQueue(ctx); err != nil {
		t.Error(err)
	}
	if err := b.ScaleToZero(ctx); err != nil {
		t.Error(err)
	}
	if err := b.RevertDrift(ctx, nil); err != nil {
		t.Error(err)
	}
	if _, err := b.Billing(ctx, time.Time{}, time.Time{}); err != nil {
		t.Error(err)
	}
	c := pt.Calls()
	want := providertest.Calls{Quote: 1, Submit: 2, Poll: 1, Cancel: 1, Sweep: 1, PurgeQueue: 1, ScaleToZero: 1, RevertDrift: 1, Billing: 1}
	if c != want || c.Total() != 10 {
		t.Errorf("calls = %+v (total %d), want %+v", c, c.Total(), want)
	}
	if specs := pt.Specs(); len(specs) != 2 || specs[0].JobID != "j1" {
		t.Errorf("recorded specs %+v", specs)
	}
	if !b.Capabilities().ScaleToZero {
		t.Error("default capabilities")
	}
}

func TestProvidertest_Concurrent(t *testing.T) {
	t.Parallel()
	b := providertest.New("fake", providertest.Script{})
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = b.Submit(context.Background(), provider.TakeSpec{JobID: "j"})
			_, _ = b.Poll(context.Background(), provider.Ref{})
		}()
	}
	wg.Wait()
	if c := b.Calls(); c.Submit != 16 || c.Poll != 16 {
		t.Errorf("calls %+v", c)
	}
}

// A backend whose every method panics proves a code path never touches the provider.
func TestProvidertest_Panicking(t *testing.T) {
	t.Parallel()
	b := providertest.NewPanicking()
	calls := map[string]func(){
		"Quote":       func() { _, _ = b.Quote(context.Background()) },
		"Submit":      func() { _, _ = b.Submit(context.Background(), provider.TakeSpec{}) },
		"Poll":        func() { _, _ = b.Poll(context.Background(), provider.Ref{}) },
		"Cancel":      func() { _ = b.Cancel(context.Background(), provider.Ref{}) },
		"Sweep":       func() { _, _ = b.Sweep(context.Background()) },
		"PurgeQueue":  func() { _ = b.PurgeQueue(context.Background()) },
		"ScaleToZero": func() { _ = b.ScaleToZero(context.Background()) },
		"RevertDrift": func() { _ = b.RevertDrift(context.Background(), nil) },
		"Billing":     func() { _, _ = b.Billing(context.Background(), time.Time{}, time.Time{}) },
	}
	for name, f := range calls {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s did not panic", name)
				}
			}()
			f()
		}()
	}
	if b.Calls().Total() != 9 {
		t.Errorf("calls %+v", b.Calls())
	}
}
