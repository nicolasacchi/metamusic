// Package atlas is the Song Atlas: the axes registry, the fixed-point layout, the scope query, the neighbours and the
// requested-versus-got adherence layer (L3 C141). It reads song_features by SQL and imports neither features nor Muse
// code. The result types double as the wire documents (JSON snake_case); types and the method set are frozen by L3-F2 and
// the bodies are lanes AT1 (axes, layout), AT2 (query) and AD (adherence).
package atlas

import "github.com/nicolasacchi/metamusic/internal/songs"

// Kind is the kind of an axis.
type Kind string

// The axis kinds.
const (
	KindNumeric     Kind = "numeric"
	KindCategorical Kind = "categorical"
)

// AxisDef defines one axis of the registry (extension move 2).
type AxisDef struct {
	Name        string
	Kind        Kind
	Unit        string
	Scale       string // linear | log2
	Column      string // song_features column
	BasisColumn string // bpm_basis | energy_basis | "" (bpm_requested and bpm_measured carry a fixed basis)
	Min, Max    int    // numeric domain in the column's fixed-point units
	Order       []string
}

// Query is the input of Get. Limit is already resolved by the caller (400 for human principals, 100 for others).
type Query struct {
	X, Y, ColorBy string
	Filter        songs.ListFilter
	Dim           bool
	NearSongID    string
	K, Limit      int
	Cursor        string
}

// Category is one band of a categorical axis.
type Category struct {
	Value string  `json:"value"`
	Lo    float64 `json:"lo"`
	Hi    float64 `json:"hi"`
	Count int     `json:"count"`
}

// AxisInfo describes an axis of one response.
type AxisInfo struct {
	Name       string     `json:"name"`
	Kind       Kind       `json:"kind" enum:"numeric,categorical"`
	Unit       string     `json:"unit,omitempty"`
	Scale      string     `json:"scale,omitempty"`
	Min        *int       `json:"min,omitempty"`
	Max        *int       `json:"max,omitempty"`
	Categories []Category `json:"categories,omitempty"`
}

// Guide is a drawing hint (for example an octave line).
type Guide struct {
	Kind string `json:"kind"`
}

// LegendEntry is one colour of the legend.
type LegendEntry struct {
	C     int    `json:"c"`
	Value string `json:"value"`
	Count int    `json:"count"`
}

// Missing counts the in-scope Songs without a value per axis.
type Missing struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// BasisCounts counts plotted values per basis: measured, declared, plan, predicted.
type BasisCounts struct {
	M int `json:"m"`
	D int `json:"d"`
	L int `json:"l"`
	P int `json:"p"`
}

// BasisPair holds the basis counts of both axes.
type BasisPair struct {
	X BasisCounts `json:"x"`
	Y BasisCounts `json:"y"`
}

// Totals is the honesty layer: how many Songs were in scope, plotted and missing.
type Totals struct {
	Scope      int       `json:"scope" doc:"Songs matching the filter"`
	ScopeTotal int       `json:"scope_total"`
	Truncated  bool      `json:"truncated"`
	Plotted    int       `json:"plotted"`
	Hit        int       `json:"hit"`
	Missing    Missing   `json:"missing"`
	Basis      BasisPair `json:"basis"`
}

// Point is one plotted Song: t title, x and y in [0, 1], c colour index, mi Muse index, st state, pk pick source, xb and yb
// bases, h highlight (only with dim).
type Point struct {
	ID string  `json:"id"`
	T  string  `json:"t"`
	X  float64 `json:"x"`
	Y  float64 `json:"y"`
	C  int     `json:"c"`
	MI *int    `json:"mi,omitempty"`
	ST int     `json:"st" doc:"0 draft, 1 rendered, 2 judged, 3 picked"`
	PK int     `json:"pk" doc:"0 none, 1 newest, 2 stars, 3 ladder, 4 pinned"`
	XB string  `json:"xb,omitempty" enum:"m,d,l,p"`
	YB string  `json:"yb,omitempty" enum:"m,d,l,p"`
	H  *int    `json:"h,omitempty"`
}

// NeighbourParts are the three cost parts of a neighbour distance.
type NeighbourParts struct {
	Tempo  int64 `json:"tempo"`
	Key    int64 `json:"key"`
	Energy int64 `json:"energy"`
}

