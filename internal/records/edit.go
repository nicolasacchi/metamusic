package records

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// withOp adds the index of the failing operation to a service error (C121.3: every error carries details.op).
func withOp(err error, i int) error {
	var pe *problem.Error
	if errors.As(err, &pe) {
		d := make(map[string]any, len(pe.Details)+1)
		for k, v := range pe.Details {
			d[k] = v
		}
		d["op"] = i
		return problem.E(pe.Status, pe.Code, pe.Message, d)
	}
	return err
}

// edit is the working state of one EditSlots call: the live slots as the previous operation left them, and the rows written.
type edit struct {
	s        *Service
	ctx      context.Context
	tx       *sql.Tx
	rec      Record
	p        auth.Principal
	at       string
	list     []*Slot
	changes  []Change
	warnings []string
}

func (e *edit) emit(c Change) error {
	c, err := e.s.insertChangeRow(e.ctx, e.tx, e.rec.ID, e.p, e.at, c)
	if err != nil {
		return err
	}
	e.changes = append(e.changes, c)
	return nil
}

func (e *edit) find(id string) (int, error) {
	for i, sl := range e.list {
		if sl.ID == id {
			return i, nil
		}
	}
	return -1, problem.E(422, "UNKNOWN_SLOT", "no such slot in this record", map[string]any{"slot_id": id})
}

// resolvedTake is the Take the slot plays now: its pin, else the Song's current Pick (read inside the transaction).
func (e *edit) resolvedTake(sl *Slot) (*string, error) {
	if sl.PinnedTakeID != nil {
		return sl.PinnedTakeID, nil
	}
	return songPickTake(e.ctx, e.tx, sl.SongID)
}

func invalidPosition(pos, hi int) error {
	return problem.E(422, "INVALID_POSITION", "position out of range", map[string]any{"position": pos, "min": 1, "max": hi})
}

// checkTake enforces that a Take belongs to the Song and is not discarded (TAKE_NOT_IN_SONG, TAKE_NOT_ELIGIBLE).
func (e *edit) checkTake(songID, takeID string) error {
	var song, status sql.NullString
	err := e.tx.QueryRowContext(e.ctx, `SELECT ts.song_id, t.status FROM tracks t LEFT JOIN take_songs ts ON ts.track_id = t.id WHERE t.id = ?`, takeID).Scan(&song, &status)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("records: read take: %w", err)
	}
	if errors.Is(err, sql.ErrNoRows) || song.String != songID {
		return problem.E(422, "TAKE_NOT_IN_SONG", "the take does not belong to the song", map[string]any{"take_id": takeID, "song_id": songID})
	}
	if status.String == "discarded" {
		return problem.E(422, "TAKE_NOT_ELIGIBLE", "the take is discarded", map[string]any{"take_id": takeID, "status": status.String})
	}
	return nil
}

func (e *edit) positions() map[string]int {
	m := make(map[string]int, len(e.list))
	for i, sl := range e.list {
		m[sl.ID] = i + 1
	}
	return m
}

