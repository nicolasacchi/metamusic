package provenance

import (
	"context"
	"database/sql"

	"github.com/nicolasacchi/metamusic/internal/ladder"
	"github.com/nicolasacchi/metamusic/internal/picks"
	"github.com/nicolasacchi/metamusic/internal/sounds"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// PickStep is what phase B needs of picks (frozen by L1-B2). *picks.Service satisfies it.
type PickStep interface {
	RecomputeTx(ctx context.Context, tx *sql.Tx, env store.Env, songIDs []string, st map[string]ladder.Strength) (picks.Changes, error)
}

// SoundStep is what phase B needs of sounds (frozen by L1-B2). *sounds.Service satisfies it.
type SoundStep interface {
	ComputeTx(ctx context.Context, tx *sql.Tx, env store.Env, trackIDs, songIDs []string) (sounds.Changes, error)
	SweepStaleTx(ctx context.Context, tx *sql.Tx, env store.Env, limit int) (int, error)
}

// pickSweeper is the optional sweep entry of the real picks service (Run calls it at start and hourly).
type pickSweeper interface {
	Sweep(ctx context.Context) (picks.Changes, error)
}

// FeatureChanges is what the feature step did, for the Reconciler report (L3 C135.5). It is provenance-local because
// provenance must not import features (L1's import table); app adapts *features.Service to FeatureStep.
type FeatureChanges struct{ Takes, Songs int }

// FeatureStep is what phase B needs of the features layer (L3 C95 amendment, frozen by L3-F2). It runs after picks and
// sounds, for the dirty takes and the affected Songs of the pass, and is idempotent and digest-guarded.
type FeatureStep interface {
	ComputeTx(ctx context.Context, tx *sql.Tx, env store.Env, trackIDs, songIDs []string) (FeatureChanges, error)
	SweepStaleTx(ctx context.Context, tx *sql.Tx, env store.Env, limit int) (int, error)
}

// nopFeatures is the step until the composition root wires the real one: it changes no row and no report field.
type nopFeatures struct{}

func (nopFeatures) ComputeTx(context.Context, *sql.Tx, store.Env, []string, []string) (FeatureChanges, error) {
	return FeatureChanges{}, nil
}

func (nopFeatures) SweepStaleTx(context.Context, *sql.Tx, store.Env, int) (int, error) { return 0, nil }
