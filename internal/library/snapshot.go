package library

import (
	"context"
	"database/sql"
	"fmt"
)

// SnapRecipe is a stored recipe as the importer needs it.
type SnapRecipe struct {
	ID, RequestID, CoreHash string
	Seed                    int64
}

// SnapTrack is a stored track as the importer needs it.
type SnapTrack struct {
	ID, RecipeID, RootTrackID, Slug, Title string
	FramesSHA256, MasterSHA256, SourceSHA  string
	TotalSamples                           int64
	RecordedAt                             string
	Generation                             int
}

// SnapDraft is a stored draft as the importer needs it.
type SnapDraft struct {
	ID, SourceRecipeID, Status, Origin string
	ParentTrackID                      string
}

// LibrarySnapshot is a consistent read of the state a dry-run or apply plans
// against, taken in one read transaction.
type LibrarySnapshot struct {
	Recipes      map[string]SnapRecipe // by recipe hash
	Tracks       []SnapTrack
	AudioIDs     map[string]string    // "<frames sha>:<total samples>" -> track id
	Drafts       map[string]SnapDraft // by source recipe id
	SourceKeys   map[string]bool      // recipe hash + "\x00" + path + "\x00" + line + "\x00" + raw sha
	RefKeys      map[string]string    // recipe hash + "\x00" + source -> sha256 of ref_json
	Collections  map[string][]string  // collection name -> member draft ids
	MasterPaths  map[string]bool      // DataDir-relative files referenced by tracks
	DerivedPaths map[string]bool      // DataDir-relative files referenced by renditions
}

// Snapshot reads everything the importer plans against in one transaction.
func (s *Service) Snapshot(ctx context.Context) (LibrarySnapshot, error) {
	snap := LibrarySnapshot{
		Recipes: map[string]SnapRecipe{}, AudioIDs: map[string]string{}, Drafts: map[string]SnapDraft{},
		SourceKeys: map[string]bool{}, RefKeys: map[string]string{}, Collections: map[string][]string{},
		MasterPaths: map[string]bool{}, DerivedPaths: map[string]bool{},
	}
	tx, err := s.st.R.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return snap, err
	}
	defer func() { _ = tx.Rollback() }()
	byID := map[string]string{} // recipe id -> hash

	each := func(q string, f func(*sql.Rows) error) error {
		rows, err := tx.QueryContext(ctx, q)
		if err != nil {
			return fmt.Errorf("library: snapshot: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			if err := f(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	if err := each(`SELECT id, hash, request_id, core_hash, seed FROM recipes`, func(r *sql.Rows) error {
		var x SnapRecipe
		var h string
		if err := r.Scan(&x.ID, &h, &x.RequestID, &x.CoreHash, &x.Seed); err != nil {
			return err
		}
		snap.Recipes[h], byID[x.ID] = x, h
		return nil
	}); err != nil {
		return snap, err
	}
	if err := each(`SELECT id, recipe_id, root_track_id, slug, title, frames_sha256, master_sha256, source_sha256, total_samples, recorded_at, master_path, generation FROM tracks ORDER BY recorded_at, id`, func(r *sql.Rows) error {
		var x SnapTrack
		var mp string
		if err := r.Scan(&x.ID, &x.RecipeID, &x.RootTrackID, &x.Slug, &x.Title, &x.FramesSHA256, &x.MasterSHA256, &x.SourceSHA, &x.TotalSamples, &x.RecordedAt, &mp, &x.Generation); err != nil {
			return err
		}
		snap.Tracks = append(snap.Tracks, x)
		snap.AudioIDs[fmt.Sprintf("%s:%d", x.FramesSHA256, x.TotalSamples)] = x.ID
		snap.MasterPaths[mp] = true
		return nil
	}); err != nil {
		return snap, err
	}
	if err := each(`SELECT id, source_recipe_id, status, origin, parent_track_id FROM recipe_drafts WHERE source_recipe_id IS NOT NULL`, func(r *sql.Rows) error {
		var x SnapDraft
		var par sql.NullString
		if err := r.Scan(&x.ID, &x.SourceRecipeID, &x.Status, &x.Origin, &par); err != nil {
			return err
		}
		x.ParentTrackID = par.String
		snap.Drafts[x.SourceRecipeID] = x
		return nil
	}); err != nil {
		return snap, err
	}
	if err := each(`SELECT recipe_id, source_path, line_no, raw_sha256 FROM recipe_sources`, func(r *sql.Rows) error {
		var id, p, sha string
		var line int
		if err := r.Scan(&id, &p, &line, &sha); err != nil {
			return err
		}
		snap.SourceKeys[fmt.Sprintf("%s\x00%s\x00%d\x00%s", byID[id], p, line, sha)] = true
		return nil
	}); err != nil {
		return snap, err
	}
	if err := each(`SELECT recipe_id, source, ref_json FROM recipe_refs`, func(r *sql.Rows) error {
		var id, src, rj string
		if err := r.Scan(&id, &src, &rj); err != nil {
			return err
		}
		snap.RefKeys[byID[id]+"\x00"+src] = sha256Hex([]byte(rj))
		return nil
	}); err != nil {
		return snap, err
	}
	if err := each(`SELECT name, draft_id FROM draft_collections ORDER BY name, draft_id`, func(r *sql.Rows) error {
		var n, d string
		if err := r.Scan(&n, &d); err != nil {
			return err
		}
		snap.Collections[n] = append(snap.Collections[n], d)
		return nil
	}); err != nil {
		return snap, err
	}
	if err := each(`SELECT path FROM renditions`, func(r *sql.Rows) error {
		var p string
		if err := r.Scan(&p); err != nil {
			return err
		}
		snap.DerivedPaths[p] = true
		return nil
	}); err != nil {
		return snap, err
	}
	return snap, tx.Commit()
}
