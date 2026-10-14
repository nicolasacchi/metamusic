package lineage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

// ErrNoFreeID means every candidate id of a take is taken (422 INVALID_ITEM rule id_exhausted).
var ErrNoFreeID = errors.New("lineage: no free request id")

// maxIDLen is the longest request id (yue2's id regex).
const maxIDLen = 180

// AllocateID names a take's request id (C71.3, D-M3-10). keep && !taken(base) keeps the base id. Otherwise
// the stem is SeedFamily(base) cut so that the longest candidate ("-s<seed>-99") fits 180 bytes at a rune
// boundary, trailing [-_.] trimmed; the candidate is stem-s<seed>, then -2 … -99.
func AllocateID(base string, seed int64, keep bool, taken func(id string) bool) (string, error) {
	if keep && !taken(base) {
		return base, nil
	}
	suffix := "-s" + strconv.FormatInt(seed, 10)
	stem := yue2.SeedFamily(base)
	limit := maxIDLen - len(suffix) - len("-99")
	if len(stem) > limit {
		stem = stem[:limit]
		for len(stem) > 0 && !utf8.ValidString(stem) {
			stem = stem[:len(stem)-1]
		}
	}
	stem = strings.TrimRight(stem, "-_.")
	if stem == "" {
		stem = "take"
	}
	cand := stem + suffix
	if !taken(cand) {
		return cand, nil
	}
	for i := 2; i <= 99; i++ {
		c := cand + "-" + strconv.Itoa(i)
		if !taken(c) {
			return c, nil
		}
	}
	return "", ErrNoFreeID
}

// DraftRow is what ReusableDraftRecipe needs of a draft: its pinned revision's canonical request and
// the frozen recipe it was imported from.
type DraftRow struct {
	ID             string
	Revision       int
	SourceRecipeID string // "" when the draft has no imported recipe
	RequestJSON    string // the draft's canonical request at Revision
}

// terminalJobStates are the terminal job states; every other state is live.
const terminalJobStates = `('completed','failed','cancelled','expired')`

// ReusableDraftRecipe reports whether a draft's own recipe row can be the recipe of its take (D-M3-10):
// the draft has a source recipe, that recipe's canonical hash equals the hash of the draft's pinned
// request (same id and seed), no track uses it and no non-terminal job does. SQL only; it reads through q.
func ReusableDraftRecipe(ctx context.Context, q Querier, d DraftRow) (recipeID string, ok bool, err error) {
	if d.SourceRecipeID == "" {
		return "", false, nil
	}
	var hash, engine string
	switch err := q.QueryRowContext(ctx, `SELECT hash, engine FROM recipes WHERE id = ?`, d.SourceRecipeID).Scan(&hash, &engine); {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("lineage: reusable recipe: %w", err)
	}
	sum := sha256.Sum256([]byte(d.RequestJSON))
	if engine != "yue2" || hex.EncodeToString(sum[:]) != hash {
		return "", false, nil
	}
	var used int
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tracks WHERE recipe_id = ?)
		OR EXISTS(SELECT 1 FROM jobs WHERE recipe_id = ? AND state NOT IN `+terminalJobStates+`)`, d.SourceRecipeID, d.SourceRecipeID).Scan(&used); err != nil { //nolint:gosec // G202: constant state list
		return "", false, fmt.Errorf("lineage: reusable recipe: %w", err)
	}
	if used == 1 {
		return "", false, nil
	}
	return d.SourceRecipeID, true, nil
}
