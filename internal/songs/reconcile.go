package songs

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Structural reconcile (C90.1, C90.2): called by the Reconciler only, inside its phase-A transaction. Every function is a
// pure function of the database state and the dirty keys, so a pass can be redone (C95.2). Muse text never appears in an
// anomaly, a log record or an error (C99.4).

// ReconcileTracksTx makes sure the lineage root of every given take belongs to a Song (C90.1): the Song of the draft
// that produced the root, else the Song of its compose_new job item, else a Song of its own; then, for the Song of
// each such root, the never-set Muse fill from the root recipe's refs (D-L1-37).
func ReconcileTracksTx(ctx context.Context, tx *sql.Tx, env Env, trackIDs []string) (Changes, error) {
	var ch Changes
	seen := map[string]bool{}
	var roots []rootKey
	for _, t := range trackIDs {
		root, _, err := rootOf(ctx, tx, t)
		if err != nil {
			return ch, err
		}
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		k, err := rootKeyOf(ctx, tx, root)
		if err != nil {
			return ch, err
		}
		roots = append(roots, k)
	}
	sortRoots(roots)
	for _, rk := range roots {
		r := rk.ID
		song, err := songOfRoot(ctx, tx, r)
		if err != nil {
			return ch, err
		}
		if song == "" {
			if song, err = attachOrCreateRoot(ctx, tx, env, r, &ch); err != nil {
				return ch, err
			}
		}
		if song != "" {
			if err := fillRootMuse(ctx, tx, env, song, r, &ch); err != nil {
				return ch, err
			}
		}
	}
	return ch, nil
}

// attachOrCreateRoot gives root r a Song (C90.1 steps 1-3) and returns its id.
func attachOrCreateRoot(ctx context.Context, tx *sql.Tx, env Env, r string, ch *Changes) (song string, err error) {
	// 1. The Song of the draft that produced r (motherless drafts only).
	var d, dRecipe, dLabel, dGroup, dReq, dFulfilledBy sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT id, source_recipe_id, label, group_key, request_id, fulfilled_by_track_id FROM recipe_drafts
		WHERE fulfilled_by_track_id = ?1 AND parent_track_id IS NULL
		UNION ALL
		SELECT d.id, d.source_recipe_id, d.label, d.group_key, d.request_id, d.fulfilled_by_track_id FROM jobs j JOIN recipe_drafts d ON d.id = j.draft_id
		WHERE j.track_id = ?1 AND d.parent_track_id IS NULL ORDER BY 1 LIMIT 1`, r).Scan(&d, &dRecipe, &dLabel, &dGroup, &dReq, &dFulfilledBy)
	if err != nil && err != sql.ErrNoRows {
		return "", fmt.Errorf("songs: draft of root: %w", err)
	}
	if d.Valid {
		s, sroot, err := liveSongOfDraft(ctx, tx, d.String)
		if err != nil {
			return "", err
		}
		if s == "" && dFulfilledBy.Valid {
			fr, _, err := rootOf(ctx, tx, dFulfilledBy.String)
			if err != nil {
				return "", err
			}
			if fr != "" {
				if s, err = songOfRoot(ctx, tx, fr); err != nil {
					return "", err
				}
				sroot = sql.NullString{String: fr, Valid: true}
			}
		}
		if s != "" {
			if err := insertSongRoot(ctx, tx, env, r, s, "draft"); err != nil {
				return "", err
			}
			if !sroot.Valid {
				if _, err := tx.ExecContext(ctx, `UPDATE songs SET root_track_id = ? WHERE id = ? AND root_track_id IS NULL`, r, s); err != nil {
					return "", fmt.Errorf("songs: set primary root: %w", err)
				}
			}
			ch.Attached = append(ch.Attached, s)
			return s, nil
		}
		// no Song for the draft yet: create it now with this root (a draft whose Song was never made)
		id, err := createDraftSong(ctx, tx, env, draftRow{ID: d.String, Origin: "", RequestID: dReq.String, Label: dLabel, GroupKey: dGroup, SourceRecipe: dRecipe}, sql.NullString{String: r, Valid: true}, ch)
		if err != nil {
			return "", err
		}
		if err := insertSongRoot(ctx, tx, env, r, id, "draft"); err != nil {
			return "", err
		}
		return id, nil
	}
	// 2. The Song of the job item (compose_new): the sibling takes of one item share a Song.
	var batch string
	var item sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT j.batch_id, json_extract(e.items_json, '$[' || j.take_index || '].item_index')
		FROM jobs j JOIN batches b ON b.id = j.batch_id JOIN estimates e ON e.id = b.estimate_id
		WHERE j.track_id = ? AND j.draft_id IS NULL AND j.source_track_id IS NULL`, r).Scan(&batch, &item)
	switch {
	case err == sql.ErrNoRows:
	case err != nil:
		return "", fmt.Errorf("songs: job item: %w", err)
	case item.Valid:
		var s string
		err := tx.QueryRowContext(ctx, `SELECT sr.song_id FROM jobs j JOIN batches b ON b.id = j.batch_id JOIN estimates e ON e.id = b.estimate_id
			JOIN tracks t ON t.id = j.track_id JOIN song_roots sr ON sr.root_track_id = t.root_track_id
			WHERE j.batch_id = ? AND j.draft_id IS NULL AND j.source_track_id IS NULL AND j.track_id IS NOT NULL
			  AND json_extract(e.items_json, '$[' || j.take_index || '].item_index') = ? ORDER BY sr.attached_at, sr.root_track_id LIMIT 1`, batch, item.Int64).Scan(&s)
		if err != nil && err != sql.ErrNoRows {
			return "", fmt.Errorf("songs: job item song: %w", err)
		}
		if s != "" {
			if err := insertSongRoot(ctx, tx, env, r, s, "job_item"); err != nil {
				return "", err
			}
			ch.Attached = append(ch.Attached, s)
			return s, nil
		}
	}
	// 3. A Song of its own, titled like the take (not kept in sync: renaming is update_song).
	var title string
	if err := tx.QueryRowContext(ctx, `SELECT title FROM tracks WHERE id = ?`, r).Scan(&title); err != nil {
		return "", fmt.Errorf("songs: root title: %w", err)
	}
	id := env.NewID()
	ts := store.FormatTime(env.Now)
	if _, err := tx.ExecContext(ctx, `INSERT INTO songs(id, root_track_id, draft_id, title, origin, created_by, created_at, updated_by, updated_at)
		VALUES (?, ?, NULL, ?, 'root', ?, ?, ?, ?)`, id, r, title, env.Actor, ts, env.Actor, ts); err != nil {
		return "", fmt.Errorf("songs: insert root song: %w", err)
	}
	if err := insertSongRoot(ctx, tx, env, r, id, "own"); err != nil {
		return "", err
	}
	ch.Created = append(ch.Created, id)
	return id, nil
}

