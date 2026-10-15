package provenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/nicolasacchi/metamusic/internal/ladder"
	"github.com/nicolasacchi/metamusic/internal/picks"
	"github.com/nicolasacchi/metamusic/internal/sessions"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/sounds"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Actor is the principal of every row the Reconciler writes (created_by, actor, decided_by). M3 already uses
// system:reconciler for its jobs reconciler, and two writers must not share a key.
const Actor = "system:provenance"

func (s *Service) env() store.Env {
	return store.Env{Now: s.o.Now(), NewID: s.o.NewID, Actor: Actor, Log: s.o.Log}
}

// Settle drains the queue synchronously under the settle lease (C95.1): ErrSettleBusy when another live process holds
// it. It returns when a pass finds no keys, when every selected key was only re-marked (no progress) or when ctx ends;
// there is no fixed pass cap.
func (s *Service) Settle(ctx context.Context) (Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	epoch, err := s.acquire(ctx)
	if err != nil {
		return Report{}, err
	}
	defer s.release(epoch)
	if s.o.BeforeSettle != nil {
		s.o.BeforeSettle(ctx)
	}
	var rep Report
	for {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		r, progressed, err := s.pass(ctx, epoch)
		rep.Add(r)
		if err != nil {
			return rep, err
		}
		if !progressed {
			return rep, nil
		}
	}
}

// phaseA is what phase A learned: the keys' ids by kind, the affected Songs and the anomalies.
type phaseA struct {
	tracks, drafts []string
	affected       []string
	transient      map[Key]bool
	rep            Report
}

// pass runs one batch: read keys, phase A, ladder, phase B, settle the queue rows.
func (s *Service) pass(ctx context.Context, epoch int64) (Report, bool, error) {
	keys, err := readKeys(ctx, s.o.Store.R, picks.BatchKeys)
	if err != nil {
		return Report{}, false, err
	}
	if len(keys) == 0 {
		return Report{}, false, nil
	}
	if err := s.renew(ctx, epoch); err != nil {
		return Report{}, false, err
	}
	var a phaseA
	if err := s.phaseA(ctx, epoch, keys, &a); err != nil {
		return a.rep, false, err
	}
	if s.o.FaultAfterPhaseA != nil {
		if err := s.o.FaultAfterPhaseA(); err != nil {
			return a.rep, false, err
		}
	}
	strengths, err := s.strengths(ctx, a.affected)
	if err != nil {
		return a.rep, false, err
	}
	progressed, err := s.phaseB(ctx, epoch, keys, &a, strengths)
	a.rep.Passes++
	return a.rep, progressed, err
}

func (s *Service) phaseA(ctx context.Context, epoch int64, keys []Key, a *phaseA) error {
	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("provenance: phase A: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.checkEpochTx(ctx, tx, epoch); err != nil {
		return err
	}
	env := s.env()
	runs, colls, batches := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, k := range keys {
		switch k.Kind {
		case "run":
			runs[k.ID] = true
		case "collection":
			colls[k.ID] = true
		case "batch":
			batches[k.ID] = true
		case "track":
			a.tracks = append(a.tracks, k.ID)
		case "draft":
			a.drafts = append(a.drafts, k.ID)
		}
	}
	// sessions reached from the dirty tracks and drafts (each distinct session once per pass)
	if err := reachSessions(ctx, tx, a.tracks, a.drafts, runs, colls, batches); err != nil {
		return err
	}
	var ch songs.Changes
	c1, err := songs.ReconcileTracksTx(ctx, tx, env, a.tracks) // tracks before drafts (C95.1)
	if err != nil {
		return err
	}
	ch.Merge(c1)
	c2, err := songs.ReconcileDraftsTx(ctx, tx, env, a.drafts)
	if err != nil {
		return err
	}
	ch.Merge(c2)
	var st sessions.Stats
	for _, f := range []struct {
		ids []string
		fn  func(context.Context, *sql.Tx, store.Env, []string) (sessions.Stats, error)
	}{{sortedKeys(runs), sessions.SyncRunsTx}, {sortedKeys(colls), sessions.SyncCollectionsTx}, {sortedKeys(batches), sessions.SyncBatchesTx}} {
		x, err := f.fn(ctx, tx, env, f.ids)
		if err != nil {
			return err
		}
		st.Add(x)
	}
	affected, err := affectedSongs(ctx, tx, a.tracks, a.drafts, ch)
	if err != nil {
		return err
	}
	a.affected = affected
	a.transient = map[Key]bool{}
	for _, an := range ch.Anomalies {
		a.rep.Anomalies = append(a.rep.Anomalies, an)
		if an.Transient {
			for _, k := range keys {
				if k.Kind == "draft" && k.ID == an.Subject {
					a.transient[k] = true
				}
			}
		}
	}
	a.rep.SongsCreated, a.rep.SongsAttached, a.rep.SongsRetired = len(ch.Created), len(ch.Attached), len(ch.Retired)
	a.rep.MusesCreated = ch.MusesCreated
	a.rep.SessionsCreated, a.rep.MembersAdded = st.Created, st.MembersAdded
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("provenance: phase A commit: %w", err)
	}
	return nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// reachSessions adds the import runs, collections and batches reached from the dirty tracks and drafts (C92.1).
