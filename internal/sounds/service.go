package sounds

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// FacetValueCount is one value of a facet with its Song count (C93.5); Mismatches is reported for tempo_band.
type FacetValueCount struct {
	Value      string
	Count      int
	Mismatches *int
}

// FacetCounts is the answer of search_sounds' facet part (C93.5).
type FacetCounts struct {
	TaxonomyVersion int
	Selected        map[string][]string
	Facets          map[string][]FacetValueCount
	SongsMatching   int
}

// TaxonomyQuery is the input of Taxonomy (C93.6).
type TaxonomyQuery struct {
	Facet, Q string
	Unmapped bool
	Limit    int
	Cursor   string
}

// TaxonomyItem is one effective mapping.
type TaxonomyItem struct {
	Term, Facet, Value string
	SinceVersion       int
}

// TaxonomyVersion is one version row.
type TaxonomyVersion struct {
	Version              int
	Kind                 string
	SeedID               *string
	CreatedBy, CreatedAt string
	Note                 *string
}

// UnmappedTerm is a style part no phrase matched, with the number of live Songs carrying it.
type UnmappedTerm struct {
	Term  string
	Songs int
}

// TaxonomyPage is the answer of Taxonomy.
type TaxonomyPage struct {
	CurrentVersion int
	Versions       []TaxonomyVersion
	Items          []TaxonomyItem
	Unmapped       []UnmappedTerm
	NextCursor     *string
}

// MapEntry is one requested mapping change.
type MapEntry struct{ Term, Facet, Value, Op string }

// MapRequest is the input of Map (C93.2).
type MapRequest struct {
	Mappings []MapEntry
	Note     *string
}

// MapResult is the outcome of Map.
type MapResult struct {
	Version                          *int
	MappingsApplied, Unchanged       int
	RecomputedTakes, RecomputedSongs int
}

// Changes is what a compute step did, for the Reconciler report.
type Changes struct{ Takes, Songs int }

// Service is the Sound service (C93). The method set is frozen by L1-B2; the bodies are L1-B6's (STUB(L1-B6)).
type Service struct {
	st    *store.Store
	now   func() time.Time
	newID func() string
}

// New builds the Service.
func New(st *store.Store, now func() time.Time, newID func() string) *Service {
	return &Service{st: st, now: now, newID: newID}
}

// EnsureSeed applies every unapplied code seed as one taxonomy version (C93.2).
func (s *Service) EnsureSeed(ctx context.Context) error {
	return errors.ErrUnsupported // STUB(L1-B6)
}

// Index returns the effective taxonomy index, cached per version.
func (s *Service) Index(ctx context.Context) (*Index, error) {
	return nil, errors.ErrUnsupported // STUB(L1-B6)
}

// Facets counts the seven facets over the Songs matching f (C93.5).
func (s *Service) Facets(ctx context.Context, f songs.ListFilter) (FacetCounts, error) {
	return FacetCounts{}, errors.ErrUnsupported // STUB(L1-B6)
}

// Taxonomy pages the effective mappings, the version list or the unmapped terms (C93.6).
func (s *Service) Taxonomy(ctx context.Context, q TaxonomyQuery) (TaxonomyPage, error) {
	return TaxonomyPage{}, errors.ErrUnsupported // STUB(L1-B6)
}

// Map adds an extension version and recomputes the affected subjects (C93.2).
func (s *Service) Map(ctx context.Context, p auth.Principal, req MapRequest) (MapResult, error) {
	return MapResult{}, errors.ErrUnsupported // STUB(L1-B6)
}

// ComputeTx recomputes the facets of the dirty takes and the affected Songs inside the Reconciler's transaction (C93.4).
func (s *Service) ComputeTx(ctx context.Context, tx *sql.Tx, env Env, trackIDs, songIDs []string) (Changes, error) {
	return Changes{}, errors.ErrUnsupported // STUB(L1-B6)
}

// SweepStaleTx recomputes up to limit stale subjects (stale digest or taxonomy version).
func (s *Service) SweepStaleTx(ctx context.Context, tx *sql.Tx, env Env, limit int) (n int, err error) {
	return 0, errors.ErrUnsupported // STUB(L1-B6)
}
