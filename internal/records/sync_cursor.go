package records

import (
	"context"
	"database/sql"
	"fmt"
)

// restartCursorTx moves a slot's Syncer cursor (record_slots.synced_seq) to the Song's current max pick event, never
// backwards, and returns it. EditSlots calls it when an unpin makes a slot floating again (C121.3, D-L2-7): the events that
// happened while the slot was pinned must not be logged as upgrades afterwards. The cursor belongs to the Syncer
// (C119.2), so this one write lives in a sync*.go file, which the write-owner guard of F1 reads as Syncer code.
func restartCursorTx(ctx context.Context, tx *sql.Tx, slotID, songID string, current int) (int, error) {
	seq, err := songMaxSeq(ctx, tx, songID)
	if err != nil {
		return 0, err
	}
	seq = max(seq, current)
	if _, err := tx.ExecContext(ctx, `UPDATE record_slots SET synced_seq = ? WHERE id = ?`, seq, slotID); err != nil {
		return 0, fmt.Errorf("records: restart cursor: %w", err)
	}
	return seq, nil
}
