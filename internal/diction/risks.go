package diction

import (
	"context"
	"sort"

	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

// maxRisks bounds the risky words of one lyric.
const maxRisks = 10

// riskyOf lists the lexicon words of a lyric that carry risk, most mangled first (ties by word), at most maxRisks. Lint and
// the lexicon share their keys because both tokenise with tierb.RefWords (C345.4).
func riskyOf(sn *snapshot, lang string, ref []tierb.RefWord) []LexiconEntry {
	langs := []string{lang}
	if lang == "" { // the lyric is too short to tell its language: any language that heard the word badly counts
		langs = nil
		seenLang := map[string]bool{}
		for _, e := range sn.entries {
			if !seenLang[e.Lang] {
				seenLang[e.Lang] = true
				langs = append(langs, e.Lang)
			}
		}
	}
	seen := map[string]bool{}
	var out []LexiconEntry
	for _, w := range ref {
		if seen[w.Fold] {
			continue
		}
		seen[w.Fold] = true
		for _, l := range langs {
			if i, ok := sn.index[[2]string{l, w.Fold}]; ok && sn.entries[i].Risk {
				out = append(out, sn.entries[i])
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Mangled != out[j].Mangled {
			return out[i].Mangled > out[j].Mangled
		}
		return out[i].Word < out[j].Word
	})
	return out[:min(len(out), maxRisks)]
}

// Risks returns the words of a lyric that earlier takes sang badly: lexicon entries with risk (at least three attempts, at least
// half of them mangled, not ignored), under the language the lyric is written in (any language when it cannot be told). It feeds the diction_risk lint warning.
func (s *Service) Risks(ctx context.Context, lyrics string) ([]yue2.DictionRisk, error) {
	if err := s.wired(); err != nil {
		return nil, err
	}
	ref := tierb.RefWords(lyrics)
	if len(ref) == 0 {
		return nil, nil
	}
	sn, err := s.lexicon(ctx)
	if err != nil {
		return nil, err
	}
	var out []yue2.DictionRisk
	for _, e := range riskyOf(sn, yue2.DetectLanguage(lyrics), ref) {
		typical := ""
		if len(e.TypicalHeard) > 0 {
			typical = e.TypicalHeard[0].Text
		}
		out = append(out, yue2.DictionRisk{Word: e.Word, Typical: typical, Attempts: e.Attempts, Mangled: e.Mangled})
	}
	return out, nil
}
