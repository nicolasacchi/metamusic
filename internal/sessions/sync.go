package sessions

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/store"
)

const (
	maxNameRunes   = 120
	maxSourceKey   = 300
	nameTimeLayout = "2006-01-02 15:04Z"
)

func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// CollectionKey is "collection:<name>", shortened with a hash when a long name would exceed the column limit.
func CollectionKey(name string) string {
	k := "collection:" + name
	if len(k) <= maxSourceKey {
		return k
	}
	h := sha256.Sum256([]byte(name))
	return "collection:" + strings.ToValidUTF8(name[:200], "") + "#" + hex.EncodeToString(h[:8])
}

func nameTime(s string) string {
	t, err := store.ParseInstant(s)
	if err != nil {
		return s
	}
	return t.Format(nameTimeLayout)
}

// SyncRunsTx syncs the import-run sessions (C92.1): source key import:<run>, kind import, name "Import <date>", started =
// the run's start, ended = its finish (NULL while running); members are the run's tracks and the drafts whose first
// recipe_sources occurrence belongs to the run.
func SyncRunsTx(ctx context.Context, tx *sql.Tx, env Env, runIDs []string) (Stats, error) {
	var st Stats
	for _, r := range runIDs {
		var started string
		var finished sql.NullString
		var state string
		err := tx.QueryRowContext(ctx, `SELECT started_at, finished_at, state FROM import_runs WHERE id = ?`, r).Scan(&started, &finished, &state)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return st, fmt.Errorf("sessions: sync run: %w", err)
		}
		var ended *string
		if finished.Valid && state != "running" {
			ended = &finished.String
		}
		id, created, err := EnsureTx(ctx, tx, env, Key{Kind: KindImport, SourceKey: "import:" + r, Name: "Import " + nameTime(started), StartedAt: started, EndedAt: ended})
		if err != nil {
			return st, err
		}
		if created {
			st.Created++
		}
		n, err := addMembersSQL(ctx, tx, env, `INSERT OR IGNORE INTO session_members(session_id, member_kind, member_id, added_at)
			SELECT ?1, 'track', id, ?2 FROM tracks WHERE import_run_id = ?3`, id, r)
		if err != nil {
			return st, err
		}
		st.MembersAdded += n
		n, err = addMembersSQL(ctx, tx, env, `INSERT OR IGNORE INTO session_members(session_id, member_kind, member_id, added_at)
			SELECT ?1, 'draft', d.id, ?2 FROM recipe_drafts d
			 WHERE (SELECT s.import_run_id FROM recipe_sources s WHERE s.recipe_id = d.source_recipe_id ORDER BY s.id LIMIT 1) = ?3`, id, r)
		if err != nil {
			return st, err
		}
		st.MembersAdded += n
	}
	return st, nil
}

// SyncCollectionsTx syncs the draft-collection sessions (C92.1): source key collection:<name>, name the collection name
// (cut to 120 runes), kind decided once at the first sync (upload iff every draft then has origin jsonl_upload, else
// import; never recomputed), started/ended the min/max draft created_at by parsed instant, members the drafts.
func SyncCollectionsTx(ctx context.Context, tx *sql.Tx, env Env, names []string) (Stats, error) {
	var st Stats
	for _, c := range names {
		rows, err := tx.QueryContext(ctx, `SELECT d.created_at, d.origin FROM draft_collections c JOIN recipe_drafts d ON d.id = c.draft_id WHERE c.name = ?`, c)
		if err != nil {
			return st, fmt.Errorf("sessions: sync collection: %w", err)
		}
		var minT, maxT time.Time
		n, uploads := 0, 0
		for rows.Next() {
			var at, origin string
			if err := rows.Scan(&at, &origin); err != nil {
				_ = rows.Close()
				return st, err
			}
			t, err := store.ParseInstant(at)
			if err != nil {
				_ = rows.Close()
				return st, err
			}
			if n == 0 || t.Before(minT) {
				minT = t
			}
			if n == 0 || t.After(maxT) {
				maxT = t
			}
			n++
			if origin == "jsonl_upload" {
				uploads++
			}
		}
		if err := rows.Close(); err != nil {
			return st, err
		}
		if n == 0 {
			continue
		}
		kind := KindImport
		if uploads == n {
			kind = KindUpload
		}
		mx := store.FormatTime(maxT)
		id, created, err := EnsureTx(ctx, tx, env, Key{Kind: kind, SourceKey: CollectionKey(c), Name: cutRunes(c, maxNameRunes), StartedAt: store.FormatTime(minT), EndedAt: &mx})
		if err != nil {
			return st, err
		}
		if created {
			st.Created++
		}
		added, err := addMembersSQL(ctx, tx, env, `INSERT OR IGNORE INTO session_members(session_id, member_kind, member_id, added_at)
			SELECT ?1, 'draft', draft_id, ?2 FROM draft_collections WHERE name = ?3`, id, c)
		if err != nil {
			return st, err
		}
		st.MembersAdded += added
	}
	return st, nil
}

// SyncBatchesTx syncs the generation-batch sessions (C92.1) of batches in state active or finished that have at least
// one job: source key batch:<id>, name "Batch <date> · n take(s)", started = created_at, ended = finished_at (NULL while
// live), members the jobs' drafts and completed takes. Other batches create no session.
func SyncBatchesTx(ctx context.Context, tx *sql.Tx, env Env, batchIDs []string) (Stats, error) {
	var st Stats
	for _, b := range batchIDs {
		var created string
		var finished sql.NullString
		var nTakes int
		err := tx.QueryRowContext(ctx, `SELECT created_at, finished_at, n_takes FROM batches b
			WHERE id = ? AND state IN ('active','finished') AND EXISTS (SELECT 1 FROM jobs WHERE batch_id = b.id)`, b).Scan(&created, &finished, &nTakes)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return st, fmt.Errorf("sessions: sync batch: %w", err)
		}
		var ended *string
		if finished.Valid {
			ended = &finished.String
		}
		unit := "takes"
		if nTakes == 1 {
			unit = "take"
		}
		id, isNew, err := EnsureTx(ctx, tx, env, Key{Kind: KindBatch, SourceKey: "batch:" + b, Name: fmt.Sprintf("Batch %s · %d %s", nameTime(created), nTakes, unit), StartedAt: created, EndedAt: ended})
		if err != nil {
			return st, err
		}
		if isNew {
			st.Created++
		}
		n, err := addMembersSQL(ctx, tx, env, `INSERT OR IGNORE INTO session_members(session_id, member_kind, member_id, added_at)
			SELECT ?1, 'draft', draft_id, ?2 FROM jobs WHERE batch_id = ?3 AND draft_id IS NOT NULL`, id, b)
		if err != nil {
			return st, err
		}
		st.MembersAdded += n
		n, err = addMembersSQL(ctx, tx, env, `INSERT OR IGNORE INTO session_members(session_id, member_kind, member_id, added_at)
			SELECT ?1, 'track', track_id, ?2 FROM jobs WHERE batch_id = ?3 AND track_id IS NOT NULL`, id, b)
		if err != nil {
			return st, err
		}
		st.MembersAdded += n
	}
	return st, nil
}
