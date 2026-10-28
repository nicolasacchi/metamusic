package records_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/records"
)

const humanHint = "published records are edited by a human"

func TestRecords_PrincipalRules(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	r1 := r.get(r.fx.Records["R1"]) // published, revision 3
	system := auth.System("reconciler")
	touch := func(p auth.Principal, rec records.Record) error {
		_, err := r.svc.Update(ctx, p, rec.ID, records.Patch{Revision: rec.Revision, Subtitle: sp("by " + p.Name)})
		return err
	}
	slotEdit := func(p auth.Principal, rec records.Record) error {
		_, err := r.svc.EditSlots(ctx, p, rec.ID, rec.Revision, []records.SlotOp{{Op: "set_side", SlotID: r.fx.Slots["A"], Side: records.OptString{Set: true, Value: "C"}}})
		return err
	}
	rows := len(r.changes(r1.ID))
	for _, p := range []auth.Principal{agent, system} {
		for name, f := range map[string]func(auth.Principal, records.Record) error{"Update": touch, "EditSlots": slotEdit} {
			err := f(p, r1)
			pe := wantProblem(t, err, 403, "HUMAN_ONLY")
			if pe.Details["hint"] != humanHint {
				t.Errorf("%s by %s: hint = %v", name, p.Kind, pe.Details["hint"])
			}
		}
	}
	if n := len(r.changes(r1.ID)); n != rows {
		t.Errorf("refused edits changed the changelog: %d rows, want %d", n, rows)
	}
	// a device (a paired phone) and a human may edit a published Record
	if err := touch(device, r1); err != nil {
		t.Errorf("device Update of a published record: %v", err)
	}
	r1 = r.get(r1.ID)
	if err := slotEdit(human, r1); err != nil {
		t.Errorf("human EditSlots of a published record: %v", err)
	}
	cs := r.changes(r1.ID)
	if last := cs[len(cs)-1]; last.Kind != "slot_side" || last.ActorKind != "human" || last.Actor != human.Key() {
		t.Errorf("last row = %+v", last)
	}
	// draft: any principal edits
	d := r.get(r.fx.Records["R2"])
	for _, p := range []auth.Principal{agent, device, human} {
		d = r.get(d.ID)
		if err := touch(p, d); err != nil {
			t.Errorf("%s Update of a draft: %v", p.Kind, err)
		}
	}
	// Publish: KindHuman only, before any read (a missing record still answers 403)
	for _, p := range []auth.Principal{agent, device, system, {}} {
		for _, id := range []string{d.ID, "no-such-record"} {
			_, err := r.svc.Publish(ctx, p, id, 1, true)
			wantProblem(t, err, 403, "HUMAN_ONLY")
			_, err = r.svc.Publish(ctx, p, id, 1, false)
			wantProblem(t, err, 403, "HUMAN_ONLY")
		}
	}
	// Propose: agents, devices and humans
	for i, p := range []auth.Principal{agent, device, human} {
		rec := r.newRecord(agent, "S"+string(rune('1'+i)))
		if _, err := r.svc.Propose(ctx, p, rec.ID, rec.Revision, "Synthetic rationale"); err != nil {
			t.Errorf("%s Propose: %v", p.Kind, err)
		}
	}
}

