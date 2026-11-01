package app

import "github.com/nicolasacchi/metamusic/internal/atlas"

// buildL3Atlas constructs the Atlas and adherence service (lanes AT2 and AD own this file).
func buildL3Atlas(in l3Inputs, s *l3Services) {
	s.Atlas = atlas.New(in.st, in.now)
}
