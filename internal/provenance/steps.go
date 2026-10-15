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
