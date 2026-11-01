package atlas_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/atlas"
)

// The method set of C141 that lanes AT1, AT2 and AD must keep; this file goes with stub_at.go.
var (
	_ func(*atlas.Service, context.Context, atlas.Query) (atlas.Atlas, error)              = (*atlas.Service).Get
	_ func(*atlas.Service, context.Context, atlas.AdherenceQuery) (atlas.Adherence, error) = (*atlas.Service).Adherence
	_ func([]atlas.Input, atlas.AxisDef, atlas.AxisDef) []atlas.XY                         = atlas.Layout
)

func TestStub_AtlasUnsupported(t *testing.T) {
	t.Parallel()
	s := atlas.New(nil, nil)
	if _, err := s.Get(context.Background(), atlas.Query{}); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Get = %v, want ErrUnsupported until lane AT2", err)
	}
	if _, err := s.Adherence(context.Background(), atlas.AdherenceQuery{}); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Adherence = %v, want ErrUnsupported until lane AD", err)
	}
}
