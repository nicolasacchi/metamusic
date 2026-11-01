package atlas

import (
	"context"
	"errors"
)

// Axes is the registry (extension move 2), pinned by testdata/axes_v1.golden.json. Lane AT1 fills it and deletes this file
// with its last stub. Empty until then.
var Axes []AxisDef

// XY is a laid-out point in [0, 1].
type XY struct{ X, Y float64 }

// Input is one point to lay out.
type Input struct {
	ID     string
	XV, YV int    // numeric value in the axis' fixed-point units, or category band index
	XC, YC string // categorical value, when the axis is categorical
}

// Layout places points with the integer-only algorithm of C141.2 (lane AT1).
func Layout(pts []Input, x, y AxisDef) []XY { return nil }

// Get answers get_song_atlas: one read-only transaction, never writes (lane AT2).
func (s *Service) Get(ctx context.Context, q Query) (Atlas, error) {
	return Atlas{}, errors.ErrUnsupported
}

// Adherence answers get_adherence (lane AD).
func (s *Service) Adherence(ctx context.Context, q AdherenceQuery) (Adherence, error) {
	return Adherence{}, errors.ErrUnsupported
}
