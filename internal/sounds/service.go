package sounds

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

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

// Service is the Sound service (C93): the versioned taxonomy, the derived facets and the faceted counts.
type Service struct {
	st    *store.Store
	now   func() time.Time
	newID func() string

	mu sync.Mutex
	ix *Index // the effective index at its Version, rebuilt when a newer version exists
}

// New builds the Service.
func New(st *store.Store, now func() time.Time, newID func() string) *Service {
	return &Service{st: st, now: now, newID: newID}
}

// querier is the read surface shared by *sql.DB and *sql.Tx.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// currentVersion is the newest taxonomy version (0 before the first seed).
func currentVersion(ctx context.Context, q querier) (int, error) {
	var v int
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM sound_taxonomy_versions`).Scan(&v); err != nil {
		return 0, fmt.Errorf("sounds: current version: %w", err)
	}
	return v, nil
}

// loadTermRows reads every sound_terms row up to version, oldest first.
func loadTermRows(ctx context.Context, q querier, version int) ([]TermRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT version, term, facet, value, op FROM sound_terms WHERE version <= ? ORDER BY version, term, facet, value`, version)
	if err != nil {
		return nil, fmt.Errorf("sounds: load terms: %w", err)
	}
	defer rows.Close()
	var out []TermRow
	for rows.Next() {
		var r TermRow
		if err := rows.Scan(&r.Version, &r.Term, &r.Facet, &r.Value, &r.Op); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// indexIn returns the effective index at the newest version visible to q, cached per version.
func (s *Service) indexIn(ctx context.Context, q querier) (*Index, error) {
	v, err := currentVersion(ctx, q)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.ix != nil && s.ix.Version == v {
		ix := s.ix
		s.mu.Unlock()
		return ix, nil
	}
	s.mu.Unlock()
	rows, err := loadTermRows(ctx, q, v)
	if err != nil {
		return nil, err
	}
	ix := BuildIndex(v, rows)
	s.mu.Lock()
	if s.ix == nil || s.ix.Version < ix.Version {
		s.ix = ix
	}
	s.mu.Unlock()
	return ix, nil
}

// Index returns the effective taxonomy index, cached per version.
func (s *Service) Index(ctx context.Context) (*Index, error) { return s.indexIn(ctx, s.st.R) }

// EnsureSeed applies every unapplied code seed as one taxonomy version (C93.2). The single writer connection
// serialises concurrent starters; the second one finds the seed applied.
func (s *Service) EnsureSeed(ctx context.Context) error {
	for _, seed := range Seeds() {
		if err := s.ensureSeed(ctx, seed); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ensureSeed(ctx context.Context, seed Seed) error {
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sounds: seed: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var one int
	switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM sound_taxonomy_versions WHERE seed_id = ?`, seed.ID).Scan(&one); err {
	case nil:
		return nil
	case sql.ErrNoRows:
	default:
		return fmt.Errorf("sounds: seed: %w", err)
	}
	v, err := currentVersion(ctx, tx)
	if err != nil {
		return err
	}
	v++
	if _, err := tx.ExecContext(ctx, `INSERT INTO sound_taxonomy_versions(version, kind, seed_id, created_by, created_at) VALUES(?, 'seed', ?, 'system:seed', ?)`,
		v, seed.ID, store.FormatTime(s.now())); err != nil {
		return fmt.Errorf("sounds: seed version: %w", err)
	}
	ins, err := tx.PrepareContext(ctx, `INSERT INTO sound_terms(version, term, facet, value, op) VALUES(?, ?, ?, ?, 'add') ON CONFLICT DO NOTHING`)
	if err != nil {
		return fmt.Errorf("sounds: seed terms: %w", err)
	}
	defer func() { _ = ins.Close() }()
	for _, t := range seed.Terms {
		if _, err := ins.ExecContext(ctx, v, NormTerm(t.Term), t.Facet, t.Value); err != nil {
			return fmt.Errorf("sounds: seed term: %w", err)
		}
	}
	return tx.Commit()
}