// emitMoves writes one slot_moved row per slot whose position differs from before, in the order of the new positions.
func (e *edit) emitMoves(before map[string]int) error {
	for i, sl := range e.list {
		if old, ok := before[sl.ID]; ok && old != i+1 {
			if err := e.emit(Change{Kind: KindSlotMoved, SlotID: strp(sl.ID), Position: intp(i + 1), Detail: []byte(fmt.Sprintf(`{"from":%d}`, old))}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *edit) add(op SlotOp) error {
	if err := requireLiveSong(e.ctx, e.tx, op.SongID); err != nil {
		return err
	}
	for _, sl := range e.list {
		if sl.SongID == op.SongID {
			return problem.E(409, "SONG_ALREADY_IN_RECORD", "the song already has a slot in this record", map[string]any{"song_id": op.SongID, "slot_id": sl.ID})
		}
	}
	n, pos := len(e.list), op.Position
	if pos == PositionEnd {
		pos = n + 1
	}
	if pos < 1 || pos > n+1 {
		return invalidPosition(op.Position, n+1)
	}
	var pin, side *string
	if op.TakeID != "" {
		if err := e.checkTake(op.SongID, op.TakeID); err != nil {
			return err
		}
		pin = strp(op.TakeID)
	}
	if op.Side.Set && !op.Side.Null {
		side = strp(op.Side.Value)
	}
	sl, err := e.s.insertSlot(e.ctx, e.tx, e.rec.ID, e.p, e.at, op.SongID, pin, side)
	if err != nil {
		return err
	}
	e.list = slices.Insert(e.list, pos-1, &sl)
	to, reason := pin, ReasonPin
	if pin == nil {
		reason = ReasonInitial
		if to, err = songPickTake(e.ctx, e.tx, op.SongID); err != nil {
			return err
		}
	}
	return e.emit(Change{Kind: KindSlotAdded, SlotID: &sl.ID, Position: intp(pos), ToTakeID: to, Reason: &reason})
}

func (e *edit) remove(op SlotOp) error {
	i, err := e.find(op.SlotID)
	if err != nil {
		return err
	}
	sl := e.list[i]
	from, err := e.resolvedTake(sl)
	if err != nil {
		return err
	}
	if _, err := e.tx.ExecContext(e.ctx, `UPDATE record_slots SET removed_by = ?, removed_at = ? WHERE id = ?`, e.p.Key(), e.at, sl.ID); err != nil {
		return fmt.Errorf("records: remove slot: %w", err)
	}
	e.list = slices.Delete(e.list, i, i+1)
	return e.emit(Change{Kind: KindSlotRemoved, SlotID: &sl.ID, Position: intp(i + 1), FromTakeID: from})
}

func (e *edit) move(op SlotOp) error {
	i, err := e.find(op.SlotID)
	if err != nil {
		return err
	}
	if op.Position < 1 || op.Position > len(e.list) {
		return invalidPosition(op.Position, len(e.list))
	}
	before := e.positions()
	sl := e.list[i]
	e.list = slices.Insert(slices.Delete(e.list, i, i+1), op.Position-1, sl)
	return e.emitMoves(before)
}

func (e *edit) reorder(op SlotOp) error {
	live := map[string]*Slot{}
	for _, sl := range e.list {
		live[sl.ID] = sl
	}
	seen := map[string]bool{}
	var extra, missing []string
	for _, id := range op.SlotIDs {
		if live[id] == nil || seen[id] {
			extra = append(extra, id)
		}
		seen[id] = true
	}
	for _, sl := range e.list {
		if !seen[sl.ID] {
			missing = append(missing, sl.ID)
		}
	}
	if len(extra) > 0 || len(missing) > 0 {
		return problem.E(422, "SLOT_ORDER_MISMATCH", "slot_ids must list exactly the live slots", map[string]any{"missing": nonNilStrings(missing), "extra": nonNilStrings(extra)})
	}
	before := e.positions()
	e.list = e.list[:0:0]
	for _, id := range op.SlotIDs {
		e.list = append(e.list, live[id])
	}
	return e.emitMoves(before)
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// pinTo pins a floating or pinned slot to takeID (no eligibility check; the callers did it) and writes the row.
func (e *edit) pinTo(i int, takeID string) error {
	sl := e.list[i]
	from, err := e.resolvedTake(sl)
	if err != nil {
		return err
	}
	if _, err := e.tx.ExecContext(e.ctx, `UPDATE record_slots SET pinned_take_id = ?, pinned_by = ?, pinned_at = ? WHERE id = ?`, takeID, e.p.Key(), e.at, sl.ID); err != nil {
		return fmt.Errorf("records: pin slot: %w", err)
	}
	sl.PinnedTakeID, sl.PinnedBy, sl.PinnedAt = strp(takeID), strp(e.p.Key()), strp(e.at)
	return e.emit(Change{Kind: KindSlotPinned, SlotID: &sl.ID, Position: intp(i + 1), FromTakeID: from, ToTakeID: strp(takeID), Reason: strp(ReasonPin)})
}

func (e *edit) pin(op SlotOp) error {
	i, err := e.find(op.SlotID)
	if err != nil {
		return err
	}
	sl := e.list[i]
	if err := e.checkTake(sl.SongID, op.TakeID); err != nil {
		return err
	}
	if sl.PinnedTakeID != nil && *sl.PinnedTakeID == op.TakeID {
		return nil
	}
	return e.pinTo(i, op.TakeID)
}

// unpinAt makes a pinned slot floating again; its cursor restarts at the Song's current max pick event (D-L2-7).
func (e *edit) unpinAt(i int) error {
	sl := e.list[i]
	if sl.PinnedTakeID == nil {
		return nil
	}
	if _, err := e.tx.ExecContext(e.ctx, `UPDATE record_slots SET pinned_take_id = NULL, pinned_by = NULL, pinned_at = NULL WHERE id = ?`, sl.ID); err != nil {
		return fmt.Errorf("records: unpin slot: %w", err)
	}
	seq, err := restartCursorTx(e.ctx, e.tx, sl.ID, sl.SongID, sl.SyncedSeq)
	if err != nil {
		return err
	}
	from := sl.PinnedTakeID
	sl.PinnedTakeID, sl.PinnedBy, sl.PinnedAt, sl.SyncedSeq = nil, nil, nil, seq
	to, err := songPickTake(e.ctx, e.tx, sl.SongID)
	if err != nil {
		return err
	}
	return e.emit(Change{Kind: KindSlotUnpinned, SlotID: &sl.ID, Position: intp(i + 1), FromTakeID: from, ToTakeID: to, Reason: strp(ReasonUnpin)})
}

func (e *edit) setSide(op SlotOp) error {
	if !op.Side.Set {
		return errInvalid("side", "side is required for set_side (a letter A to D, or null to clear)")
	}
	i, err := e.find(op.SlotID)
	if err != nil {
		return err
	}
	sl := e.list[i]
	var side *string
	detail := `{"side":null}`
	if !op.Side.Null {
		side, detail = strp(op.Side.Value), `{"side":"`+op.Side.Value+`"}`
	}
	if (side == nil && sl.Side == nil) || (side != nil && sl.Side != nil && *side == *sl.Side) {
		return nil
	}
	if _, err := e.tx.ExecContext(e.ctx, `UPDATE record_slots SET side = ? WHERE id = ?`, side, sl.ID); err != nil {
		return fmt.Errorf("records: set side: %w", err)
	}
	sl.Side = side
	return e.emit(Change{Kind: KindSlotSide, SlotID: &sl.ID, Position: intp(i + 1), Detail: []byte(detail)})
}

func (e *edit) pinAll() error {
	for i, sl := range e.list {
		if sl.PinnedTakeID != nil {
			continue
		}
		pick, err := songPickTake(e.ctx, e.tx, sl.SongID)
		if err != nil {
			return err
		}
		if pick == nil {
			e.warnings = append(e.warnings, "pin_all_skipped:"+sl.ID)
			continue
		}
		if err := e.pinTo(i, *pick); err != nil {
			return err
		}
	}
	return nil
}

func (e *edit) unpinAll() error {
	for i := range e.list {
		if err := e.unpinAt(i); err != nil {
			return err
		}
	}
	return nil
}

func (e *edit) apply(op SlotOp) error {
	switch op.Op {
	case "add":
		return e.add(op)
	case "remove":
		return e.remove(op)
	case "move":
		return e.move(op)
	case "reorder":
		return e.reorder(op)
	case "pin":
		return e.pin(op)
	case "unpin":
		i, err := e.find(op.SlotID)
		if err != nil {
			return err
		}
		return e.unpinAt(i)
	case "set_side":
		return e.setSide(op)
	case "pin_all":
		return e.pinAll()
	case "unpin_all":
		return e.unpinAll()
	}
	return errInvalid("op", "op must be add, remove, move, reorder, pin, unpin, set_side, pin_all or unpin_all")
}

// EditSlots applies ops in one write transaction (C121.3). A call that changes nothing (every operation was a no-op)
// writes no row and does not bump the revision; the catch-up rows R-b's seam may have written still commit.
func (s *Service) EditSlots(ctx context.Context, p auth.Principal, id string, revision int, ops []SlotOp) (EditResult, error) {
	if len(ops) == 0 {
		return EditResult{}, errInvalid("ops", "at least one operation is required")
	}
	if len(ops) > MaxEditOps {
		return EditResult{}, errInvalid("ops", fmt.Sprintf("at most %d operations per call", MaxEditOps))
	}
	var out EditResult
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		out = EditResult{}
		rec, err := loadRecord(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := requireEditable(p, rec); err != nil {
			return err
		}
		if rec.Revision != revision {
			return errStale(rec)
		}
		if err := s.catchUpTx(ctx, tx, id); err != nil {
			return err
		}
		slots, err := loadSlots(ctx, tx, id)
		if err != nil {
			return err
		}
		e := &edit{s: s, ctx: ctx, tx: tx, rec: rec, p: p, at: s.stamp()}
		for i := range slots {
			e.list = append(e.list, &slots[i])
		}
		for i, op := range ops {
			if err := e.apply(op); err != nil {
				return withOp(err, i)
			}
		}
		if len(e.list) > MaxSlots {
			return problem.E(422, "TOO_MANY_SLOTS", "a record holds at most 60 slots", map[string]any{"limit": MaxSlots, "slots": len(e.list)})
		}
		if len(e.changes) == 0 {
			out = EditResult{Record: rec, Warnings: e.warnings}
			return nil
		}
		order := make([]string, len(e.list))
		for i, sl := range e.list {
			order[i] = sl.ID
		}
		if err := writeOrder(ctx, tx, id, order); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE records SET revision = revision + 1, updated_by = ?, updated_at = ? WHERE id = ?`, p.Key(), e.at, id); err != nil {
			return fmt.Errorf("records: bump revision: %w", err)
		}
		dismissed, err := s.dismissProposal(ctx, tx, rec, p, e.at, "edited")
		if err != nil {
			return err
		}
		e.changes = append(e.changes, dismissed...)
		updated, err := loadRecord(ctx, tx, id)
		if err != nil {
			return err
		}
		out = EditResult{Record: updated, Changes: e.changes, Warnings: e.warnings}
		return nil
	})
	if err != nil {
		return EditResult{}, err
	}
	return out, nil
}
