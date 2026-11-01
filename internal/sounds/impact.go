package sounds

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

// ImpactMaxSongs bounds the Songs one impact preview re-parses (C140.2); more live Songs set Impact.Truncated.
const ImpactMaxSongs = 20000

// Mapping is one requested taxonomy change: the shipped MapEntry (validation is validateMap, C93.2).
type Mapping = MapEntry

// FacetCount is the number of Songs that gain or lose one (facet, value) row.
type FacetCount struct {
	Facet string `json:"facet"`
	Value string `json:"value"`
	Songs int    `json:"songs"`
}

// TermCount is the number of Songs whose unmapped list loses a term.
type TermCount struct {
	Term  string `json:"term"`
	Songs int    `json:"songs"`
}

// Impact is what a set of mappings would change over the live Songs (C140.2). TaxonomyVersion is the version the
// preview was computed against; the slices are never nil.
type Impact struct {
	TaxonomyVersion int          `json:"taxonomy_version"`
	SongsScanned    int          `json:"songs_scanned"`
	Truncated       bool         `json:"truncated"`
	Gain            []FacetCount `json:"gain"`
	Loss            []FacetCount `json:"loss"`
	UnmappedCleared []TermCount  `json:"unmapped_cleared"`
}

// Impact is a dry run: it applies ms (already validated and folded; mappings that are not effective changes are
// ignored) on top of the current effective taxonomy in memory and reports what the real recompute would change.
// It reads one consistent snapshot and writes nothing.
func (s *Service) Impact(ctx context.Context, ms []Mapping) (Impact, error) {
	tx, err := s.st.R.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Impact{}, fmt.Errorf("sounds: impact: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	return s.impactIn(ctx, tx, ms)
}

// hypothetical folds the effective part of ms into rows as the next version (the rows Map would insert).
func hypothetical(cur int, rows []TermRow, ms []Mapping) (old, hyp *Index) {
	old = BuildIndex(cur, rows)
	next := slices.Clone(rows)
	for _, m := range ms {
		term := NormTerm(m.Term)
		if has := old.Has(term, m.Facet, m.Value); (m.Op == "add" && !has) || (m.Op == "remove" && has) {
			next = append(next, TermRow{Version: cur + 1, Term: term, Facet: m.Facet, Value: m.Value, Op: m.Op})
		}
	}
	return old, BuildIndex(cur+1, next)
}

// songView is what a taxonomy index makes of one Song's subject: its (facet, value) pairs and unmapped style parts.
func songView(ix *Index, in input) (map[[2]string]bool, map[string]bool) {
	rows, parsed := buildRows(ix, in)
	pairs := make(map[[2]string]bool, len(rows))
	for _, r := range rows {
		if r.Facet != "tempo_band" { // the Song's collapsed tempo row is the same under both indexes
			pairs[[2]string{r.Facet, r.Value}] = true
		}
	}
	un := make(map[string]bool, len(parsed.Unmapped))
	for _, u := range parsed.Unmapped {
		un[u] = true
	}
	return pairs, un
}

// impactIn computes the preview inside tx: for up to ImpactMaxSongs live Songs, newest first, it re-parses the style of
// the Song's representative subject (the exact input of computeSong) under the current and the hypothetical index and
// counts the rows gained and lost and the unmapped parts cleared. The unmapped part is derived from the same Parse
// instead of the song_unmapped_terms cache so the preview also holds for Songs the cache has not caught up with.
func (s *Service) impactIn(ctx context.Context, tx *sql.Tx, ms []Mapping) (Impact, error) {
	cur, err := currentVersion(ctx, tx)
	if err != nil {
		return Impact{}, err
	}
	rows, err := loadTermRows(ctx, tx, cur)
	if err != nil {
		return Impact{}, err
	}
	oldIx, newIx := hypothetical(cur, rows, ms)
	imp := Impact{TaxonomyVersion: cur, Gain: []FacetCount{}, Loss: []FacetCount{}, UnmappedCleared: []TermCount{}}
	ids, err := stringsOf(ctx, tx, `SELECT id FROM songs WHERE retired_at IS NULL ORDER BY created_at DESC, id DESC LIMIT ?`, ImpactMaxSongs+1)
	if err != nil {
		return imp, err
	}
	if len(ids) > ImpactMaxSongs {
		ids, imp.Truncated = ids[:ImpactMaxSongs], true
	}
	imp.SongsScanned = len(ids)
	c := newStmts(tx)
	defer c.Close()
	gain, loss := map[[2]string]int{}, map[[2]string]int{}
	cleared := map[string]int{}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return imp, err
		}
		sub, err := loadSubject(ctx, c, cur, id)
		if err != nil {
			return imp, err
		}
		if sub.Kind == SubjectNone {
			continue
		}
		oldPairs, oldUn := songView(oldIx, sub.In)
		newPairs, newUn := songView(newIx, sub.In)
		for k := range newPairs {
			if !oldPairs[k] {
				gain[k]++
			}
		}
		for k := range oldPairs {
			if !newPairs[k] {
				loss[k]++
			}
		}
		for t := range oldUn {
			if !newUn[t] {
				cleared[t]++
			}
		}
	}
	imp.Gain, imp.Loss = facetCounts(gain), facetCounts(loss)
	for t, n := range cleared {
		imp.UnmappedCleared = append(imp.UnmappedCleared, TermCount{Term: t, Songs: n})
	}
	slices.SortFunc(imp.UnmappedCleared, func(a, b TermCount) int {
		if a.Songs != b.Songs {
			return b.Songs - a.Songs
		}
		return strings.Compare(a.Term, b.Term)
	})
	return imp, nil
}

// facetCounts orders by Songs descending, then facet and value.
func facetCounts(m map[[2]string]int) []FacetCount {
	out := make([]FacetCount, 0, len(m))
	for k, n := range m {
		out = append(out, FacetCount{Facet: k[0], Value: k[1], Songs: n})
	}
	slices.SortFunc(out, func(a, b FacetCount) int {
		if a.Songs != b.Songs {
			return b.Songs - a.Songs
		}
		if c := strings.Compare(a.Facet, b.Facet); c != 0 {
			return c
		}
		return strings.Compare(a.Value, b.Value)
	})
	return out
}
