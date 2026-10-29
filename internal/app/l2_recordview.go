package app

import "github.com/nicolasacchi/metamusic/internal/recordview"

// buildL2RecordView wires the read assembly (lane V2 owns this file). The batch readers are adapters over the L1 tables
// (l2_readers.go) and the cover linker adapts cover.Service.EnsureMosaic and the link signer. recordview never reaches Muse text
// except through muses.Service.Refs and the curation flag. Tapes is late-bound (see tapeBriefsOf) because the tape service takes
// recordview as its snapshot source.
func buildL2RecordView(in l2Inputs, s *l2Services) {
	rd := l2Readers{st: in.st}
	s.RecordView = recordview.New(recordview.Options{
		Store: in.st, Records: s.Records, Muses: in.muses, Sessions: rd, Sounds: rd, Songs: rd, Picks: rd, Ears: rd,
		Curation: s.Curation, Library: in.lib, Covers: coverLinker{covers: in.covers, signer: in.signer},
		Tapes: tapeBriefsOf(s), Now: in.now,
	})
}