func reachSessions(ctx context.Context, tx *sql.Tx, tracks, drafts []string, runs, colls, batches map[string]bool) error {
	for _, t := range tracks {
		var run sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT import_run_id FROM tracks WHERE id = ?`, t).Scan(&run); err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("provenance: reach run: %w", err)
		}
		if run.Valid {
			runs[run.String] = true
		}
		if err := collect(ctx, tx, batches, `SELECT batch_id FROM jobs WHERE track_id = ?`, t); err != nil {
			return err
		}
	}
	for _, d := range drafts {
		if err := collect(ctx, tx, colls, `SELECT name FROM draft_collections WHERE draft_id = ?`, d); err != nil {
			return err
		}
		if err := collect(ctx, tx, runs, `SELECT s.import_run_id FROM recipe_sources s
			WHERE s.recipe_id = (SELECT source_recipe_id FROM recipe_drafts WHERE id = ?) ORDER BY s.id LIMIT 1`, d); err != nil {
			return err
		}
	}
	return nil
}

func collect(ctx context.Context, tx *sql.Tx, into map[string]bool, q string, args ...any) error {
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("provenance: reach: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return err
		}
		into[v] = true
	}
	return rows.Err()
}

// affectedSongs collects the live Songs whose picks and sounds may have changed: the Songs of the dirty tracks, the
// Songs created, attached or retired into, and the Songs of the dirty drafts.
func affectedSongs(ctx context.Context, tx *sql.Tx, tracks, drafts []string, ch songs.Changes) ([]string, error) {
	set := map[string]bool{}
	for _, id := range slices.Concat(ch.Created, ch.Attached, ch.Touched) {
		set[id] = true
	}
	for _, t := range tracks {
		if err := collect(ctx, tx, set, `SELECT song_id FROM take_songs WHERE track_id = ?`, t); err != nil {
			return nil, err
		}
	}
	for _, d := range drafts {
		if err := collect(ctx, tx, set, `SELECT id FROM songs WHERE draft_id = ? AND retired_at IS NULL`, d); err != nil {
			return nil, err
		}
		if err := collect(ctx, tx, set, `SELECT sr.song_id FROM recipe_drafts d JOIN tracks t ON t.id = d.fulfilled_by_track_id
			JOIN song_roots sr ON sr.root_track_id = t.root_track_id WHERE d.id = ?`, d); err != nil {
			return nil, err
		}
	}
	live := map[string]bool{}
	for id := range set {
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM songs WHERE id = ? AND retired_at IS NULL`, id).Scan(&one)
		if err == nil {
			live[id] = true
		} else if err != sql.ErrNoRows {
			return nil, fmt.Errorf("provenance: affected: %w", err)
		}
	}
	return sortedKeys(live), nil
}

