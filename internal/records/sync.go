package records

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// The Syncer (C122.1, C122.2, D-L2-7, D-L2-8) turns the Pick events of L1 into changelog rows of the Records whose floating
// slots follow them. Everything it writes goes through syncSlotTx, the per-slot step, which is also what an edit runs first
// (catchUpTx). The file name matters: the write-owner guard of F1 reads sync*.go as Syncer code, the only code that may
// write the Syncer-owned kinds, the duplicate removal and record_slots.synced_seq or song_id.

// Syncer consumes pick_events and appends record_changes idempotently. Any number of Syncers (server, CLI) are safe: every
// slot is processed in its own write transaction and a row is unique per (slot, event).
type Syncer struct {
	svc *Service
	log *slog.Logger

	// Test seams (unexported): probe and settle replace Pending and Settle in Run; fault runs at the start of every slot
	// transaction (a test makes it return a SQLITE_BUSY error, or fail one slot); passes counts the batches Settle took.
	probe  func(ctx context.Context) (int, error)
	settle func(ctx context.Context) error
	fault  func(slotID string) error
	passes int
}

// NewSyncer builds a Syncer over s; a nil log means slog.Default().
func NewSyncer(s *Service, log *slog.Logger) *Syncer {
	if log == nil {
		log = slog.Default()
	}
	return &Syncer{svc: s, log: log}
}

// pendingWhere selects the slots with work: live floating slots that have an unconsumed Pick event or sit on a retired Song.
const pendingWhere = `s.removed_at IS NULL AND s.pinned_take_id IS NULL AND (
	EXISTS (SELECT 1 FROM pick_events e WHERE e.song_id = s.song_id AND e.seq > s.synced_seq)
	OR EXISTS (SELECT 1 FROM songs so WHERE so.id = s.song_id AND so.retired_at IS NOT NULL))`

