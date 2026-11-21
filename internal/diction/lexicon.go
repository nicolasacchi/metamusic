package diction

import (
	_ "embed"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
)

//go:embed stopwords_it.txt
var stopwordsIT string

var stopIT = func() map[string]bool {
	m := map[string]bool{}
	for _, l := range strings.Split(stopwordsIT, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			m[l] = true
		}
	}
	return m
}()

// MinWordRunes is the shortest word the lexicon counts; Italian stop words are never counted (C344.3, D-EARS-20).
const MinWordRunes = 3

func countable(lang, fold string) bool {
	return utf8.RuneCountInString(fold) >= MinWordRunes && !(lang == "it" && stopIT[fold])
}

// TrackDiction is the current tier_b_diction row of one track as the lexicon reads it.
type TrackDiction struct {
	TrackID   string
	Lang      string // ISO 639-1, "und" when unknown
	CreatedAt string
	Lines     []tierb.LineResult
}

// Override is one lexicon_overrides row.
type Override struct {
	Status string
	Note   *string
}

type lexKey struct{ lang, word string }

// maxExamples and maxTypical bound the lists of an entry.
const (
	maxExamples = 3
	maxTypical  = 3
)

type acc struct {
	entry  LexiconEntry
	heard  map[string]int
	tracks map[string]bool
}

// BuildLexicon aggregates the words of every track's lines into the lexicon (C344.3): each reference word of three or more
// runes that is not an Italian stop word is one attempt; a substitution whose fold differs adds a mangled (with what was heard);
// a deletion adds a dropped. A line whose human mark is clear contributes nothing. Overrides set the status. The result is sorted
// risk first, then by mangled, attempts, language and word, so it is deterministic.
func BuildLexicon(tracks []TrackDiction, human map[string]map[LineKey]string, overrides map[[2]string]Override) []LexiconEntry {
	byKey := map[lexKey]*acc{}
	for _, td := range tracks {
		for _, ln := range td.Lines {
			if human[td.TrackID][LineKey{ln.Section, ln.Line}] == MarkClear {
				continue
			}
			for _, w := range ln.Words {
				if !countable(td.Lang, w.Fold) {
					continue
				}
				k := lexKey{td.Lang, w.Fold}
				a := byKey[k]
				if a == nil {
					a = &acc{entry: LexiconEntry{Lang: td.Lang, Word: w.Fold, Status: "active", TypicalHeard: []HeardCount{}, Examples: []LexiconExample{}},
						heard: map[string]int{}, tracks: map[string]bool{}}
					byKey[k] = a
				}
				a.entry.Attempts++
				a.tracks[td.TrackID] = true
				if td.CreatedAt > a.entry.LastSeenAt {
					a.entry.LastSeenAt = td.CreatedAt
				}
				switch w.Op {
				case tierb.OpDelete:
					a.entry.Dropped++
				case tierb.OpSub:
					if w.HeardFold == w.Fold {
						continue
					}
					a.entry.Mangled++
					a.heard[w.HeardFold]++
					if len(a.entry.Examples) < maxExamples {
						a.entry.Examples = append(a.entry.Examples, LexiconExample{TrackID: td.TrackID, AtSecond: round2(w.StartS), Heard: w.HeardFold})
					}
				}
			}
		}
	}
	out := make([]LexiconEntry, 0, len(byKey))
	for k, a := range byKey {
		e := a.entry
		e.Tracks = len(a.tracks)
		e.Rate = r3(float64(e.Mangled) / float64(e.Attempts))
		for h, n := range a.heard {
			e.TypicalHeard = append(e.TypicalHeard, HeardCount{Text: h, N: n})
		}
		sort.Slice(e.TypicalHeard, func(i, j int) bool {
			if e.TypicalHeard[i].N != e.TypicalHeard[j].N {
				return e.TypicalHeard[i].N > e.TypicalHeard[j].N
			}
			return e.TypicalHeard[i].Text < e.TypicalHeard[j].Text
		})
		e.TypicalHeard = e.TypicalHeard[:min(len(e.TypicalHeard), maxTypical)]
		if o, ok := overrides[[2]string{k.lang, k.word}]; ok {
			e.Status, e.Note = o.Status, o.Note
		}
		e.Risk = e.Attempts >= 3 && 2*e.Mangled >= e.Attempts && e.Status == "active"
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.Risk != b.Risk:
			return a.Risk
		case a.Mangled != b.Mangled:
			return a.Mangled > b.Mangled
		case a.Attempts != b.Attempts:
			return a.Attempts > b.Attempts
		case a.Lang != b.Lang:
			return a.Lang < b.Lang
		}
		return a.Word < b.Word
	})
	return out
}