// Neighbour is a Song near near_song_id under sequencer.Distance.
type Neighbour struct {
	ID       string         `json:"id"`
	Title    string         `json:"title"`
	Distance int64          `json:"distance"`
	Parts    NeighbourParts `json:"parts"`
}

// Atlas is the mm.atlas/1 document.
type Atlas struct {
	Def             string        `json:"def"`
	GeneratedAt     string        `json:"generated_at"`
	TaxonomyVersion int           `json:"taxonomy_version"`
	LayoutToken     string        `json:"layout_token"`
	Pending         bool          `json:"pending"`
	Axes            AxisPair      `json:"axes"`
	Guides          []Guide       `json:"guides" nullable:"false"`
	ColorBy         string        `json:"color_by"`
	Legend          []LegendEntry `json:"legend" nullable:"false"`
	Muses           []string      `json:"muses" nullable:"false" doc:"Muse ids only, for color_by=muse"`
	Totals          Totals        `json:"totals"`
	Points          []Point       `json:"points" nullable:"false"`
	Neighbors       []Neighbour   `json:"neighbors,omitempty"`
	NextCursor      *string       `json:"next_cursor" nullable:"true"`
}

// AxisPair holds the two axes of a response.
type AxisPair struct {
	X AxisInfo `json:"x"`
	Y AxisInfo `json:"y"`
}

// AdherenceQuery is the input of Adherence (C141.4).
type AdherenceQuery struct {
	Facet, Value string
	MinN         int
	MinErrorPct  *float64
	Octave       *bool
	Limit        int
	Cursor       string
}

// AdherenceCoverage counts the Songs behind an adherence answer.
type AdherenceCoverage struct {
	Songs        int `json:"songs"`
	WithPair     int `json:"with_pair"`
	WithDuration int `json:"with_duration"`
}

// AdherenceSummary is the overall requested-versus-got summary.
type AdherenceSummary struct {
	N               int     `json:"n"`
	Within5PctShare float64 `json:"within_5pct_share"`
	OctaveShare     float64 `json:"octave_share"`
	MedianErrorPct  float64 `json:"median_error_pct"`
}

// AdherenceBPM is the tempo part of one facet value.
type AdherenceBPM struct {
	N               int     `json:"n"`
	Within5PctShare float64 `json:"within_5pct_share"`
	MedianErrorPct  float64 `json:"median_error_pct"`
	OctaveShare     float64 `json:"octave_share"`
	MeanRequested   float64 `json:"mean_requested"`
	MeanGot         float64 `json:"mean_got"`
}

// AdherenceDuration is the duration part of one facet value.
type AdherenceDuration struct {
	N           int     `json:"n"`
	MedianRatio float64 `json:"median_ratio"`
}

// AdherenceFacet is one (facet, value) aggregate.
type AdherenceFacet struct {
	Facet    string             `json:"facet"`
	Value    string             `json:"value"`
	N        int                `json:"n"`
	BPM      *AdherenceBPM      `json:"bpm,omitempty"`
	Duration *AdherenceDuration `json:"duration,omitempty"`
}

// AdherenceItem is one Song's requested-versus-got line.
type AdherenceItem struct {
	SongID        string   `json:"song_id"`
	TakeID        string   `json:"take_id"`
	Title         string   `json:"title"`
	RequestedBPM  float64  `json:"requested_bpm"`
	MeasuredBPM   float64  `json:"measured_bpm"`
	ErrorPct      float64  `json:"error_pct"`
	Octave        bool     `json:"octave"`
	DurationRatio *float64 `json:"duration_ratio" nullable:"true"`
}

// Adherence is the requested-versus-got answer.
type Adherence struct {
	DimsAvailable []string          `json:"dims_available" nullable:"false"`
	Coverage      AdherenceCoverage `json:"coverage"`
	Summary       AdherenceSummary  `json:"summary"`
	ByFacet       []AdherenceFacet  `json:"by_facet" nullable:"false"`
	Items         []AdherenceItem   `json:"items" nullable:"false"`
	NextCursor    *string           `json:"next_cursor" nullable:"true"`
}