// Pending is the number of slots with events beyond synced_seq, or on a retired Song (one indexed query).
func (y *Syncer) Pending(ctx context.Context) (int, error) {
	var n int
	if err := y.svc.st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM record_slots s WHERE `+pendingWhere).Scan(&n); err != nil {
		return 0, fmt.Errorf("records: pending: %w", err)
	}
	return n, nil
}

// pendingBatch returns up to limit pending slot ids greater than after, in id order.
func pendingBatch(ctx context.Context, q querier, after, recordID string, limit int) ([]string, error) {
	query := `SELECT s.id FROM record_slots s WHERE s.id > ? AND ` + pendingWhere
	args := []any{after}
	if recordID != "" {
		query += ` AND s.record_id = ?`
		args = append(args, recordID)
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY s.id LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("records: pending slots: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Settle drains until no slot has unconsumed events: batches of at most SyncBatch slots, one transaction per slot, so one
// failing slot never blocks the others (it is logged and retried on each sweep while others progress; Skipped counts the
// slots still failing, or found pinned or removed, when Settle returns).
func (y *Syncer) Settle(ctx context.Context) (SyncReport, error) {
	var rep SyncReport
	failed := map[string]bool{}
	for {
		progressed := false
		after := ""
		for {
			ids, err := pendingBatch(ctx, y.svc.st.R, after, "", SyncBatch)
			if err != nil {
				return rep, err
			}
			if len(ids) == 0 {
				break
			}
			y.passes++
			for _, id := range ids {
				var st syncStep
				err := y.svc.writeTx(ctx, func(tx *sql.Tx) error {
					st = syncStep{}
					if y.fault != nil {
						if err := y.fault(id); err != nil {
							return err
						}
					}
					var err error
					st, err = y.svc.syncSlotTx(ctx, tx, id)
					return err
				})
				switch {
				case err != nil && ctx.Err() != nil:
					return rep, ctx.Err()
				case err != nil:
					y.log.Error("records sync failed for a slot", "slot", id, "err", err.Error())
					failed[id] = true
				case st.skipped:
					delete(failed, id)
					rep.Skipped++
				default:
					delete(failed, id)
					if st.touched {
						progressed = true
						rep.Slots++
					}
					rep.Rows += st.rows
					if st.followed {
						rep.Followed++
					}
					if st.removed {
						rep.Removed++
					}
				}
			}
			after = ids[len(ids)-1]
		}
		if !progressed {
			break
		}
	}
	rep.Skipped += len(failed)
	return rep, nil
}

// Run loops until ctx ends: every SyncPoll it asks Pending and settles only when some slot has work.
func (y *Syncer) Run(ctx context.Context) {
	probe, settle := y.probe, y.settle
	if probe == nil {
		probe = y.Pending
	}
	if settle == nil {
		settle = func(ctx context.Context) error { _, err := y.Settle(ctx); return err }
	}
	t := time.NewTicker(SyncPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		n, err := probe(ctx)
		if err != nil {
			if ctx.Err() == nil {
				y.log.Error("records sync probe failed", "err", err.Error())
			}
			continue
		}
		if n == 0 {
			continue
		}
		if err := settle(ctx); err != nil && ctx.Err() == nil {
			y.log.Error("records sync failed", "err", err.Error())
		}
	}
}

// syncStep is what the per-slot step did.
type syncStep struct {
	rows     int  // changelog rows written
	touched  bool // the cursor moved or the slot was followed
	followed bool
	removed  bool
	skipped  bool // the slot was pinned or removed meanwhile
}

// catchUpTx logs every pending Pick event of a Record's floating slots inside the caller's transaction, so history is never
// reordered around an edit (C121.3 step 2).
func (s *Service) catchUpTx(ctx context.Context, tx *sql.Tx, recordID string) error {
	var all []string
	after := ""
	for {
		ids, err := pendingBatch(ctx, tx, after, recordID, SyncBatch)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			break
		}
		all = append(all, ids...)
		after = ids[len(ids)-1]
	}
	for _, id := range all {
		if _, err := s.syncSlotTx(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

type pickEvent struct {
	id, reason string
	seq        int
	from, to   *string
	evidence   string
}

// syncSlotTx is the per-slot step (C122.2): re-read the slot, follow a retired Song, else log its unconsumed events.
func (s *Service) syncSlotTx(ctx context.Context, tx *sql.Tx, slotID string) (syncStep, error) {
	var recID, songID string
	var pinned, removed *string
	var synced int
	err := tx.QueryRowContext(ctx, `SELECT record_id, song_id, pinned_take_id, removed_at, synced_seq FROM record_slots WHERE id = ?`, slotID).
		Scan(&recID, &songID, &pinned, &removed, &synced)
	if errors.Is(err, sql.ErrNoRows) {
		return syncStep{skipped: true}, nil
	}
	if err != nil {
		return syncStep{}, fmt.Errorf("records: read slot: %w", err)
	}
	if pinned != nil || removed != nil {
		return syncStep{skipped: true}, nil
	}
	var retiredAt, into *string
	if err := tx.QueryRowContext(ctx, `SELECT retired_at, retired_into FROM songs WHERE id = ?`, songID).Scan(&retiredAt, &into); err != nil {
		return syncStep{}, fmt.Errorf("records: read song: %w", err)
	}
	if retiredAt != nil && into != nil {
		return s.followTx(ctx, tx, recID, slotID, songID, *into)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, seq, from_take_id, to_take_id, reason, evidence_json FROM pick_events WHERE song_id = ? AND seq > ? ORDER BY seq`, songID, synced)
	if err != nil {
		return syncStep{}, fmt.Errorf("records: read pick events: %w", err)
	}
	var events []pickEvent
	for rows.Next() {
		var e pickEvent
		if err := rows.Scan(&e.id, &e.seq, &e.from, &e.to, &e.reason, &e.evidence); err != nil {
			rows.Close()
			return syncStep{}, err
		}
		events = append(events, e)
	}
	if err := rows.Close(); err != nil {
		return syncStep{}, err
	}
	if err := rows.Err(); err != nil {
		return syncStep{}, err
	}
	if len(events) == 0 {
		return syncStep{}, nil
	}
	pos, err := slotPosition(ctx, tx, recID, slotID)
	if err != nil {
		return syncStep{}, err
	}
	at := s.stamp()
	var st syncStep
	for _, e := range events {
		c := Change{SlotID: &slotID, Position: pos, FromTakeID: e.from, ToTakeID: e.to, Reason: strp(e.reason), PickEventID: strp(e.id),
			Evidence: json.RawMessage(e.evidence)}
		switch {
		case e.to == nil:
			c.Kind, c.Reason = KindSlotEmptied, strp(ReasonEmptied)
		case e.from == nil:
			c.Kind = KindSlotFilled
		case *e.from != *e.to:
			c.Kind = KindSlotUpgraded
		default:
			continue // the same Take under another source: what the slot plays did not change
		}
		if Undecided(e.reason) {
			c.Detail = json.RawMessage(`{"undecided":true}`)
		}
		ok, err := s.insertSyncRow(ctx, tx, recID, at, c)
		if err != nil {
			return syncStep{}, err
		}
		if ok {
			st.rows++
		}
	}
	last := events[len(events)-1].seq
	if _, err := tx.ExecContext(ctx, `UPDATE record_slots SET synced_seq = ? WHERE id = ? AND synced_seq < ?`, last, slotID, last); err != nil {
		return syncStep{}, fmt.Errorf("records: advance cursor: %w", err)
	}
	st.touched = true
	return st, nil
}