func insertSongRoot(ctx context.Context, tx *sql.Tx, env Env, root, song, via string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO song_roots(root_track_id, song_id, via, attached_by, attached_at) VALUES (?,?,?,?,?)`,
		root, song, via, env.Actor, store.FormatTime(env.Now)); err != nil {
		return fmt.Errorf("songs: insert song_root: %w", err)
	}
	return nil
}

// fillRootMuse gives the Song of root r the Muse of the root recipe's first reference row while its muse_source is
// NULL (C90.1 step 4, D-L1-37): a rendered `varianti` root whose recipe carries an index row gets its Muse with or
// without a draft.
func fillRootMuse(ctx context.Context, tx *sql.Tx, env Env, song, root string, ch *Changes) error {
	s, err := readSong(ctx, tx, song)
	if err != nil {
		return err
	}
	if s.MuseSource.Valid || s.Retired {
		return nil
	}
	var recipe string
	if err := tx.QueryRowContext(ctx, `SELECT recipe_id FROM tracks WHERE id = ?`, root).Scan(&recipe); err != nil {
		return fmt.Errorf("songs: root recipe: %w", err)
	}
	refs, err := refsOfRecipe(ctx, tx, recipe)
	if err != nil {
		return err
	}
	return setMuseFromRefs(ctx, tx, env, song, refs, ch)
}

// setMuseFromRefs sets the Muse from the first reference with a title; the caller checked that muse_source is NULL.
func setMuseFromRefs(ctx context.Context, tx *sql.Tx, env Env, song string, refs []ref, ch *Changes) error {
	for _, r := range refs {
		if r.Title == "" {
			continue
		}
		var artist *string
		if r.Artist != "" {
			a := r.Artist
			artist = &a
		}
		id, created, err := muses.EnsureReferenceTx(ctx, tx, env.Now, env.NewID, r.Title, artist, env.Actor)
		if err != nil {
			return err
		}
		if created {
			ch.MusesCreated++
		}
		if _, err := tx.ExecContext(ctx, `UPDATE songs SET muse_id = ?, muse_source = 'reference' WHERE id = ? AND muse_source IS NULL`, id, song); err != nil {
			return fmt.Errorf("songs: set muse: %w", err)
		}
		return nil
	}
	return nil
}

// draftRow is the columns of recipe_drafts the reconcile reads.
type draftRow struct {
	ID, Origin, Status, RequestID string
	GroupKey, Label               sql.NullString
	SourceRecipe, Parent, Done    sql.NullString
}

// ReconcileDraftsTx reconciles the Songs of the given drafts (C90.2): a draft with a mother or fulfilled by a non-root
// take has no Song of its own (an existing takes-less one is retired into the lineage's Song); an unfulfilled
// motherless draft has a draft Song; a draft fulfilled by a root attaches to the root's Song or creates one with both.
func ReconcileDraftsTx(ctx context.Context, tx *sql.Tx, env Env, draftIDs []string) (Changes, error) {
	var ch Changes
	ids := append([]string(nil), draftIDs...)
	sort.Strings(ids)
	var sm suffixMap
	lazy := func() (suffixMap, error) {
		if sm != nil {
			return sm, nil
		}
		m, err := buildSuffixMap(ctx, tx)
		sm = m
		return sm, err
	}
	var prev string
	for _, id := range ids {
		if id == prev {
			continue
		}
		prev = id
		var d draftRow
		err := tx.QueryRowContext(ctx, `SELECT id, origin, status, request_id, group_key, label, source_recipe_id, parent_track_id, fulfilled_by_track_id
			FROM recipe_drafts WHERE id = ?`, id).Scan(&d.ID, &d.Origin, &d.Status, &d.RequestID, &d.GroupKey, &d.Label, &d.SourceRecipe, &d.Parent, &d.Done)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return ch, fmt.Errorf("songs: read draft: %w", err)
		}
		if err := reconcileDraft(ctx, tx, env, d, lazy, &ch); err != nil {
			return ch, err
		}
	}
	return ch, nil
}

func reconcileDraft(ctx context.Context, tx *sql.Tx, env Env, d draftRow, sm func() (suffixMap, error), ch *Changes) error {
	// the lineage the draft belongs to, if any
	var lineageRoot string
	switch {
	case d.Parent.Valid:
		r, _, err := rootOf(ctx, tx, d.Parent.String)
		if err != nil {
			return err
		}
		lineageRoot = r
	case d.Done.Valid:
		r, gen, err := rootOf(ctx, tx, d.Done.String)
		if err != nil {
			return err
		}
		if gen > 0 {
			lineageRoot = r
		}
	}
	if lineageRoot != "" {
		return reconcileLineageDraft(ctx, tx, env, d, lineageRoot, ch)
	}
	// own: a live Song of the draft exists => the never-set fill only
	s, _, err := liveSongOfDraft(ctx, tx, d.ID)
	if err != nil {
		return err
	}
	if s != "" {
		return fillDraftSong(ctx, tx, env, s, d, sm, ch)
	}
	// fulfilled by a root take
	if d.Done.Valid {
		t := d.Done.String
		st, err := songOfRoot(ctx, tx, t)
		if err != nil {
			return err
		}
		if st != "" {
			row, err := readSong(ctx, tx, st)
			if err != nil {
				return err
			}
			if row.Retired || row.Draft.Valid {
				return nil // another draft owns the root's Song (the second draft is not a Song)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE songs SET draft_id = ? WHERE id = ? AND draft_id IS NULL`, d.ID, st); err != nil {
				return fmt.Errorf("songs: attach draft: %w", err)
			}
			ch.Attached = append(ch.Attached, st)
			return fillDraftSong(ctx, tx, env, st, d, sm, ch)
		}
		id, err := createDraftSong(ctx, tx, env, d, sql.NullString{String: t, Valid: true}, ch)
		if err != nil {
			return err
		}
		return insertSongRoot(ctx, tx, env, t, id, "draft")
	}
	if _, err := createDraftSong(ctx, tx, env, d, sql.NullString{}, ch); err != nil {
		return err
	}
	return nil
}

