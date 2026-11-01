package app

import (
	"context"
	"database/sql"
	"errors"

	"github.com/nicolasacchi/metamusic/internal/features"
	"github.com/nicolasacchi/metamusic/internal/provenance"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// buildL3Features constructs the features service, adapts it to the Reconciler's third phase-B step and to the sounds
// recompute hook (lane FC2 owns this file). Until FC2 lands the service answers errors.ErrUnsupported, which the adapter
// reads as "nothing to do", so wiring it now changes no row and no report field.
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

// featureStep adapts *features.Service to provenance.FeatureStep (provenance must not import features, D-L3-41).
type featureStep struct{ f *features.Service }

func (a featureStep) ComputeTx(ctx context.Context, tx *sql.Tx, env store.Env, trackIDs, songIDs []string) (provenance.FeatureChanges, error) {
	c, err := a.f.ComputeTx(ctx, tx, env, trackIDs, songIDs)
	if errors.Is(err, errors.ErrUnsupported) {
		return provenance.FeatureChanges{}, nil
	}
	return provenance.FeatureChanges{Takes: c.Takes, Songs: c.Songs}, err
}

func (a featureStep) SweepStaleTx(ctx context.Context, tx *sql.Tx, env store.Env, limit int) (int, error) {
	n, err := a.f.SweepStaleTx(ctx, tx, env, limit)
	if errors.Is(err, errors.ErrUnsupported) {
		return 0, nil
	}
	return n, err
}
