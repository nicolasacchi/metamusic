package app

import "github.com/nicolasacchi/metamusic/internal/recordview"

// buildL2RecordView wires the read assembly (lane V2 owns this file). The batch readers (sessions, sounds, songs, picks,
// loudness) and the cover linker (an adapter over cover.Service.EnsureMosaic and the link signer) are nil until V2 supplies
// them here. recordview never reaches Muse text except through muses.Service.Refs and the curation flag. Tapes is late-bound
// (see tapeBriefsOf) because the tape service takes recordview as its snapshot source.
func buildL2RecordView(in l2Inputs, s *l2Services) {
	s.RecordView = recordview.New(recordview.Options{
		Store: in.st, Records: s.Records, Muses: in.muses, Curation: s.Curation, Library: in.lib,
		Tapes: tapeBriefsOf(s), Now: in.now,
	})
}
