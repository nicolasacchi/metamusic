package features_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/features"
)

// frozen is the method set of C135 that lanes FC1 and FC2 must keep; this file goes with stub_fc2.go.
type frozen interface {
	ComputeTx(ctx context.Context, tx *sql.Tx, env features.Env, trackIDs, songIDs []string) (features.Changes, error)
	SweepStaleTx(ctx context.Context, tx *sql.Tx, env features.Env, limit int) (int, error)
	Verify(ctx context.Context) ([]features.Anomaly, error)
	Take(ctx context.Context, trackID string) (features.Row, bool, error)
	Songs(ctx context.Context, songIDs []string) (map[string]features.SongRow, error)
	Takes(ctx context.Context, trackIDs []string) (map[string]features.Row, error)
}

var (
	_ frozen                                = (*features.Service)(nil)
	_ func(int, string) features.Camelot    = features.CamelotOf
	_ func(int, string) string              = features.KeyName
	_ func(string) (int, string, bool)      = features.ParseStyleKey
	_ func(string) (int, string, bool)      = features.ParseABCKey
	_ func(features.Facets) (int, bool)     = features.PredictEnergy
	_ func(features.TakeInput) features.Row = features.DeriveTake
	_ func(features.Camelot) string         = features.Camelot.String
)

func TestStub_FeaturesServiceUnsupported(t *testing.T) {
	t.Parallel()
	s := features.New(nil, nil)
	if _, err := s.Verify(context.Background()); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Verify = %v, want ErrUnsupported until lane FC2", err)
	}
	if _, _, err := s.Take(context.Background(), "t"); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Take = %v, want ErrUnsupported until lane FC2", err)
	}
}
