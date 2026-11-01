package app

import "github.com/nicolasacchi/metamusic/internal/sequence"

// buildL3Seq constructs the sequence service (lane SR owns this file).
func buildL3Seq(in l3Inputs, s *l3Services) {
	s.Sequence = sequence.New(in.st)
}
