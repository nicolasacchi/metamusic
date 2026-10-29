package app

import (
	"context"

	"github.com/nicolasacchi/metamusic/internal/albums"
	"github.com/nicolasacchi/metamusic/internal/curation"
	"github.com/nicolasacchi/metamusic/internal/muses"
)

// buildL2Curation wires the curation settings (lane A owns this file).
func buildL2Curation(in l2Inputs, s *l2Services) {
	s.Curation = curation.New(in.st, in.now)
}

// museDirectory adapts muses.Service to albums.MuseDirectory. muses cannot import the L2 packages, so it has its own MuseMeta
// and this is the one place the two meet. Labels (real Muse text) is only ever reached by the catalog when the operator turned
// subsonic_muse_names on (D-L2-16).
type museDirectory struct{ m *muses.Service }

func (d museDirectory) Ordinals(ctx context.Context) (map[string]albums.MuseMeta, error) {
	in, err := d.m.Ordinals(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]albums.MuseMeta, len(in))
	for id, m := range in {
		out[id] = albums.MuseMeta{Ordinal: m.Ordinal, Hidden: m.Hidden}
	}
	return out, nil
}

func (d museDirectory) Version(ctx context.Context) (string, error) { return d.m.Version(ctx) }

func (d museDirectory) Labels(ctx context.Context, ids []string) (map[string]string, error) {
	return d.m.Labels(ctx, ids)
}

// buildL2Catalog wires the Subsonic catalog over the Records service and the Muse directory (lanes A and A2 own this file).
// Without a Muse service only Record entries exist.
func buildL2Catalog(in l2Inputs, s *l2Services) {
	var dir albums.MuseDirectory
	if in.muses != nil {
		dir = museDirectory{in.muses}
	}
	s.Albums = albums.New(in.st, s.Records, dir, s.Curation, in.now)
}