// strengths fetches the ladder strengths of every take of the affected Songs outside any write transaction (C95.1:
// the single writer connection is never held across the fit).
func (s *Service) strengths(ctx context.Context, songIDs []string) (map[string]ladder.Strength, error) {
	if s.o.Ladder == nil || len(songIDs) == 0 {
		return nil, nil
	}
	var ids []string
	for _, song := range songIDs {
		rows, err := s.o.Store.R.QueryContext(ctx, `SELECT track_id FROM take_songs WHERE song_id = ?`, song)
		if err != nil {
			return nil, fmt.Errorf("provenance: takes of song: %w", err)
		}
		for rows.Next() {
			var t string
			if err := rows.Scan(&t); err != nil {
				_ = rows.Close()
				return nil, err
			}
			ids = append(ids, t)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	st, _, err := s.o.Ladder.Strengths(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("provenance: ladder strengths: %w", err)
	}
	return st, nil
}

// phaseB recomputes picks and sounds and settles the queue rows by outcome (C95.1).
func (s *Service) phaseB(ctx context.Context, epoch int64, keys []Key, a *phaseA, strengths map[string]ladder.Strength) (bool, error) {
	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("provenance: phase B: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.checkEpochTx(ctx, tx, epoch); err != nil {
		return false, err
	}
	env := s.env()
	var pc picks.Changes
	if s.o.Picks != nil { // nil: the structural core only (tests of the Song/Muse/Session phase)
		if pc, err = s.o.Picks.RecomputeTx(ctx, tx, env, a.affected, strengths); err != nil {
			return false, err
		}
	}
	a.rep.PickEvents, a.rep.PickRefreshed = pc.Events, pc.Refreshed
	soundSongs := slices.Clone(a.affected)
	soundSongs = append(soundSongs, pc.Songs...)
	slices.Sort(soundSongs)
	soundSongs = slices.Compact(soundSongs)
	var sc sounds.Changes
	if s.o.Sounds != nil {
		if sc, err = s.o.Sounds.ComputeTx(ctx, tx, env, a.tracks, soundSongs); err != nil {
			return false, err
		}
	}
	a.rep.SoundsComputed = sc.Takes + sc.Songs

	var clean, requeue []Key
	for _, k := range keys {
		switch {
		case a.transient[k] && k.Tries+1 < picks.MaxQueueTries:
			requeue = append(requeue, k)
		case a.transient[k]: // MaxQueueTries reached: dequeue, the anomaly stays reported (and Verify finds it)
			clean = append(clean, k)
			a.rep.KeysDropped++
		default:
			clean = append(clean, k)
		}
	}
	if err := dequeueTx(ctx, tx, clean); err != nil {
		return false, err
	}
	if err := requeueTx(ctx, tx, requeue); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("provenance: phase B commit: %w", err)
	}
	a.rep.KeysProcessed = len(clean)
	a.rep.KeysRequeued = len(requeue)
	return len(clean) > 0, nil
}

// Sweep recomputes the cross-Song ladder drift and the stale Sound subjects (C91.5, C93.4); Run calls it at start and
// every SweepInterval. It takes the settle lease for the duration.
func (s *Service) Sweep(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	epoch, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer s.release(epoch)
	if s.o.Sounds == nil {
		if sw, ok := s.o.Picks.(pickSweeper); ok {
			if _, err := sw.Sweep(ctx); err != nil && !errors.Is(err, errors.ErrUnsupported) {
				return err
			}
		}
		return nil
	}
	if sw, ok := s.o.Picks.(pickSweeper); ok {
		if _, err := sw.Sweep(ctx); err != nil && !errors.Is(err, errors.ErrUnsupported) {
			return err
		}
	}
	for {
		if err := s.renew(ctx, epoch); err != nil {
			return err
		}
		tx, err := s.o.Store.W.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("provenance: sweep: %w", err)
		}
		if err := s.checkEpochTx(ctx, tx, epoch); err != nil {
			_ = tx.Rollback()
			return err
		}
		n, err := s.o.Sounds.SweepStaleTx(ctx, tx, s.env(), picks.BatchKeys)
		if err != nil {
			_ = tx.Rollback()
			if errors.Is(err, errors.ErrUnsupported) {
				return nil
			}
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("provenance: sweep commit: %w", err)
		}
		if n == 0 {
			return nil
		}
	}
}

// Run settles every poll interval while the queue is non-empty and sweeps at start and every sweep interval, until
// ctx ends. When another process holds the lease it waits and retries.
func (s *Service) Run(ctx context.Context) {
	log := s.o.Log
	sweep := func() {
		if err := s.Sweep(ctx); err != nil && !errors.Is(err, ErrSettleBusy) && ctx.Err() == nil {
			log.Warn("provenance sweep failed", "err", err.Error())
		}
	}
	sweep()
	poll, sw := time.NewTicker(s.o.Poll), time.NewTicker(s.o.SweepEvery)
	defer poll.Stop()
	defer sw.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sw.C:
			sweep()
		case <-poll.C:
			pending, err := s.hasPending(ctx)
			if err != nil || !pending {
				continue
			}
			if _, err := s.Settle(ctx); err != nil && !errors.Is(err, ErrSettleBusy) && ctx.Err() == nil {
				log.Warn("provenance settle failed", "err", err.Error())
			}
		}
	}
}
