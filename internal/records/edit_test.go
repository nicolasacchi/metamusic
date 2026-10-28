package records_test

import (
	"context"
	"reflect"
	"strconv"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/records"
)

// TestEditSlots_Table is C121.3: 24 rows over a Record of S1, S2, S3 (slots a, b, c) edited by an agent.
func TestEditSlots_Table(t *testing.T) {
	t.Parallel()
	type env struct {
		r       *rig
		rec     records.Record
		a, b, c string // the slot ids
		scale   bool
		songOfN func(n int) string
	}
	type wantErr struct {
		status  int
		code    string
		op      int
		details map[string]any
	}
	add := func(song string) records.SlotOp { return records.SlotOp{Op: "add", SongID: song} }
	for _, c := range []struct {
		name  string
		setup func(e *env)
		scale bool
		ops   func(e *env) []records.SlotOp
		err   *wantErr
		kinds []string
		check func(t *testing.T, e *env, res records.EditResult)
	}{
		{name: "01 add defaults to the end", ops: func(e *env) []records.SlotOp {
			return []records.SlotOp{{Op: "add", SongID: e.r.song("S4"), Position: records.PositionEnd}}
		},
			kinds: []string{"slot_added"}, check: func(t *testing.T, e *env, res records.EditResult) {
				sl := e.r.slots(e.rec.ID)
				if len(sl) != 4 || sl[3].SongID != e.r.song("S4") || *res.Changes[0].Position != 4 {
					t.Errorf("slots %+v change %+v", sl, res.Changes[0])
				}
				if res.Changes[0].ToTakeID == nil || *res.Changes[0].ToTakeID != e.r.fx.Takes["c1"] {
					t.Errorf("to = %v, want the Pick take c1", res.Changes[0].ToTakeID)
				}
			}},
		{name: "02 add at position 1", ops: func(e *env) []records.SlotOp {
			return []records.SlotOp{{Op: "add", SongID: e.r.song("S4"), Position: 1}}
		},
			kinds: []string{"slot_added"}, check: func(t *testing.T, e *env, res records.EditResult) {
				sl := e.r.slots(e.rec.ID)
				same(t, "order", []string{sl[0].SongID, sl[1].ID, sl[2].ID, sl[3].ID}, []string{e.r.song("S4"), e.a, e.b, e.c})
			}},
		{name: "03 add at n+1", ops: func(e *env) []records.SlotOp {
			return []records.SlotOp{{Op: "add", SongID: e.r.song("S4"), Position: 4}}
		},
			kinds: []string{"slot_added"}, check: func(t *testing.T, e *env, res records.EditResult) {
				if sl := e.r.slots(e.rec.ID); sl[3].SongID != e.r.song("S4") {
					t.Errorf("slots %+v", sl)
				}
			}},
		{name: "04 add position 0", ops: func(e *env) []records.SlotOp {
			return []records.SlotOp{{Op: "add", SongID: e.r.song("S4"), Position: 0}}
		},
			err: &wantErr{422, "INVALID_POSITION", 0, map[string]any{"position": 0, "min": 1, "max": 4}}},
		{name: "05 add position n+2", ops: func(e *env) []records.SlotOp {
			return []records.SlotOp{{Op: "add", SongID: e.r.song("S4"), Position: 5}}
		},
			err: &wantErr{422, "INVALID_POSITION", 0, nil}},
		{name: "06 add with a take is pinned", ops: func(e *env) []records.SlotOp {
			return []records.SlotOp{{Op: "add", SongID: e.r.song("S4"), TakeID: e.r.fx.Takes["c2"], Position: records.PositionEnd, Side: records.OptString{Set: true, Value: "B"}}}
		},
			kinds: []string{"slot_added"}, check: func(t *testing.T, e *env, res records.EditResult) {
				sl := e.r.slots(e.rec.ID)[3]
				if sl.PinnedTakeID == nil || *sl.PinnedTakeID != e.r.fx.Takes["c2"] || sl.PinnedBy == nil || sl.Side == nil || *sl.Side != "B" {
					t.Errorf("slot = %+v", sl)
				}
				ch := res.Changes[0]
				if *ch.ToTakeID != e.r.fx.Takes["c2"] || *ch.Reason != "pin" {
					t.Errorf("row = %+v", ch)
				}
			}},
		{name: "07 add another song's take", ops: func(e *env) []records.SlotOp {
			return []records.SlotOp{{Op: "add", SongID: e.r.song("S4"), TakeID: e.r.fx.Takes["a1"], Position: records.PositionEnd}}
		}, err: &wantErr{422, "TAKE_NOT_IN_SONG", 0, nil}},
		{name: "08 add a discarded take", setup: func(e *env) { e.r.exec(`UPDATE tracks SET status = 'discarded' WHERE id = ?`, e.r.fx.Takes["c2"]) },
			ops: func(e *env) []records.SlotOp {
				return []records.SlotOp{{Op: "add", SongID: e.r.song("S4"), TakeID: e.r.fx.Takes["c2"], Position: records.PositionEnd}}
			}, err: &wantErr{422, "TAKE_NOT_ELIGIBLE", 0, map[string]any{"status": "discarded"}}},
		{name: "09 duplicate song", ops: func(e *env) []records.SlotOp { return []records.SlotOp{add(e.r.song("S2"))} },
			err: &wantErr{409, "SONG_ALREADY_IN_RECORD", 0, nil}},
		{name: "10 retired song", setup: func(e *env) {
			e.r.exec(`UPDATE songs SET retired_at = '2026-10-06T00:00:00.000Z', retired_into = ? WHERE id = ?`, e.r.song("S1"), e.r.song("S7"))
		}, ops: func(e *env) []records.SlotOp { return []records.SlotOp{add(e.r.song("S7"))} },
			err: &wantErr{409, "SONG_RETIRED", 0, nil}},
		{name: "11 hidden song is fine", setup: func(e *env) {
			e.r.exec(`UPDATE songs SET hidden_at = '2026-10-06T00:00:00.000Z', hidden_by = 'human:fixture' WHERE id = ?`, e.r.song("S4"))
		}, ops: func(e *env) []records.SlotOp {
			return []records.SlotOp{{Op: "add", SongID: e.r.song("S4"), Position: records.PositionEnd}}
		},
			kinds: []string{"slot_added"}},
		{name: "12 the 61st slot rolls back", setup: func(e *env) {}, scale: true,
			ops: func(e *env) []records.SlotOp {
				return []records.SlotOp{{Op: "add", SongID: e.songOfN(60), Position: records.PositionEnd}, {Op: "add", SongID: e.songOfN(61), Position: records.PositionEnd}, {Op: "set_side", SlotID: e.a, Side: records.OptString{Set: true, Value: "A"}}}
			}, err: &wantErr{422, "TOO_MANY_SLOTS", -1, nil}},
		{name: "13 remove then re-add gives a new slot", ops: func(e *env) []records.SlotOp {
			return []records.SlotOp{{Op: "remove", SlotID: e.b}, {Op: "add", SongID: e.r.song("S2"), Position: records.PositionEnd}}
		}, kinds: []string{"slot_removed", "slot_added"}, check: func(t *testing.T, e *env, res records.EditResult) {
			sl := e.r.slots(e.rec.ID)
			if len(sl) != 3 || sl[2].SongID != e.r.song("S2") || sl[2].ID == e.b {
				t.Errorf("slots = %+v", sl)
			}
			if *res.Changes[0].Position != 2 || *res.Changes[0].FromTakeID != e.r.fx.Takes["a1"] {
				t.Errorf("removed row = %+v", res.Changes[0])
			}
			if n := count(t, e.r, `SELECT COUNT(*) FROM record_slots WHERE id = ? AND removed_at IS NOT NULL AND removed_by IS NOT NULL`, e.b); n != 1 {
				t.Error("the old slot is not soft-removed")
			}
		}},
		{name: "14 move to the same position", ops: func(e *env) []records.SlotOp { return []records.SlotOp{{Op: "move", SlotID: e.b, Position: 2}} },
			kinds: []string{}, check: func(t *testing.T, e *env, res records.EditResult) {
				if e.r.get(e.rec.ID).Revision != 1 {
					t.Error("a no-op call bumped the revision")
				}
			}},
		{name: "15 move 1 to 3", ops: func(e *env) []records.SlotOp { return []records.SlotOp{{Op: "move", SlotID: e.a, Position: 3}} },
			kinds: []string{"slot_moved", "slot_moved", "slot_moved"}, check: func(t *testing.T, e *env, res records.EditResult) {
				same(t, "order", e.r.slotIDs(e.rec.ID), []string{e.b, e.c, e.a})
				var got []string
				for _, c := range res.Changes {
					got = append(got, *c.SlotID+string(c.Detail)+strconv.Itoa(*c.Position))
				}
				same(t, "rows", got, []string{e.b + `{"from":2}1`, e.c + `{"from":3}2`, e.a + `{"from":1}3`})
			}},
		{name: "16 move out of range", ops: func(e *env) []records.SlotOp { return []records.SlotOp{{Op: "move", SlotID: e.a, Position: 4}} },
			err: &wantErr{422, "INVALID_POSITION", 0, nil}},
		{name: "17 reorder with a missing id", ops: func(e *env) []records.SlotOp { return []records.SlotOp{{Op: "reorder", SlotIDs: []string{e.c, e.a}}} },
			err: &wantErr{422, "SLOT_ORDER_MISMATCH", 0, map[string]any{"missing": func(e *env) any { return []string{e.b} }, "extra": []string{}}}},
		{name: "18 reorder identity", ops: func(e *env) []records.SlotOp {
			return []records.SlotOp{{Op: "reorder", SlotIDs: []string{e.a, e.b, e.c}}}
		},
			kinds: []string{}},
		{name: "19 reorder swap writes two rows", ops: func(e *env) []records.SlotOp {
			return []records.SlotOp{{Op: "reorder", SlotIDs: []string{e.b, e.a, e.c}}}
		},
			kinds: []string{"slot_moved", "slot_moved"}, check: func(t *testing.T, e *env, res records.EditResult) {
				same(t, "order", e.r.slotIDs(e.rec.ID), []string{e.b, e.a, e.c})
			}},
		{name: "20 set_side A then null", ops: func(e *env) []records.SlotOp {
			return []records.SlotOp{{Op: "set_side", SlotID: e.b, Side: records.OptString{Set: true, Value: "A"}}, {Op: "set_side", SlotID: e.b, Side: records.OptString{Set: true, Null: true}},
				{Op: "set_side", SlotID: e.b, Side: records.OptString{Set: true, Null: true}}}
		}, kinds: []string{"slot_side", "slot_side"}, check: func(t *testing.T, e *env, res records.EditResult) {
			same(t, "details", []string{string(res.Changes[0].Detail), string(res.Changes[1].Detail)}, []string{`{"side":"A"}`, `{"side":null}`})
			if e.r.slots(e.rec.ID)[1].Side != nil {
				t.Error("side not cleared")
			}
		}},
		{name: "21 set_side without side", ops: func(e *env) []records.SlotOp { return []records.SlotOp{{Op: "set_side", SlotID: e.b}} },
			err: &wantErr{422, "VALIDATION_FAILED", 0, map[string]any{"field": "side"}}},
		{name: "22 unknown slot carries the op index", ops: func(e *env) []records.SlotOp {
			return []records.SlotOp{{Op: "move", SlotID: e.a, Position: 2}, {Op: "remove", SlotID: "ghost"}}
		}, err: &wantErr{422, "UNKNOWN_SLOT", 1, map[string]any{"slot_id": "ghost"}}},
		{name: "23 a failing second op writes nothing", ops: func(e *env) []records.SlotOp {
			return []records.SlotOp{{Op: "add", SongID: e.r.song("S4"), Position: records.PositionEnd}, {Op: "add", SongID: e.r.song("S4")}, {Op: "remove", SlotID: e.a}}
		}, err: &wantErr{409, "SONG_ALREADY_IN_RECORD", 1, nil}},
		{name: "24 unknown op", ops: func(e *env) []records.SlotOp { return []records.SlotOp{{Op: "explode"}} },
			err: &wantErr{422, "VALIDATION_FAILED", 0, map[string]any{"field": "op"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := &env{scale: c.scale}
			if c.scale {
				e.r = newRigWith(t, true)
				e.songOfN = func(n int) string { return e.r.song("S" + strconv.Itoa(n)) }
				// 59 slots: the two adds of the row reach 61
				var ids []string
				for i := 1; i <= 59; i++ {
					ids = append(ids, e.songOfN(i))
				}
				rec, err := e.r.svc.Create(context.Background(), agent, records.NewRecord{Title: "full", SongIDs: ids})
				if err != nil {
					t.Fatal(err)
				}
				e.rec = rec
				e.a = e.r.slotIDs(rec.ID)[0]
			} else {
				e.r = newRig(t)
				if c.setup != nil {
					c.setup(e)
				}
				e.rec = e.r.newRecord(agent, "S1", "S2", "S3")
				ids := e.r.slotIDs(e.rec.ID)
				e.a, e.b, e.c = ids[0], ids[1], ids[2]
			}
			beforeOrder, beforeRows := e.r.slotIDs(e.rec.ID), len(e.r.changes(e.rec.ID))
			res, err := edit(t, e.r, agent, e.rec, c.ops(e)...)
			if c.err != nil {
				pe := wantProblem(t, err, c.err.status, c.err.code)
				if c.err.op >= 0 && pe.Details["op"] != c.err.op {
					t.Errorf("details.op = %v, want %d", pe.Details["op"], c.err.op)
				}
				for k, want := range c.err.details {
					if f, ok := want.(func(*env) any); ok {
						want = f(e)
					}
					if got := pe.Details[k]; !reflect.DeepEqual(got, want) {
						t.Errorf("details.%s = %#v, want %#v", k, got, want)
					}
				}
				// all or nothing
				same(t, "order after a refused call", e.r.slotIDs(e.rec.ID), beforeOrder)
				if got := e.r.get(e.rec.ID).Revision; got != 1 {
					t.Errorf("revision = %d after a refused call", got)
				}
				if n := len(e.r.changes(e.rec.ID)); n != beforeRows {
					t.Errorf("%d rows written by a refused call", n-beforeRows)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			same(t, "kinds", kinds(res.Changes), c.kinds)
			same(t, "rows in the table", len(e.r.changes(e.rec.ID))-beforeRows, len(c.kinds))
			if len(c.kinds) > 0 {
				if res.Record.Revision != 2 || res.Record.UpdatedBy != agent.Key() {
					t.Errorf("record after = rev %d by %s", res.Record.Revision, res.Record.UpdatedBy)
				}
			}
			for _, ch := range res.Changes {
				if ch.Actor != agent.Key() || ch.ActorKind != "agent" || ch.ActorName != "synthetic-agent" {
					t.Errorf("row actor = %+v", ch)
				}
			}
			if c.check != nil {
				c.check(t, e, res)
			}
		})
	}
}