// followTx follows a Song that was retired into target (C122.2.1, D-L2-8).
func (s *Service) followTx(ctx context.Context, tx *sql.Tx, recID, slotID, songID, target string) (syncStep, error) {
	pos, err := slotPosition(ctx, tx, recID, slotID)
	if err != nil {
		return syncStep{}, err
	}
	from, err := songPickTake(ctx, tx, songID)
	if err != nil {
		return syncStep{}, err
	}
	at := s.stamp()
	var dup int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM record_slots WHERE record_id = ? AND song_id = ? AND removed_at IS NULL`, recID, target).Scan(&dup); err != nil {
		return syncStep{}, fmt.Errorf("records: read duplicate: %w", err)
	}
	if dup > 0 {
		var orderJSON string
		if err := tx.QueryRowContext(ctx, `SELECT order_json FROM records WHERE id = ?`, recID).Scan(&orderJSON); err != nil {
			return syncStep{}, fmt.Errorf("records: read order: %w", err)
		}
		var order []string
		if err := json.Unmarshal([]byte(orderJSON), &order); err != nil {
			return syncStep{}, fmt.Errorf("records: decode order: %w", err)
		}
		kept := make([]string, 0, len(order))
		for _, id := range order {
			if id != slotID {
				kept = append(kept, id)
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE record_slots SET removed_by = ?, removed_at = ? WHERE id = ?`, SyncerActor, at, slotID); err != nil {
			return syncStep{}, fmt.Errorf("records: remove duplicate slot: %w", err)
		}
		if err := writeOrder(ctx, tx, recID, kept); err != nil {
			return syncStep{}, err
		}
		if _, err := s.insertSyncRow(ctx, tx, recID, at, Change{Kind: KindSlotRemoved, SlotID: &slotID, Position: pos, FromTakeID: from, Reason: strp(ReasonDuplicate)}); err != nil {
			return syncStep{}, err
		}
		return syncStep{rows: 1, touched: true, removed: true}, nil
	}
	to, err := songPickTake(ctx, tx, target)
	if err != nil {
		return syncStep{}, err
	}
	seq, err := songMaxSeq(ctx, tx, target)
	if err != nil {
		return syncStep{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE record_slots SET song_id = ?, synced_seq = ? WHERE id = ?`, target, seq, slotID); err != nil {
		return syncStep{}, fmt.Errorf("records: follow retired song: %w", err)
	}
	if _, err := s.insertSyncRow(ctx, tx, recID, at, Change{Kind: KindSlotSongFollowed, SlotID: &slotID, Position: pos, FromTakeID: from, ToTakeID: to, Reason: strp(ReasonRetired)}); err != nil {
		return syncStep{}, err
	}
	return syncStep{rows: 1, touched: true, followed: true}, nil
}

// slotPosition is the 1-based position of a live slot in its Record's order, nil when it is not listed.
func slotPosition(ctx context.Context, q querier, recID, slotID string) (*int, error) {
	var p int
	err := q.QueryRowContext(ctx, `SELECT j.key + 1 FROM records r, json_each(r.order_json) j WHERE r.id = ? AND j.value = ?`, recID, slotID).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("records: read position: %w", err)
	}
	return &p, nil
}

// insertSyncRow appends one Syncer row at the Record's next seq. A row for an event the slot already logged is dropped by
// the unique (slot, event) index; the result says whether a row was written.
func (s *Service) insertSyncRow(ctx context.Context, tx *sql.Tx, recID, at string, c Change) (bool, error) {
	var seq int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM record_changes WHERE record_id = ?`, recID).Scan(&seq); err != nil {
		return false, fmt.Errorf("records: next seq: %w", err)
	}
	var evidence, detail any
	if len(c.Evidence) > 0 {
		evidence = string(c.Evidence)
	}
	if len(c.Detail) > 0 {
		detail = string(c.Detail)
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO record_changes(id, record_id, seq, at, kind, slot_id, position, from_take_id, to_take_id, reason, pick_event_id,
		evidence_json, detail_json, actor, actor_kind, actor_name) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(slot_id, pick_event_id) WHERE pick_event_id IS NOT NULL DO NOTHING`,
		s.newID(), recID, seq, at, c.Kind, c.SlotID, c.Position, c.FromTakeID, c.ToTakeID, c.Reason, c.PickEventID, evidence, detail,
		SyncerActor, SyncerActorKind, SyncerActorName)
	if err != nil {
		return false, fmt.Errorf("records: write %s row: %w", c.Kind, err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