// reconcileLineageDraft handles a draft that belongs to an existing lineage: no Song of its own; a takes-less Song it
// had is retired into the lineage's Song, carrying its Muse and angle when the target has none (D-L1-37).
func reconcileLineageDraft(ctx context.Context, tx *sql.Tx, env Env, d draftRow, root string, ch *Changes) error {
	sd, sdRoot, err := liveSongOfDraft(ctx, tx, d.ID)
	if err != nil {
		return err
	}
	if sd == "" {
		return nil
	}
	sr, err := songOfRoot(ctx, tx, root)
	if err != nil {
		return err
	}
	if sr == "" {
		ch.Anomalies = append(ch.Anomalies, Anomaly{Code: "song_missing_for_root", Subject: d.ID, Detail: "root " + root, Transient: true})
		return nil
	}
	if sdRoot.Valid {
		if sd != sr {
			ch.Anomalies = append(ch.Anomalies, Anomaly{Code: "draft_song_has_root", Subject: d.ID, Detail: "song " + sd})
		}
		return nil
	}
	if sd == sr {
		return nil
	}
	old, err := readSong(ctx, tx, sd)
	if err != nil {
		return err
	}
	target, err := readSong(ctx, tx, sr)
	if err != nil {
		return err
	}
	ts := store.FormatTime(env.Now)
	if _, err := tx.ExecContext(ctx, `UPDATE songs SET retired_at = ?, retired_into = ? WHERE id = ?`, ts, sr, sd); err != nil {
		return fmt.Errorf("songs: retire: %w", err)
	}
	ch.Retired = append(ch.Retired, sd)
	ch.Touched = append(ch.Touched, sr)
	carried := false
	if !target.MuseSource.Valid && old.MuseSource.Valid {
		if _, err := tx.ExecContext(ctx, `UPDATE songs SET muse_id = (SELECT muse_id FROM songs WHERE id = ?1), muse_source = (SELECT muse_source FROM songs WHERE id = ?1)
			WHERE id = ?2`, sd, sr); err != nil {
			return fmt.Errorf("songs: carry muse: %w", err)
		}
		carried = true
		if !target.AngleSource.Valid { // the angle travels with the Muse, never alone
			if _, err := tx.ExecContext(ctx, `UPDATE songs SET angle = (SELECT angle FROM songs WHERE id = ?1), angle_source = (SELECT angle_source FROM songs WHERE id = ?1)
				WHERE id = ?2 AND (SELECT angle_source FROM songs WHERE id = ?1) IS NOT NULL AND (SELECT angle_source FROM songs WHERE id = ?1) <> 'cleared'`, sd, sr); err != nil {
				return fmt.Errorf("songs: carry angle: %w", err)
			}
		}
	}
	if carried {
		if _, err := tx.ExecContext(ctx, `UPDATE songs SET revision = revision + 1, updated_at = ?, updated_by = ? WHERE id = ?`, ts, env.Actor, sr); err != nil {
			return fmt.Errorf("songs: bump target: %w", err)
		}
		env.Logger().Info("song_muse_carried", "from_song", sd, "to_song", sr)
	}
	return nil
}