func TestRecords_StatusPath(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	rec := r.newRecord(agent, "S1", "S2")
	// draft -> proposed
	rec, err := r.svc.Propose(ctx, agent, rec.ID, rec.Revision, "  Synthetic   rationale ")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != records.StatusProposed || rec.ProposalNote == nil || *rec.ProposalNote != "Synthetic rationale" || rec.ProposedBy == nil || *rec.ProposedBy != agent.Key() || rec.ProposedAt == nil || rec.Revision != 2 {
		t.Errorf("proposed = %+v", rec)
	}
	// propose again, or a published record: 409
	_, err = r.svc.Propose(ctx, agent, rec.ID, rec.Revision, "again")
	pe := wantProblem(t, err, 409, "INVALID_RECORD_STATE")
	same(t, "status detail", pe.Details["status"], "proposed")
	// proposed -> published -> draft
	rec, err = r.svc.Publish(ctx, human, rec.ID, rec.Revision, true)
	if err != nil || rec.Status != records.StatusPublished || rec.PublishedBy == nil || *rec.PublishedBy != human.Key() || rec.FirstPublishedAt == nil {
		t.Fatalf("publish: %+v %v", rec, err)
	}
	_, err = r.svc.Propose(ctx, agent, rec.ID, rec.Revision, "too late")
	wantProblem(t, err, 409, "INVALID_RECORD_STATE")
	rec, err = r.svc.Publish(ctx, human, rec.ID, rec.Revision, false)
	if err != nil || rec.Status != records.StatusDraft || rec.PublishedAt != nil || rec.ProposedAt != nil || rec.ProposalNote != nil {
		t.Fatalf("unpublish: %+v %v", rec, err)
	}
	same(t, "kinds", kinds(r.changes(rec.ID))[3:], []string{"proposed", "published", "unpublished"})
	// published -> proposed is refused by the trigger (nothing in the service asks for it)
	rec, err = r.svc.Publish(ctx, human, rec.ID, rec.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.st.W.Exec(`UPDATE records SET status = 'proposed', proposed_by = 'x', proposed_at = 'y', published_by = NULL, published_at = NULL WHERE id = ?`, rec.ID)
	if err == nil || !strings.Contains(err.Error(), "illegal status transition") {
		t.Errorf("published -> proposed: %v", err)
	}
	// Propose with no slot, a bad rationale
	empty, err := r.svc.Create(ctx, agent, records.NewRecord{Title: "Empty"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.svc.Propose(ctx, agent, empty.ID, 1, "nothing here")
	wantProblem(t, err, 422, "RECORD_EMPTY")
	full := r.newRecord(agent, "S3")
	for _, bad := range []string{"", "   ", strings.Repeat("x", 501), "a\x00b"} {
		_, err = r.svc.Propose(ctx, agent, full.ID, 1, bad)
		wantProblem(t, err, 422, "VALIDATION_FAILED")
	}
	_, err = r.svc.Propose(ctx, agent, full.ID, 9, "ok")
	wantProblem(t, err, 409, "STALE_REVISION")
	_, err = r.svc.Propose(ctx, agent, "nope", 1, "ok")
	wantProblem(t, err, 404, "NOT_FOUND")
}

func TestRecords_PublishRules(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	// no playable slot: an empty slot (S7 has no Pick), a pinned discarded Take, no slot at all
	rec := r.newRecord(agent, "S7")
	_, err := r.svc.Publish(ctx, human, rec.ID, rec.Revision, true)
	wantProblem(t, err, 422, "RECORD_EMPTY")
	rec = mustEdit(t, r, agent, rec, records.SlotOp{Op: "add", SongID: r.song("S3"), TakeID: r.fx.Takes["b2"], Position: records.PositionEnd}).Record
	r.exec(`UPDATE tracks SET status = 'discarded' WHERE id = ?`, r.fx.Takes["b2"])
	_, err = r.svc.Publish(ctx, human, rec.ID, rec.Revision, true)
	wantProblem(t, err, 422, "RECORD_EMPTY")
	// a playable slot makes it publishable
	rec = mustEdit(t, r, agent, rec, records.SlotOp{Op: "add", SongID: r.song("S1"), Position: records.PositionEnd}).Record
	pub, err := r.svc.Publish(ctx, human, rec.ID, rec.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	first := *pub.FirstPublishedAt
	if pub.Status != records.StatusPublished || *pub.PublishedBy != human.Key() || *pub.PublishedAt != first || pub.Revision != rec.Revision+1 {
		t.Errorf("published = %+v", pub)
	}
	cs := r.changes(rec.ID)
	if last := cs[len(cs)-1]; last.Kind != "published" || last.ActorKind != "human" || last.SlotID != nil {
		t.Errorf("last row = %+v", last)
	}
	// published again at the current revision: 200 unchanged; at a stale one: 409
	again, err := r.svc.Publish(ctx, human, rec.ID, pub.Revision, true)
	if err != nil || again.Revision != pub.Revision || len(r.changes(rec.ID)) != len(cs) {
		t.Errorf("publish twice: %+v %v", again, err)
	}
	_, err = r.svc.Publish(ctx, human, rec.ID, rec.Revision, true)
	pe := wantProblem(t, err, 409, "STALE_REVISION")
	same(t, "details", pe.Details, map[string]any{"revision": pub.Revision, "status": "published"})
	// unpublish: published_* and proposed_* cleared, first_published_at kept, row unpublished
	un, err := r.svc.Publish(ctx, human, rec.ID, pub.Revision, false)
	if err != nil || un.Status != records.StatusDraft || un.PublishedAt != nil || un.PublishedBy != nil || un.FirstPublishedAt == nil || *un.FirstPublishedAt != first {
		t.Fatalf("unpublish: %+v %v", un, err)
	}
	cs = r.changes(rec.ID)
	if cs[len(cs)-1].Kind != "unpublished" {
		t.Errorf("rows = %v", kinds(cs))
	}
	// publish again: first_published_at is set once
	pub2, err := r.svc.Publish(ctx, human, rec.ID, un.Revision, true)
	if err != nil || *pub2.FirstPublishedAt != first || *pub2.PublishedAt == first {
		t.Errorf("republish: %+v %v", pub2, err)
	}
	// proposed -> draft through Publish(false): proposal_dismissed, proposal fields cleared
	p := r.newRecord(agent, "S2")
	p, err = r.svc.Propose(ctx, agent, p.ID, p.Revision, "please")
	if err != nil {
		t.Fatal(err)
	}
	dr, err := r.svc.Publish(ctx, human, p.ID, p.Revision, false)
	if err != nil || dr.Status != records.StatusDraft || dr.ProposedBy != nil || dr.ProposedAt != nil || dr.ProposalNote != nil || dr.Revision != p.Revision+1 {
		t.Fatalf("decline: %+v %v", dr, err)
	}
	cs = r.changes(p.ID)
	if last := cs[len(cs)-1]; last.Kind != "proposal_dismissed" || string(last.Detail) != `{"reason":"declined"}` {
		t.Errorf("last row = %+v", last)
	}
}

func TestRecords_PublishCheckOrder(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	// (1) principal before the read
	_, err := r.svc.Publish(ctx, agent, "no-such-record", 1, true)
	wantProblem(t, err, 403, "HUMAN_ONLY")
	// (2) existence
	_, err = r.svc.Publish(ctx, human, "no-such-record", 1, true)
	wantProblem(t, err, 404, "NOT_FOUND")
	// (3) the revision, even on a published Record and before the content rule
	rec := r.newRecord(agent, "S1")
	rec, err = r.svc.Publish(ctx, human, rec.ID, rec.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.svc.Publish(ctx, human, rec.ID, rec.Revision-1, true)
	wantProblem(t, err, 409, "STALE_REVISION")
	// (4) the no-op rule beats (5) the content rule: a published Record whose slots are all gone is "already published"
	rec = mustEdit(t, r, human, rec, records.SlotOp{Op: "remove", SlotID: r.slotIDs(rec.ID)[0]}).Record
	got, err := r.svc.Publish(ctx, human, rec.ID, rec.Revision, true)
	if err != nil || got.Status != records.StatusPublished {
		t.Errorf("already published with no slot: %+v %v", got, err)
	}
	// (5) content
	empty, err := r.svc.Create(ctx, human, records.NewRecord{Title: "Empty"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.svc.Publish(ctx, human, empty.ID, 1, true)
	wantProblem(t, err, 422, "RECORD_EMPTY")
}

func TestRecords_PublishFalseFromDraft(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.newRecord(agent, "S1")
	rows := len(r.changes(rec.ID))
	got, err := r.svc.Publish(context.Background(), human, rec.ID, rec.Revision, false)
	if err != nil || got.Revision != rec.Revision || got.UpdatedAt != rec.UpdatedAt || got.Status != records.StatusDraft || len(r.changes(rec.ID)) != rows {
		t.Errorf("publish(false) of a draft: %+v %v", got, err)
	}
}

func TestRecords_ProposedEdit(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	propose := func() records.Record {
		rec := r.newRecord(agent, "S1", "S2")
		rec, err := r.svc.Propose(ctx, agent, rec.ID, rec.Revision, "Synthetic rationale")
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}
	dismissed := func(rec records.Record) {
		t.Helper()
		got := r.get(rec.ID)
		if got.Status != records.StatusDraft || got.ProposedBy != nil || got.ProposedAt != nil || got.ProposalNote != nil {
			t.Errorf("record after an agent edit = %+v", got)
		}
		cs := r.changes(rec.ID)
		last := cs[len(cs)-1]
		if last.Kind != "proposal_dismissed" || string(last.Detail) != `{"reason":"edited"}` || last.Actor != agent.Key() {
			t.Errorf("last row = %+v", last)
		}
		if got.Revision != rec.Revision+1 {
			t.Errorf("revision %d, want one bump from %d", got.Revision, rec.Revision)
		}
	}
	// an agent's Update
	rec := propose()
	if _, err := r.svc.Update(ctx, agent, rec.ID, records.Patch{Revision: rec.Revision, Title: sp("Changed by an agent")}); err != nil {
		t.Fatal(err)
	}
	dismissed(rec)
	same(t, "rows", kinds(r.changes(rec.ID))[3:], []string{"proposed", "renamed", "proposal_dismissed"})
	// an agent's EditSlots (the dismissal row is returned with the edit rows)
	rec = propose()
	res := mustEdit(t, r, agent, rec, records.SlotOp{Op: "remove", SlotID: r.slotIDs(rec.ID)[1]})
	same(t, "returned rows", kinds(res.Changes), []string{"slot_removed", "proposal_dismissed"})
	dismissed(rec)
	// a no-op edit does not dismiss
	rec = propose()
	if _, err := r.svc.Update(ctx, agent, rec.ID, records.Patch{Revision: rec.Revision, Title: sp(rec.Title)}); err != nil {
		t.Fatal(err)
	}
	if got := r.get(rec.ID); got.Status != records.StatusProposed {
		t.Errorf("a no-op edit dismissed the proposal: %+v", got)
	}
	// a human's edit (and a paired device's) keeps the proposal
	for _, p := range []auth.Principal{human, device} {
		rec = propose()
		if _, err := r.svc.Update(ctx, p, rec.ID, records.Patch{Revision: rec.Revision, Title: sp("Changed by " + p.Name)}); err != nil {
			t.Fatal(err)
		}
		got := r.get(rec.ID)
		if got.Status != records.StatusProposed || got.ProposalNote == nil || *got.ProposalNote != "Synthetic rationale" {
			t.Errorf("%s edit: %+v", p.Kind, got)
		}
		res := mustEdit(t, r, p, got, records.SlotOp{Op: "set_side", SlotID: r.slotIDs(rec.ID)[0], Side: records.OptString{Set: true, Value: "A"}})
		if res.Record.Status != records.StatusProposed {
			t.Errorf("%s slot edit: %+v", p.Kind, res.Record)
		}
	}
}

// TestEditSlots_ChangesGolden pins the changelog rows a scripted session writes: kinds, positions, reasons, detail shapes
// and the text ChangeText renders for them.
func TestEditSlots_ChangesGolden(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.newRecord(agent, "S1", "S3", "S4")
	ids := r.slotIDs(rec.ID)
	rec = mustEdit(t, r, agent, rec,
		records.SlotOp{Op: "add", SongID: r.song("S2"), Position: 1}, records.SlotOp{Op: "move", SlotID: ids[2], Position: 1},
		records.SlotOp{Op: "set_side", SlotID: ids[0], Side: records.OptString{Set: true, Value: "A"}},
		records.SlotOp{Op: "set_side", SlotID: ids[0], Side: records.OptString{Set: true, Null: true}},
		records.SlotOp{Op: "pin", SlotID: ids[1], TakeID: r.fx.Takes["b2"]}, records.SlotOp{Op: "remove", SlotID: ids[0]}).Record
	rec = mustEdit(t, r, agent, rec, records.SlotOp{Op: "unpin", SlotID: ids[1]}).Record
	names := map[string]string{}
	for label, id := range r.fx.Takes {
		names[id] = "Synthetic Take " + strings.ToUpper(label)
	}
	slotNo := map[string]string{}
	for i, id := range append([]string{}, ids...) {
		slotNo[id] = "slot" + string(rune('1'+i))
	}
	type row struct {
		Seq      int     `json:"seq"`
		Kind     string  `json:"kind"`
		Slot     string  `json:"slot,omitempty"`
		Position *int    `json:"position"`
		Reason   *string `json:"reason"`
		Detail   string  `json:"detail,omitempty"`
		Actor    string  `json:"actor_kind"`
		Text     string  `json:"text"`
	}
	var out []row
	for _, c := range r.changes(rec.ID) {
		slot := ""
		if c.SlotID != nil {
			slot = slotNo[*c.SlotID]
			if slot == "" {
				slot = "new"
			}
		}
		out = append(out, row{c.Seq, c.Kind, slot, c.Position, c.Reason, string(c.Detail), c.ActorKind, records.ChangeText(c, func(id string) string { return names[id] })})
	}
	golden(t, "edit_changes.golden.json", out)
}
