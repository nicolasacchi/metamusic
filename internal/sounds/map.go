package sounds

import (
	"context"
	"fmt"
	"unicode"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type mapKey struct{ term, facet, value string }

// validateMap checks and normalises the entries of a Map call (C93.2): terms are folded, nothing is applied.
func validateMap(req MapRequest) ([]MapEntry, error) {
	if n := len(req.Mappings); n == 0 || n > MaxMappingsPerCall {
		return nil, problem.E(422, "TOO_MANY_MAPPINGS", fmt.Sprintf("a call carries 1..%d mappings", MaxMappingsPerCall), map[string]any{"count": n})
	}
	if req.Note != nil && (*req.Note == "" || utf8.RuneCountInString(*req.Note) > 500) {
		return nil, problem.E(422, "INVALID_NOTE", "note must be 1..500 characters", nil)
	}
	out := make([]MapEntry, 0, len(req.Mappings))
	ops := map[mapKey]string{}
	for i, m := range req.Mappings {
		at := map[string]any{"index": i}
		term := NormTerm(m.Term)
		hasLetter := false
		for _, r := range term {
			if unicode.IsLetter(r) {
				hasLetter = true
				break
			}
		}
		if n := utf8.RuneCountInString(term); n < 1 || n > 60 || !hasLetter {
			return nil, problem.E(422, "INVALID_TERM", "term must be 1..60 characters after folding and contain a letter", at)
		}
		switch {
		case m.Facet == "tempo_band" || m.Facet == "engine":
			return nil, problem.E(422, "FACET_NOT_MAPPABLE", "tempo_band and engine are computed, not mapped", map[string]any{"index": i, "facet": m.Facet})
		case !ValidTermFacet(m.Facet):
			return nil, problem.E(422, "INVALID_FACET", "facet must be genre_family, era, vocal, mood or language", map[string]any{"index": i, "facet": m.Facet})
		case !ValidValue(m.Facet, m.Value):
			return nil, problem.E(422, "INVALID_VALUE", "value is not allowed for this facet", map[string]any{"index": i, "facet": m.Facet})
		case m.Op != "add" && m.Op != "remove":
			return nil, problem.E(422, "INVALID_OP", "op must be add or remove", at)
		}
		k := mapKey{term, m.Facet, m.Value}
		if prev, dup := ops[k]; dup && prev != m.Op {
			return nil, problem.E(422, "CONFLICTING_MAPPINGS", "one call adds and removes the same mapping", at)
		}
		ops[k] = m.Op
		out = append(out, MapEntry{Term: term, Facet: m.Facet, Value: m.Value, Op: m.Op})
	}
	return out, nil
}

// Map adds an extension version and recomputes the affected subjects (C93.2). Every subject whose digest used an older
// taxonomy version is recomputed before the call answers.
func (s *Service) Map(ctx context.Context, p auth.Principal, req MapRequest) (MapResult, error) {
	entries, err := validateMap(req)
	if err != nil {
		return MapResult{}, err
	}
	var res MapResult
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("sounds: map: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	cur, err := currentVersion(ctx, tx)
	if err != nil {
		return res, err
	}
	rows, err := loadTermRows(ctx, tx, cur)
	if err != nil {
		return res, err
	}
	ix := BuildIndex(cur, rows)
	var apply []MapEntry
	seen := map[mapKey]bool{}
	for _, e := range entries {
		k := mapKey{e.Term, e.Facet, e.Value}
		if seen[k] {
			res.Unchanged++
			continue
		}
		seen[k] = true
		has := ix.Has(e.Term, e.Facet, e.Value)
		switch {
		case e.Op == "add" && has:
			res.Unchanged++
		case e.Op == "remove" && !has:
			return res, problem.E(422, "MAPPING_NOT_EFFECTIVE", "the mapping to remove is not effective", map[string]any{"facet": e.Facet, "value": e.Value})
		default:
			apply = append(apply, e)
		}
	}
	if len(apply) == 0 {
		return res, nil // an all-unchanged call creates no version
	}
	v := cur + 1
	if _, err := tx.ExecContext(ctx, `INSERT INTO sound_taxonomy_versions(version, kind, created_by, created_at, note) VALUES(?, 'extension', ?, ?, ?)`,
		v, p.Key(), store.FormatTime(s.now()), req.Note); err != nil {
		return res, fmt.Errorf("sounds: map version: %w", err)
	}
	for _, e := range apply {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sound_terms(version, term, facet, value, op) VALUES(?, ?, ?, ?, ?)`, v, e.Term, e.Facet, e.Value, e.Op); err != nil {
			return res, fmt.Errorf("sounds: map term: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("sounds: map commit: %w", err)
	}
	res.Version = &v
	res.MappingsApplied = len(apply)
	for {
		ch, err := s.recomputeBatch(ctx, p)
		if err != nil {
			return res, err
		}
		res.RecomputedTakes += ch.Takes
		res.RecomputedSongs += ch.Songs
		if ch.Takes+ch.Songs == 0 {
			return res, nil
		}
	}
}

// BatchKeys is the number of subjects one recomputation transaction handles.
const BatchKeys = 100

// recomputeBatch recomputes up to BatchKeys subjects behind the current taxonomy version in its own transaction.
func (s *Service) recomputeBatch(ctx context.Context, p auth.Principal) (Changes, error) {
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return Changes{}, fmt.Errorf("sounds: recompute: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	env := Env{Now: s.now(), NewID: s.newID, Actor: p.Key()}
	ch, err := s.sweep(ctx, tx, env, BatchKeys, false)
	if err != nil {
		return ch, err
	}
	return ch, tx.Commit()
}
