package sounds

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/problem"
)

// DefaultTaxonomyLimit and MaxTaxonomyLimit bound a Taxonomy page (C93.6).
const (
	DefaultTaxonomyLimit = 100
	MaxTaxonomyLimit     = 200
	versionListLen       = 20
)

func badCursor() error {
	return problem.E(422, "INVALID_CURSOR", "the cursor is not valid", nil)
}

func encodeCursor(parts ...string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join(parts, "\x1f")))
}

func decodeCursor(c string, n int) ([]string, bool) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return make([]string, n), false
	}
	parts := strings.SplitN(string(b), "\x1f", n)
	if len(parts) != n {
		return make([]string, n), false
	}
	return parts, true
}

// Taxonomy pages the effective mappings, the version list or the unmapped terms (C93.6).
func (s *Service) Taxonomy(ctx context.Context, q TaxonomyQuery) (TaxonomyPage, error) {
	limit := q.Limit
	switch {
	case limit == 0:
		limit = DefaultTaxonomyLimit
	case limit < 1 || limit > MaxTaxonomyLimit:
		return TaxonomyPage{}, problem.E(422, "INVALID_LIMIT", fmt.Sprintf("limit must be 1..%d", MaxTaxonomyLimit), nil)
	}
	if q.Facet != "" && !ValidTermFacet(q.Facet) {
		return TaxonomyPage{}, problem.E(422, "INVALID_FACET", "facet must be one of the mappable facets", map[string]any{"facet": q.Facet})
	}
	cur, err := currentVersion(ctx, s.st.R)
	if err != nil {
		return TaxonomyPage{}, err
	}
	page := TaxonomyPage{CurrentVersion: cur}
	if q.Unmapped {
		page.Unmapped, page.NextCursor, err = s.unmappedPage(ctx, q, limit)
		return page, err
	}
	if page.Versions, err = s.versions(ctx); err != nil {
		return page, err
	}
	rows, err := loadTermRows(ctx, s.st.R, cur)
	if err != nil {
		return page, err
	}
	all := effectiveItems(rows)
	needle := NormTerm(q.Q)
	var after [3]string
	if q.Cursor != "" {
		p, ok := decodeCursor(q.Cursor, 3)
		if !ok {
			return page, badCursor()
		}
		after = [3]string{p[0], p[1], p[2]}
	}
	for _, it := range all {
		if q.Facet != "" && it.Facet != q.Facet || needle != "" && !strings.Contains(it.Term, needle) {
			continue
		}
		if q.Cursor != "" && !itemAfter(it, after) {
			continue
		}
		if len(page.Items) == limit {
			c := encodeCursor(page.Items[limit-1].Facet, page.Items[limit-1].Value, page.Items[limit-1].Term)
			page.NextCursor = &c
			break
		}
		page.Items = append(page.Items, it)
	}
	return page, nil
}

func itemAfter(it TaxonomyItem, a [3]string) bool {
	if it.Facet != a[0] {
		return it.Facet > a[0]
	}
	if it.Value != a[1] {
		return it.Value > a[1]
	}
	return it.Term > a[2]
}

// effectiveItems folds the rows into the effective mappings ordered by facet, value, term; SinceVersion is the version
// of the add that made the mapping effective.
func effectiveItems(rows []TermRow) []TaxonomyItem {
	type key struct{ term, facet, value string }
	since := map[key]int{}
	for _, r := range rows { // rows are ordered by version
		k := key{r.Term, r.Facet, r.Value}
		switch r.Op {
		case "add":
			if _, ok := since[k]; !ok {
				since[k] = r.Version
			}
		case "remove":
			delete(since, k)
		}
	}
	out := make([]TaxonomyItem, 0, len(since))
	for k, v := range since {
		out = append(out, TaxonomyItem{Term: k.term, Facet: k.facet, Value: k.value, SinceVersion: v})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Facet != b.Facet {
			return a.Facet < b.Facet
		}
		if a.Value != b.Value {
			return a.Value < b.Value
		}
		return a.Term < b.Term
	})
	return out
}

// versions lists the newest taxonomy versions, newest first.
func (s *Service) versions(ctx context.Context) ([]TaxonomyVersion, error) {
	rows, err := s.st.R.QueryContext(ctx, `SELECT version, kind, seed_id, created_by, created_at, note FROM sound_taxonomy_versions ORDER BY version DESC LIMIT ?`, versionListLen)
	if err != nil {
		return nil, fmt.Errorf("sounds: versions: %w", err)
	}
	defer rows.Close()
	var out []TaxonomyVersion
	for rows.Next() {
		var v TaxonomyVersion
		var seed, note sql.NullString
		if err := rows.Scan(&v.Version, &v.Kind, &seed, &v.CreatedBy, &v.CreatedAt, &note); err != nil {
			return nil, err
		}
		if seed.Valid {
			v.SeedID = &seed.String
		}
		if note.Valid {
			v.Note = &note.String
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
