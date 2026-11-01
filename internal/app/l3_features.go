package app

import (
	"context"
	"database/sql"

	"github.com/nicolasacchi/metamusic/internal/features"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// buildL3Features constructs the features service and, when the composition root asks for it (serve, and the rigs that set
// servertest.Options.RealFeatures), installs it as the Reconciler's third phase-B step and as the sounds recompute hook.
func buildL3Features(in l3Inputs, s *l3Services) {
	s.Features = features.New(in.st, in.now)
	if !in.o.RealFeatures {
		return
	}
	step := featureStep{s.Features}
	in.prov.UseFeatures(step)
	in.sounds.SetRecomputeHook(func(ctx context.Context, tx *sql.Tx, env store.Env, songIDs []string) error {
		_, err := step.ComputeTx(ctx, tx, env, nil, songIDs)
		return err
	})
}

// FeatureStep adapts the features service to the Reconciler's step interface; the CLI installs it with
// provenance.UseFeatures so a CLI settle derives the same rows as the server (provenance must not import features, D-L3-41).
func FeatureStep(f *features.Service) provenance.FeatureStep { return featureStep{f} }

// featureStep adapts *features.Service to provenance.FeatureStep.
type featureStep struct{ f *features.Service }

func (a featureStep) ComputeTx(ctx context.Context, tx *sql.Tx, env store.Env, trackIDs, songIDs []string) (provenance.FeatureChanges, error) {
	c, err := a.f.ComputeTx(ctx, tx, env, trackIDs, songIDs)
	return provenance.FeatureChanges{Takes: c.Takes, Songs: c.Songs}, err
}

func (a featureStep) SweepStaleTx(ctx context.Context, tx *sql.Tx, env store.Env, limit int) (int, error) {
	return a.f.SweepStaleTx(ctx, tx, env, limit)
}
