package tape_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/tape"
)

// TestStubsT1_Pipeline: the stub pipeline answers errors.ErrUnsupported. Lane T1 deletes stub_t1.go and this test (NewPipeline returns tape.Pipeline, so its type is pinned by the call).
func TestStubsT1_Pipeline(t *testing.T) {
	t.Parallel()
	if _, err := tape.NewPipeline(tape.PipelineOptions{}).Render(context.Background(), tape.Job{}, nil); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Render: %v", err)
	}
}

// TestStubsT2_FrozenMethodSet pins the method set of C128 and checks the stubs: Export, Get and Brief answer
// errors.ErrUnsupported; the calls every process makes (Run, RunOne, Recover, Sweep) are harmless; Resolve is serve.ErrNotFound
// so the media chain falls through to the next resolver. Lane T2 deletes stub_t2.go and this test.
func TestStubsT2_FrozenMethodSet(t *testing.T) {
	t.Parallel()
	var _ interface {
		Export(context.Context, auth.Principal, string, tape.ExportInput) (tape.Tape, error)
		Get(context.Context, string) (tape.Tape, error)
		Brief(context.Context, string, int) ([]tape.Brief, error)
		Run(context.Context)
		RunOne(context.Context) (bool, error)
		Recover(context.Context) error
		Resolve(context.Context, string, string) (serve.Object, error)
		Sweep(context.Context, time.Duration) (int, error)
	} = (*tape.Service)(nil)
	var _ serve.Resolver = (*tape.Service)(nil)
	s, ctx := tape.New(tape.Options{}), context.Background()
	if _, err := s.Export(ctx, auth.Principal{}, "r", tape.ExportInput{}); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Export: %v", err)
	}
	if _, err := s.Get(ctx, "t"); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Get: %v", err)
	}
	if _, err := s.Brief(ctx, "r", 3); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Brief: %v", err)
	}
	if ran, err := s.RunOne(ctx); ran || err != nil {
		t.Errorf("RunOne: %v %v", ran, err)
	}
	if err := s.Recover(ctx); err != nil {
		t.Errorf("Recover: %v", err)
	}
	if n, err := s.Sweep(ctx, time.Hour); n != 0 || err != nil {
		t.Errorf("Sweep: %d %v", n, err)
	}
	if _, err := s.Resolve(ctx, "abc", "flac"); !errors.Is(err, serve.ErrNotFound) {
		t.Errorf("Resolve: %v, want serve.ErrNotFound", err)
	}
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { s.Run(rctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
}
