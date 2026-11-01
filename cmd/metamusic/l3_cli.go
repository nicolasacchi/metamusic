package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/features"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The L3 glue of `library` (C135.6). Features are drained by the L1 Run loop, so serve starts nothing new for L3.

// useFeatures installs the real feature step in a CLI Reconciler, so a CLI settle derives the rows a server settle would.
func useFeatures(st *store.Store, prov *provenance.Service) {
	prov.UseFeatures(app.FeatureStep(features.New(st, time.Now)))
}

// l3Verify runs features.Verify after the L2 invariants. The lines use the L1 form "<code> <subject> <detail>" and every
// finding counts as an error.
func l3Verify(ctx context.Context, st *store.Store, out io.Writer) (bad int, err error) {
	an, err := features.New(st, time.Now).Verify(ctx)
	if err != nil {
		return 0, err
	}
	for _, a := range an {
		fmt.Fprintf(out, "%s %s %s\n", a.Code, a.Subject, a.Detail)
	}
	return len(an), nil
}
