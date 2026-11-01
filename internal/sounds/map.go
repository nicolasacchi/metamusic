package sounds

import (
	"context"
	"database/sql"
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
// taxonomy version is recomputed before the call answers. Map owns its transactions (D-L3-41): applyTx writes the
// version in one, which commits, then recompute runs the batched loop.
func (s *Service) Map(ctx context.Context, p auth.Principal, req MapRequest) (MapResult, error) {
	entries, err := validateMap(req)
	if err != nil {
		return MapResult{}, err
	}
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return MapResult{}, fmt.Errorf("sounds: map: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, applied, err := s.applyTx(ctx, tx, p, entries, req.Note)
	if err != nil || !applied {
		return res, err // an all-unchanged call creates no version
	}
	if err := tx.Commit(); err != nil {
		res.Version, res.MappingsApplied = nil, 0
		return res, fmt.Errorf("sounds: map commit: %w", err)
	}
	ch, err := s.recompute(ctx, p)
	res.RecomputedTakes, res.RecomputedSongs = ch.Takes, ch.Songs
	return res, err
}

// applyTx inserts one extension version with the effective part of entries (already validated and folded) inside tx and
// reports whether a version was written; the caller commits. Entries already effective count as Unchanged, and a remove
// that is not effective is 422 MAPPING_NOT_EFFECTIVE. Nothing is recomputed here.
func (s *Service) applyTx(ctx context.Context, tx *sql.Tx, p auth.Principal, entries []MapEntry, note *string) (MapResult, bool, error) {
	var res MapResult
	cur, err := currentVersion(ctx, tx)
	if err != nil {
		return res, false, err
	}
	rows, err := loadTermRows(ctx, tx, cur)
	if err != nil {
		return res, false, err
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
			return res, false, problem.E(422, "MAPPING_NOT_EFFECTIVE", "the mapping to remove is not effective", map[string]any{"facet": e.Facet, "value": e.Value})
		default:
			apply = append(apply, e)
		}
	}
	if len(apply) == 0 {
		return res, false, nil
	}
	v := cur + 1
	if _, err := tx.ExecContext(ctx, `INSERT INTO sound_taxonomy_versions(version, kind, created_by, created_at, note) VALUES(?, 'extension', ?, ?, ?)`,
		v, p.Key(), store.FormatTime(s.now()), note); err != nil {
		return res, false, fmt.Errorf("sounds: map version: %w", err)
	}
	for _, e := range apply {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sound_terms(version, term, facet, value, op) VALUES(?, ?, ?, ?, ?)`, v, e.Term, e.Facet, e.Value, e.Op); err != nil {
			return res, false, fmt.Errorf("sounds: map term: %w", err)
		}
	}
	res.Version = &v
	res.MappingsApplied = len(apply)
	return res, true, nil
}

// recompute recomputes every subject behind the current taxonomy version, BatchKeys per transaction, until none is
// stale, and returns what it did; on an error the counts so far are returned with it. Each batch commits on its own and
// calls the recompute hook inside its transaction (D-L3-41).
func (s *Service) recompute(ctx context.Context, p auth.Principal) (Changes, error) {
	var total Changes
	var swc sweepCursor
	for {
		ch, err := s.recomputeBatch(ctx, p, &swc)
		if err != nil {
			return total, err
		}
		total.Takes += ch.Takes
		total.Songs += ch.Songs
		total.SongIDs = append(total.SongIDs, ch.SongIDs...)
		if ch.Takes+ch.Songs == 0 {
			return total, nil
		}
	}
}

// BatchKeys is the number of subjects one recomputation transaction handles.
const BatchKeys = 100

// recomputeBatch recomputes up to BatchKeys subjects behind the current taxonomy version in its own transaction. With a
// recompute hook set it hands the hook the Songs the batch rewrote before committing; a hook error aborts the batch.
func (s *Service) recomputeBatch(ctx context.Context, p auth.Principal, cur *sweepCursor) (Changes, error) {
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return Changes{}, fmt.Errorf("sounds: recompute: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	env := Env{Now: s.now(), NewID: s.newID, Actor: p.Key()}
	hook := s.recomputeHook()
	var candidates []string
	afterSong := cur.song
	if hook != nil {
		if candidates, err = s.staleSongs(ctx, tx, afterSong); err != nil {
			return Changes{}, err
		}
	}
	ch, err := s.sweep(ctx, tx, env, BatchKeys, false, cur)
	if err != nil {
		return ch, err
	}
	if hook != nil && ch.Songs > 0 {
		// sweep handles stale Songs in id order and advances the cursor to the last one it rewrote, so the rewritten
		// Songs are the candidates up to the cursor.
		for _, id := range candidates {
			if id > afterSong && id <= cur.song {
				ch.SongIDs = append(ch.SongIDs, id)
			}
		}
		if len(ch.SongIDs) > 0 {
			if err := hook(ctx, tx, env, ch.SongIDs); err != nil {
				return ch, err
			}
		}
	}
	return ch, tx.Commit()
}

// staleSongs is the list sweep starts from: live Songs after the cursor without a state row at the current version,
// at most BatchKeys, in id order (the same predicate as sweep's own query).
func (s *Service) staleSongs(ctx context.Context, tx *sql.Tx, after string) ([]string, error) {
	ix, err := s.indexIn(ctx, tx)
	if err != nil {
		return nil, err
	}
	return stringsOf(ctx, tx, `SELECT x.id FROM songs x WHERE x.retired_at IS NULL AND x.id > ? AND NOT EXISTS (SELECT 1 FROM sound_state ss
		WHERE ss.subject_kind = 'song' AND ss.subject_id = x.id AND ss.taxonomy_version = ?) ORDER BY x.id LIMIT ?`, after, ix.Version, BatchKeys)
}

// recomputeHook is the hook set by SetRecomputeHook, nil when none.
func (s *Service) recomputeHook() RecomputeHook {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hook
}
