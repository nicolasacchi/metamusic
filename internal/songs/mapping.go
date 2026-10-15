package songs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// The take mapping (D-L1-3): a take belongs to the Song that owns its lineage root through song_roots; there is no
// per-take link table. These are the lookups the structural reconcile uses.

// songOfRoot returns the Song that owns root r through song_roots, or "".
func songOfRoot(ctx context.Context, tx *sql.Tx, root string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT song_id FROM song_roots WHERE root_track_id = ?`, root).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("songs: song of root: %w", err)
	}
	return id, nil
}

// rootOf returns the lineage root of a take and its generation.
func rootOf(ctx context.Context, tx *sql.Tx, track string) (root string, gen int, err error) {
	err = tx.QueryRowContext(ctx, `SELECT root_track_id, generation FROM tracks WHERE id = ?`, track).Scan(&root, &gen)
	if err == sql.ErrNoRows {
		return "", 0, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("songs: root of: %w", err)
	}
	return root, gen, nil
}

// liveSongOfDraft returns the live Song of a draft and its primary root, or "".
func liveSongOfDraft(ctx context.Context, tx *sql.Tx, draft string) (id string, root sql.NullString, err error) {
	err = tx.QueryRowContext(ctx, `SELECT id, root_track_id FROM songs WHERE draft_id = ? AND retired_at IS NULL`, draft).Scan(&id, &root)
	if err == sql.ErrNoRows {
		return "", root, nil
	}
	if err != nil {
		return "", root, fmt.Errorf("songs: live song of draft: %w", err)
	}
	return id, root, nil
}

// songRow is the columns the reconcile reads of a Song.
type songRow struct {
	ID                      string
	Root, Draft             sql.NullString
	MuseSource, AngleSource sql.NullString
	Revision                int
	Retired                 bool
}

func readSong(ctx context.Context, tx *sql.Tx, id string) (songRow, error) {
	var s songRow
	var retired sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id, root_track_id, draft_id, muse_source, angle_source, revision, retired_at FROM songs WHERE id = ?`, id).
		Scan(&s.ID, &s.Root, &s.Draft, &s.MuseSource, &s.AngleSource, &s.Revision, &retired)
	if err != nil {
		return s, fmt.Errorf("songs: read song: %w", err)
	}
	s.Retired = retired.Valid
	return s, nil
}

// sortByInstant orders roots by (generation, recorded_at instant, id); recorded_at columns of imported takes are 20
// characters wide and those of job takes 24, so they are compared as parsed instants (D-L1-34).
type rootKey struct {
	ID  string
	Gen int
	At  time.Time
}

func sortRoots(rs []rootKey) {
	sort.Slice(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.Gen != b.Gen {
			return a.Gen < b.Gen
		}
		if !a.At.Equal(b.At) {
			return a.At.Before(b.At)
		}
		return a.ID < b.ID
	})
}

func rootKeyOf(ctx context.Context, tx *sql.Tx, root string) (rootKey, error) {
	var at string
	var gen int
	if err := tx.QueryRowContext(ctx, `SELECT generation, recorded_at FROM tracks WHERE id = ?`, root).Scan(&gen, &at); err != nil {
		return rootKey{}, fmt.Errorf("songs: root key: %w", err)
	}
	t, err := store.ParseInstant(at)
	if err != nil {
		return rootKey{}, err
	}
	return rootKey{ID: root, Gen: gen, At: t}, nil
}

// ref is the part of a recipe_refs row the Song derivation reads (Muse text: handled only through muses.EnsureReferenceTx).
type ref struct {
	Title, Artist, VariantType string
}

// refsOfRecipe reads the reference rows of a recipe ordered by source.
func refsOfRecipe(ctx context.Context, tx *sql.Tx, recipeID string) ([]ref, error) {
	if recipeID == "" {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT ref_json FROM recipe_refs WHERE recipe_id = ? ORDER BY source`, recipeID)
	if err != nil {
		return nil, fmt.Errorf("songs: refs: %w", err)
	}
	defer rows.Close()
	var out []ref
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var m map[string]any
		if json.Unmarshal([]byte(raw), &m) != nil {
			continue
		}
		str := func(k string) string {
			s, _ := m[k].(string)
			return strings.Join(strings.Fields(s), " ")
		}
		out = append(out, ref{Title: str("source_title"), Artist: str("source_artist"), VariantType: str("variant_type")})
	}
	return out, rows.Err()
}

var requestSuffixRe = regexp.MustCompile(`_v(\d{1,3})$`)

// suffixMap is the unambiguous angle of each numeric request-id suffix (D-L1-7): built from recipe_refs joined to
// their drafts; an entry exists iff every ref row whose draft's request id ends _v<n> carries the same variant type.
type suffixMap map[string]string

func buildSuffixMap(ctx context.Context, tx *sql.Tx) (suffixMap, error) {
	rows, err := tx.QueryContext(ctx, `SELECT d.request_id, json_extract(r.ref_json, '$.variant_type')
		FROM recipe_refs r JOIN recipe_drafts d ON d.source_recipe_id = r.recipe_id`)
	if err != nil {
		return nil, fmt.Errorf("songs: suffix map: %w", err)
	}
	defer rows.Close()
	seen := map[string]map[string]bool{}
	for rows.Next() {
		var req string
		var vt sql.NullString
		if err := rows.Scan(&req, &vt); err != nil {
			return nil, err
		}
		m := requestSuffixRe.FindStringSubmatch(req)
		if m == nil || !vt.Valid {
			continue
		}
		slug, ok := AngleSlug(vt.String)
		if !ok {
			continue
		}
		if seen[m[1]] == nil {
			seen[m[1]] = map[string]bool{}
		}
		seen[m[1]][slug] = true
	}
	out := suffixMap{}
	for n, set := range seen {
		if len(set) == 1 {
			for a := range set {
				out[n] = a
			}
		}
	}
	return out, rows.Err()
}

// angleFor derives the angle of a draft: its ref row's variant type, else the unambiguous request-id suffix.
func angleFor(refs []ref, requestID string, sm func() (suffixMap, error)) (angle, source string, err error) {
	for _, r := range refs {
		if r.VariantType == "" {
			continue
		}
		if a, ok := AngleSlug(r.VariantType); ok {
			return a, AngleReference, nil
		}
	}
	if m := requestSuffixRe.FindStringSubmatch(requestID); m != nil {
		s, err := sm()
		if err != nil {
			return "", "", err
		}
		if a, ok := s[m[1]]; ok {
			return a, AngleRequestID, nil
		}
	}
	return "", "", nil
}
