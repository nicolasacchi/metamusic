package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// EngineDSP is recipes.engine of a transform (T1): the recipe's request_json is the canonical transform spec, not an engine
// request. Every reader that means "a YuE2 recipe" selects engine = 'yue2' (the engine sweep, C211.1c).
const EngineDSP = "dsp"

// SpecRecipe is the recipe of a transform: the canonical spec bytes (which include the spec id), their sha256 (M1's
// recipes.hash), the id inside the spec and the seed it carries.
type SpecRecipe struct {
	Kind      string
	Canonical []byte
	Hash      string
	RequestID string
	Seed      int64
}

// InsertTransformRecipe stores the recipe of a transform once, by hash, in the caller's transaction (C201.4). It is idempotent
// by design: an existing row is returned with created = false and never an error (a transform of the same spec is reused,
// D-T1-2), unlike InsertRecipe for a job. style, lyrics and cot are ” (the human label lives in transforms.title, so no
// style reader can mistake it for style text), core_hash = hash, and no recipe_keys row is written: a transform has no
// content/render split.
func (s *Service) InsertTransformRecipe(ctx context.Context, tx *sql.Tx, r SpecRecipe, createdBy string) (id string, created bool, err error) {
	switch {
	case createdBy == "" || createdBy == "import":
		return "", false, invalid("created_by", "a transform recipe is made by a principal")
	case !oneOf(r.Kind, TransformKinds...):
		return "", false, invalid("kind", "unknown transform kind %q", r.Kind)
	case len(r.Canonical) == 0 || len(r.Hash) != 64 || r.RequestID == "":
		return "", false, invalid("spec", "a transform recipe needs its canonical spec, hash and id")
	case r.Hash != sha256Hex(r.Canonical):
		return "", false, invalid("hash", "the hash is not the sha256 of the canonical spec")
	}
	rid := s.newID()
	err = tx.QueryRowContext(ctx, `INSERT INTO recipes(id, engine, request_id, request_json, style, lyrics, seed, cot,
		sampling_json, hash, core_hash, created_by, created_at) VALUES (?,?,?,?,'','',?,'',NULL,?,?,?,?)
		ON CONFLICT(hash) DO NOTHING RETURNING id`,
		rid, EngineDSP, r.RequestID, string(r.Canonical), r.Seed, r.Hash, r.Hash, createdBy, s.nowStr()).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("library: insert transform recipe: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM recipes WHERE hash = ?`, r.Hash).Scan(&id); err != nil {
		return "", false, fmt.Errorf("library: find transform recipe: %w", err)
	}
	return id, false, nil
}
