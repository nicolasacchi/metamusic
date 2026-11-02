package atlas

import (
	"cmp"
	"slices"

	"github.com/nicolasacchi/metamusic/internal/sequencer"
)

// seqSong turns a scope row into the sequencer's input; ok is false for a Song with no tempo, key or energy at all.
func seqSong(r row) (sequencer.Song, bool) {
	s := sequencer.Song{ID: r.id, Energyx10000: sequencer.NoEnergy,
		Bases: sequencer.Bases{BPM: basisWord(r.f.bpmBasis.String), Energy: basisWord(r.f.energyBasis.String), Key: basisWord(r.f.keyBasis.String)}}
	if r.f.bpm.Valid {
		s.BPMx100 = iv(r.f.bpm)
	}
	if r.f.energy.Valid {
		s.Energyx10000 = iv(r.f.energy)
	}
	if r.f.keyNum.Valid && len(r.f.keyLetter.String) == 1 {
		s.Key = &sequencer.Camelot{Num: iv(r.f.keyNum), Letter: r.f.keyLetter.String[0]}
	}
	return s, s.BPMx100 != 0 || s.Energyx10000 != sequencer.NoEnergy || s.Key != nil
}

func basisWord(b string) string { return b }

// neighbours returns the k Songs of rows nearest to near by sequencer.Distance over their features (absolute energy, no
// rescale), nearest first, ties by id; Songs with no feature at all are skipped and near itself is not listed.
func neighbours(near row, rows []row, k int) []Neighbour {
	out := []Neighbour{}
	a, ok := seqSong(near)
	if !ok {
		return out
	}
	for _, r := range rows {
		if r.id == near.id {
			continue
		}
		b, ok := seqSong(r)
		if !ok {
			continue
		}
		p := sequencer.TransitionMicro(a, b)
		out = append(out, Neighbour{ID: r.id, Title: truncRunes(r.title, TitleRunes), Distance: sequencer.Distance(a, b),
			Parts: NeighbourParts{Tempo: p.Tempo, Key: p.Key, Energy: p.Energy}})
	}
	slices.SortFunc(out, func(x, y Neighbour) int {
		if c := cmp.Compare(x.Distance, y.Distance); c != 0 {
			return c
		}
		return cmp.Compare(x.ID, y.ID)
	})
	if len(out) > k {
		out = out[:k]
	}
	return out
}
