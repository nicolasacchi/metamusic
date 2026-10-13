package library

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/canonjson"
)

// Reference tracks and lossy masters (M2c, D-M2c-1).
//
// A reference track is an ordinary track whose recipe is a stub with
// engine = 'unknown'. Code that reads recipes.seed, or groups tracks by request
// id (seed families, "same seed" features, mothers), must check RecipeKnown
// first: a stub's seed is the placeholder 0, which is also a valid real seed,
// so seed = 0 is never the discriminator. A future seed-family feature must
// filter on recipe_known.

// EngineUnknown is recipes.engine of a stub recipe.
const EngineUnknown = "unknown"

// RuleReference is tracks.import_rule of a reference track.
const RuleReference = "reference"

// maxStubTitleRunes is the cut applied to a stub recipe's title.
const maxStubTitleRunes = 200

var (
	// ErrRecipeUnknown marks an operation that needs a recipe on a reference track.
	ErrRecipeUnknown = errors.New("library: RECIPE_UNKNOWN")
	// ErrLossyMaster marks an operation that needs a lossless master on a lossy track.
	ErrLossyMaster = errors.New("library: LOSSY_MASTER")
)

// RecipeUnknownError carries the offending track.
type RecipeUnknownError struct{ TrackID string }

func (e *RecipeUnknownError) Error() string        { return "library: RECIPE_UNKNOWN" }
func (e *RecipeUnknownError) Is(target error) bool { return target == ErrRecipeUnknown }

// LossyMasterError carries the offending track.
type LossyMasterError struct{ TrackID string }

func (e *LossyMasterError) Error() string        { return "library: LOSSY_MASTER" }
func (e *LossyMasterError) Is(target error) bool { return target == ErrLossyMaster }

// RecipeKnownEngine reports whether a recipe with this engine is a real recipe.
func RecipeKnownEngine(engine string) bool { return engine != EngineUnknown }

// RecipeKnown reports whether the track's recipe is a real one. ErrNotFound for an unknown id.
func (s *Service) RecipeKnown(ctx context.Context, trackID string) (bool, error) {
	var engine string
	err := s.st.R.QueryRowContext(ctx, `SELECT r.engine FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE t.id = ?`, trackID).Scan(&engine)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("library: recipe known: %w", err)
	}
	return RecipeKnownEngine(engine), nil
}

// RequireRecipeKnown returns a *RecipeUnknownError for a reference track.
func (s *Service) RequireRecipeKnown(ctx context.Context, trackID string) error {
	ok, err := s.RecipeKnown(ctx, trackID)
	if err != nil {
		return err
	}
	if !ok {
		return &RecipeUnknownError{TrackID: trackID}
	}
	return nil
}

// LosslessMaster reports whether the track's master is a lossless file (!tracks.lossy).
func (s *Service) LosslessMaster(ctx context.Context, trackID string) (bool, error) {
	var lossy int
	err := s.st.R.QueryRowContext(ctx, `SELECT lossy FROM tracks WHERE id = ?`, trackID).Scan(&lossy)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("library: lossless master: %w", err)
	}
	return lossy == 0, nil
}

// RequireLosslessMaster returns a *LossyMasterError for a lossy track.
func (s *Service) RequireLosslessMaster(ctx context.Context, trackID string) error {
	ok, err := s.LosslessMaster(ctx, trackID)
	if err != nil {
		return err
	}
	if !ok {
		return &LossyMasterError{TrackID: trackID}
	}
	return nil
}

// NewReferenceRecipe describes a stub recipe. AudioKey is the importer's audio id.
type NewReferenceRecipe struct{ Title, Slug, AudioKey string }

// ReferenceRequestID is "ref_" + slug + "_" + the first 8 hex digits of sha256(audioKey).
func ReferenceRequestID(slug, audioKey string) string {
	h := sha256.Sum256([]byte(audioKey))
	return "ref_" + slug + "_" + hex.EncodeToString(h[:])[:8]
}

// ReferenceStubJSON is the canonical request_json of a stub recipe and its hash
// (the importer's dry run computes it to cover exactly what apply writes).
func ReferenceStubJSON(requestID, title string) (canonical []byte, hash string, err error) {
	raw, err := json.Marshal(map[string]any{"id": requestID, "seed": 0, "title": cutRunes(title, maxStubTitleRunes)})
	if err != nil {
		return nil, "", err
	}
	canonical, err = canonjson.Canonical(raw)
	if err != nil {
		return nil, "", err
	}
	return canonical, sha256Hex(canonical), nil
}

func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// InsertReferenceRecipe stores a stub recipe once (by canonical hash).
func (s *Service) InsertReferenceRecipe(ctx context.Context, tx *sql.Tx, r NewReferenceRecipe) (id string, created bool, err error) {
	reqID := ReferenceRequestID(r.Slug, r.AudioKey)
	canon, hash, err := ReferenceStubJSON(reqID, r.Title)
	if err != nil {
		return "", false, fmt.Errorf("library: stub recipe: %w", err)
	}
	rid := s.newID()
	err = tx.QueryRowContext(ctx, `INSERT INTO recipes(id, engine, request_id, request_json, style, lyrics, seed, cot,
		sampling_json, hash, core_hash, created_by, created_at) VALUES (?,?,?,?,'','',0,'',NULL,?,?,?,?)
		ON CONFLICT(hash) DO NOTHING RETURNING id`,
		rid, EngineUnknown, reqID, string(canon), hash, hash, "import", s.nowStr()).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("library: insert stub recipe: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM recipes WHERE hash = ?`, hash).Scan(&id); err != nil {
		return "", false, fmt.Errorf("library: find stub recipe: %w", err)
	}
	return id, false, nil
}
