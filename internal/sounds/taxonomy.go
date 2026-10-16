// Package sounds is the Sound model (C93): facets derived from a take's recipe, plan and Ears measurements, a versioned
// append-only taxonomy of style terms, and the phrase matcher that applies it. Sounds are facets, not folders (D-L1-14).
package sounds

import (
	"regexp"
	"sort"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// Env is the Reconciler environment (C87).
type Env = store.Env

// Facets are the seven Sound facets; sound_terms may map only the first five minus tempo_band (D-L1-15).
var Facets = []string{"genre_family", "era", "tempo_band", "vocal", "mood", "language", "engine"}

// TermFacets are the facets a taxonomy term can map to; tempo_band and engine are computed.
var TermFacets = []string{"genre_family", "era", "vocal", "mood", "language"}

// TempoBands are the values of the tempo_band facet.
var TempoBands = []string{"slow", "mid", "upbeat", "fast", "rapid"}

var (
	eraRe   = regexp.MustCompile(`^(5|6|7|8|9|0|1|2)0s$`)
	langRe  = regexp.MustCompile(`^[a-z]{2}$`)
	valueRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
)

// ValidTermFacet reports whether facet is a mappable one.
func ValidTermFacet(facet string) bool {
	for _, f := range TermFacets {
		if f == facet {
			return true
		}
	}
	return false
}

// ValidFacet reports whether facet is one of the seven.
func ValidFacet(facet string) bool {
	for _, f := range Facets {
		if f == facet {
			return true
		}
	}
	return false
}

// ValidValue reports whether value is allowed for a term-derived facet (C93.2): era ^(5|6|7|8|9|0|1|2)0s$,
// language ^[a-z]{2}$, the others ^[a-z][a-z0-9-]{0,39}$.
func ValidValue(facet, value string) bool {
	switch facet {
	case "era":
		return eraRe.MatchString(value)
	case "language":
		return langRe.MatchString(value)
	case "genre_family", "vocal", "mood":
		return valueRe.MatchString(value)
	}
	return false
}

// Match is one taxonomy mapping that a style phrase triggered.
type Match struct{ Term, Facet, Value string }

// Parsed is the result of Parse.
type Parsed struct {
	Matches  []Match
	Unmapped []string
	BPM      int
	Language string
}

// TermRow is one stored sound_terms row.
type TermRow struct {
	Version            int
	Term, Facet, Value string
	Op                 string // add | remove
}

// FacetValue is an (facet, value) mapping of a phrase.
type FacetValue struct{ Facet, Value string }

// Index is the effective taxonomy at one version: folded phrase -> mappings.
type Index struct {
	Version int
	phrases map[string][]FacetValue
}

// BuildIndex folds the add/remove rows of every version <= version, oldest first (D-L1-15). Within one version the
// rows apply in the order given.
func BuildIndex(version int, rows []TermRow) *Index {
	rs := make([]TermRow, 0, len(rows))
	for _, r := range rows {
		if r.Version <= version {
			rs = append(rs, r)
		}
	}
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Version < rs[j].Version })
	ix := &Index{Version: version, phrases: map[string][]FacetValue{}}
	for _, r := range rs {
		fv := FacetValue{r.Facet, r.Value}
		cur := ix.phrases[r.Term]
		switch r.Op {
		case "add":
			dup := false
			for _, c := range cur {
				if c == fv {
					dup = true
				}
			}
			if !dup {
				ix.phrases[r.Term] = append(cur, fv)
			}
		case "remove":
			out := cur[:0:0]
			for _, c := range cur {
				if c != fv {
					out = append(out, c)
				}
			}
			if len(out) == 0 {
				delete(ix.phrases, r.Term)
			} else {
				ix.phrases[r.Term] = out
			}
		}
	}
	return ix
}

// Has reports whether the mapping (term, facet, value) is effective.
func (ix *Index) Has(term, facet, value string) bool {
	for _, c := range ix.phrases[term] {
		if c.Facet == facet && c.Value == value {
			return true
		}
	}
	return false
}

// Len is the number of effective (term, facet, value) mappings.
func (ix *Index) Len() int {
	n := 0
	for _, v := range ix.phrases {
		n += len(v)
	}
	return n
}

// TempoDetail is the detail of the collapsed tempo_band row (D-L1-17).
type TempoDetail struct {
	RequestedBPM *float64 `json:"requested_bpm"`
	PlanBPM      *float64 `json:"plan_bpm"`
	MeasuredBPM  *float64 `json:"measured_bpm"`
	Basis        string   `json:"basis" enum:"measured,declared,plan"`
	Mismatch     bool     `json:"mismatch"`
	Octave       bool     `json:"octave"`
}