// createDraftSong inserts the draft Song of d (origin draft) with the Muse and angle of its refs (C90.3). root is the
// primary root when the draft was rendered before its Song existed.
func createDraftSong(ctx context.Context, tx *sql.Tx, env Env, d draftRow, root sql.NullString, ch *Changes) (string, error) {
	refs, err := refsOfRecipe(ctx, tx, d.SourceRecipe.String)
	if err != nil {
		return "", err
	}
	var smap suffixMap
	angle, asrc, err := angleFor(refs, d.RequestID, func() (suffixMap, error) {
		if smap == nil {
			m, err := buildSuffixMap(ctx, tx)
			smap = m
			return smap, err
		}
		return smap, nil
	})
	if err != nil {
		return "", err
	}
	var ap *string
	if angle != "" {
		ap = &angle
	}
	title := DefaultTitle(d.Label.String, d.GroupKey.String, d.RequestID, ap)
	id := env.NewID()
	ts := store.FormatTime(env.Now)
	var angleArg, srcArg any
	if angle != "" {
		angleArg, srcArg = angle, asrc
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO songs(id, root_track_id, draft_id, angle, angle_source, title, origin, created_by, created_at, updated_by, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'draft', ?, ?, ?, ?)`, id, nullArg(root), d.ID, angleArg, srcArg, title, env.Actor, ts, env.Actor, ts); err != nil {
		return "", fmt.Errorf("songs: insert draft song: %w", err)
	}
	ch.Created = append(ch.Created, id)
	if err := setMuseFromRefs(ctx, tx, env, id, refs, ch); err != nil {
		return "", err
	}
	return id, nil
}

func nullArg(s sql.NullString) any {
	if s.Valid {
		return s.String
	}
	return nil
}

// fillDraftSong applies the never-set rule (C90.3, D-L1-37) to an existing Song of draft d: the Muse and the angle are
// filled only while their source column is NULL; a value a human set or cleared is never touched.
func fillDraftSong(ctx context.Context, tx *sql.Tx, env Env, song string, d draftRow, sm func() (suffixMap, error), ch *Changes) error {
	s, err := readSong(ctx, tx, song)
	if err != nil {
		return err
	}
	if s.MuseSource.Valid && s.AngleSource.Valid {
		return nil
	}
	refs, err := refsOfRecipe(ctx, tx, d.SourceRecipe.String)
	if err != nil {
		return err
	}
	if !s.MuseSource.Valid {
		if err := setMuseFromRefs(ctx, tx, env, song, refs, ch); err != nil {
			return err
		}
	}
	if !s.AngleSource.Valid {
		angle, src, err := angleFor(refs, d.RequestID, sm)
		if err != nil {
			return err
		}
		if angle != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE songs SET angle = ?, angle_source = ? WHERE id = ? AND angle_source IS NULL`, angle, src, song); err != nil {
				return fmt.Errorf("songs: set angle: %w", err)
			}
		}
	}
	return nil
}
