package records_test

import (
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/records"
)

func TestEditSlots_Pin_Rules(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.newRecord(agent, "S4", "S3")
	s4, s3 := r.slotIDs(rec.ID)[0], r.slotIDs(rec.ID)[1]
	c1, c2 := r.fx.Takes["c1"], r.fx.Takes["c2"]
	rev := func() records.Record { return r.get(rec.ID) }
	do := func(ops ...records.SlotOp) (records.EditResult, error) { return edit(t, r, agent, rev(), ops...) }

	// pin an eligible Take: from the Pick Take (c1) to the pin, reason pin
	res, err := do(records.SlotOp{Op: "pin", SlotID: s4, TakeID: c2})
	if err != nil {
		t.Fatal(err)
	}
	ch := res.Changes[0]
	if ch.Kind != "slot_pinned" || *ch.FromTakeID != c1 || *ch.ToTakeID != c2 || *ch.Reason != "pin" || *ch.Position != 1 {
		t.Errorf("row = %+v", ch)
	}
	sl := r.slots(rec.ID)[0]
	if sl.PinnedTakeID == nil || *sl.PinnedTakeID != c2 || sl.PinnedBy == nil || *sl.PinnedBy != agent.Key() || sl.PinnedAt == nil {
		t.Errorf("slot = %+v", sl)
	}
	// the same pin again: no row, no bump
	before := rev().Revision
	res, err = do(records.SlotOp{Op: "pin", SlotID: s4, TakeID: c2})
	if err != nil || len(res.Changes) != 0 || rev().Revision != before {
		t.Errorf("repeated pin: %+v %v", res, err)
	}
	// another Song's Take; a Take that is no Take
	_, err = do(records.SlotOp{Op: "pin", SlotID: s4, TakeID: r.fx.Takes["a1"]})
	wantProblem(t, err, 422, "TAKE_NOT_IN_SONG")
	_, err = do(records.SlotOp{Op: "pin", SlotID: s4, TakeID: "no-such-take"})
	wantProblem(t, err, 422, "TAKE_NOT_IN_SONG")
	// a discarded Take
	r.exec(`UPDATE tracks SET status = 'discarded' WHERE id = ?`, r.fx.Takes["b2"])
	_, err = do(records.SlotOp{Op: "pin", SlotID: s3, TakeID: r.fx.Takes["b2"]})
	pe := wantProblem(t, err, 422, "TAKE_NOT_ELIGIBLE")
	same(t, "status detail", pe.Details["status"], "discarded")
	same(t, "op detail", pe.Details["op"], 0)
	// re-pin to the other Take: from is the old pin
	res, err = do(records.SlotOp{Op: "pin", SlotID: s4, TakeID: c1})
	if err != nil || *res.Changes[0].FromTakeID != c2 || *res.Changes[0].ToTakeID != c1 {
		t.Fatalf("re-pin: %+v %v", res, err)
	}
	// a new pick event on the Song moves the cursor the unpin must catch up with
	r.exec(`INSERT INTO pick_events(id,song_id,seq,at,from_take_id,to_take_id,from_source,to_source,reason,actor,actor_kind,actor_name,evidence_json)
		VALUES('01900000-0000-7000-8000-00000000e999',?,4,'2026-10-06T11:00:00.000Z',?,?,'stars','newest','newest','system:provenance','system','provenance','{}')`,
		r.song("S4"), c1, c2)
	r.exec(`UPDATE song_picks SET take_id = ?, source = 'newest', last_event_id = '01900000-0000-7000-8000-00000000e999' WHERE song_id = ?`, c2, r.song("S4"))
	// unpin: a row to the Pick Take, the cursor at the Song's max seq
	res, err = do(records.SlotOp{Op: "unpin", SlotID: s4})
	if err != nil {
		t.Fatal(err)
	}
	ch = res.Changes[0]
	if ch.Kind != "slot_unpinned" || *ch.FromTakeID != c1 || *ch.ToTakeID != c2 || *ch.Reason != "unpin" {
		t.Errorf("unpin row = %+v", ch)
	}
	sl = r.slots(rec.ID)[0]
	if sl.PinnedTakeID != nil || sl.PinnedBy != nil || sl.PinnedAt != nil || sl.SyncedSeq != 4 {
		t.Errorf("slot after unpin = %+v", sl)
	}
	// unpin of a floating slot is a no-op
	before = rev().Revision
	if res, err = do(records.SlotOp{Op: "unpin", SlotID: s4}); err != nil || len(res.Changes) != 0 || rev().Revision != before {
		t.Errorf("repeated unpin: %+v %v", res, err)
	}
}

