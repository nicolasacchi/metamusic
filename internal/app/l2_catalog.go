package app

import (
	"github.com/nicolasacchi/metamusic/internal/albums"
	"github.com/nicolasacchi/metamusic/internal/curation"
)

// buildL2Curation wires the curation settings (lane A owns this file).
func buildL2Curation(in l2Inputs, s *l2Services) {
	s.Curation = curation.New(in.st, in.now)
}

// buildL2Catalog wires the Subsonic catalog over the Records service (lanes A and A2 own this file). The Muse directory is nil
// until lane A2 adapts muses.Service to albums.MuseDirectory (names only when the flag is on, D-L2-16); with a nil directory
// only Record entries exist.
func buildL2Catalog(in l2Inputs, s *l2Services) {
	s.Albums = albums.New(in.st, s.Records, nil, s.Curation, in.now)
}
