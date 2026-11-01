package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/nicolasacchi/metamusic/internal/features"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The L3 glue of `library` (C135.6). Features are drained by the L1 Run loop, so serve starts nothing new for L3.

// l3Verify runs features.Verify after the L2 invariants. The lines use the L1 form "<code> <subject> <detail>" and every
// finding counts as an error. Until lane FC2 implements Verify the service answers errors.ErrUnsupported, which reads as
// "no findings".
func l3Verify(ctx context.Context, st *store.Store, out io.Writer) (bad int, err error) {
	an, err := features.New(st, time.Now).Verify(ctx)
	if errors.Is(err, errors.ErrUnsupported) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	for _, a := range an {
		fmt.Fprintf(out, "%s %s %s\n", a.Code, a.Subject, a.Detail)
	}
	return len(an), nil
}