func TestEditSlots_PinAll_UnpinAll(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.newRecord(agent, "S1", "S7", "S2")
	ids := r.slotIDs(rec.ID)
	res, err := edit(t, r, agent, rec, records.SlotOp{Op: "pin_all"})
	if err != nil {
		t.Fatal(err)
	}
	same(t, "kinds", kinds(res.Changes), []string{"slot_pinned", "slot_pinned"})
	same(t, "warnings (the empty slot is skipped)", res.Warnings, []string{"pin_all_skipped:" + ids[1]})
	sl := r.slots(rec.ID)
	if sl[0].PinnedTakeID == nil || *sl[0].PinnedTakeID != r.fx.Takes["s1a"] || sl[1].PinnedTakeID != nil || *sl[2].PinnedTakeID != r.fx.Takes["a1"] {
		t.Errorf("slots = %+v", sl)
	}
	if *res.Changes[0].FromTakeID != *res.Changes[0].ToTakeID {
		t.Errorf("pin_all of a floating slot: from %v to %v", *res.Changes[0].FromTakeID, *res.Changes[0].ToTakeID)
	}
	// a second pin_all pins nothing more but still reports the empty slot
	res2, err := edit(t, r, agent, res.Record, records.SlotOp{Op: "pin_all"})
	if err != nil || len(res2.Changes) != 0 || len(res2.Warnings) != 1 {
		t.Errorf("second pin_all: %+v %v", res2, err)
	}
	res3, err := edit(t, r, agent, res.Record, records.SlotOp{Op: "unpin_all"})
	if err != nil {
		t.Fatal(err)
	}
	same(t, "kinds", kinds(res3.Changes), []string{"slot_unpinned", "slot_unpinned"})
	for _, s := range r.slots(rec.ID) {
		if s.PinnedTakeID != nil {
			t.Errorf("slot %s still pinned", s.ID)
		}
	}
}

func TestRecords_OrderTrigger(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.newRecord(agent, "S1", "S2", "S3")
	ids := r.slotIDs(rec.ID)
	// the service never trips it, through any mix of operations
	mustEdit(t, r, agent, rec, records.SlotOp{Op: "add", SongID: r.song("S4"), Position: 2}, records.SlotOp{Op: "remove", SlotID: ids[0]},
		records.SlotOp{Op: "move", SlotID: ids[2], Position: 1})
	order := r.slotIDs(rec.ID)
	if len(order) != 3 || order[0] != ids[2] {
		t.Fatalf("order = %v", order)
	}
	// a hand-written bad order does
	for name, bad := range map[string]string{
		"empty":        `[]`,
		"unknown":      `["x","y","z"]`,
		"duplicate":    `["` + order[0] + `","` + order[0] + `","` + order[1] + `"]`,
		"removed slot": `["` + order[0] + `","` + order[1] + `","` + ids[0] + `"]`,
		"short":        `["` + order[0] + `","` + order[1] + `"]`,
	} {
		_, err := r.st.W.Exec(`UPDATE records SET order_json = ? WHERE id = ?`, bad, rec.ID)
		if err == nil || !strings.Contains(err.Error(), "order must list exactly the live slots") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	same(t, "order untouched", r.slotIDs(rec.ID), order)
}
