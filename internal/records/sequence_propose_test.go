package records_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/records"
)

// TestProposeRecord_SequencedRow: ProposeWith sequences and proposes in one transaction: one revision bump, the proposed row's
// detail names the run, and the slot_moved rows come first.
func TestProposeRecord_SequencedRow(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.tRecord(agent)
	out, err := r.svc.ProposeWith(context.Background(), agent, rec.ID, rec.Revision, "ready to hear", &records.SeqSpec{Strategy: "ramp_up"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != records.StatusProposed || out.Revision != rec.Revision+1 {
		t.Errorf("record = %s rev %d", out.Status, out.Revision)
	}
	seqs := r.sequences(rec.ID)
	if len(seqs) != 1 || seqs[0].Strategy != "ramp_up" || seqs[0].Revision != rec.Revision {
		t.Fatalf("runs = %+v", seqs)
	}
	changes := r.changes(rec.ID)
	last := changes[len(changes)-1]
	if last.Kind != "proposed" {
		t.Fatalf("last row = %s", last.Kind)
	}
	var d map[string]string
	if err := json.Unmarshal(last.Detail, &d); err != nil || d["sequence_id"] != seqs[0].ID || d["strategy"] != "ramp_up" {
		t.Errorf("detail = %s", last.Detail)
	}
	moved := 0
	for _, c := range changes {
		if c.Kind == "slot_moved" {
			moved++
		}
	}
	if moved == 0 {
		t.Error("the shuffled Record needed moves")
	}
	var after []string
	_ = json.Unmarshal([]byte(seqs[0].After), &after)
	same(t, "stored order is the live order", after, r.slotIDs(rec.ID))
}

// TestProposeRecord_SequenceAtomic: a failing sequence step writes nothing and leaves the Record a draft.
func TestProposeRecord_SequenceAtomic(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.newRecord(agent, "S1", "S2", "S3") // no features anywhere
	order, rows := r.slotIDs(rec.ID), len(r.changes(rec.ID))
	_, err := r.svc.ProposeWith(context.Background(), agent, rec.ID, rec.Revision, "ready", &records.SeqSpec{})
	wantProblem(t, err, 422, "SEQUENCE_NO_FEATURES")
	got := r.get(rec.ID)
	if got.Status != records.StatusDraft || got.Revision != rec.Revision || got.ProposalNote != nil {
		t.Errorf("record = %+v", got)
	}
	same(t, "order", r.slotIDs(rec.ID), order)
	if len(r.changes(rec.ID)) != rows || len(r.sequences(rec.ID)) != 0 {
		t.Error("a failed sequence step wrote rows")
	}
	// the other checks keep their order: stale revision and state come before the sequence step
	_, err = r.svc.ProposeWith(context.Background(), agent, rec.ID, rec.Revision+5, "ready", &records.SeqSpec{})
	wantProblem(t, err, 409, "STALE_REVISION")
	r2 := newRig(t)
	rec2 := r2.tRecord(agent)
	if _, err := r2.svc.Propose(context.Background(), agent, rec2.ID, rec2.Revision, "ready"); err != nil {
		t.Fatal(err)
	}
	cur := r2.get(rec2.ID)
	_, err = r2.svc.ProposeWith(context.Background(), agent, rec2.ID, cur.Revision, "again", &records.SeqSpec{})
	wantProblem(t, err, 409, "INVALID_RECORD_STATE")
}

// TestProposeRecord_L2Unchanged: without a sequence ProposeWith is Propose: same rows, no run, a plain proposed row.
func TestProposeRecord_L2Unchanged(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rec := r.tRecord(agent)
	order := r.slotIDs(rec.ID)
	out, err := r.svc.ProposeWith(context.Background(), agent, rec.ID, rec.Revision, "ready", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != records.StatusProposed || out.Revision != rec.Revision+1 {
		t.Errorf("record = %s rev %d", out.Status, out.Revision)
	}
	same(t, "order untouched", r.slotIDs(rec.ID), order)
	changes := r.changes(rec.ID)
	if last := changes[len(changes)-1]; last.Kind != "proposed" || len(last.Detail) != 0 {
		t.Errorf("last row = %s %s", last.Kind, last.Detail)
	}
	if len(r.sequences(rec.ID)) != 0 {
		t.Error("a plain proposal stored a run")
	}
	// a one-slot Record cannot be sequenced, so proposing it with a sequence is refused; the plain proposal is allowed
	one := r.newRecord(agent, "S7")
	_, err = r.svc.ProposeWith(context.Background(), agent, one.ID, one.Revision, "x", &records.SeqSpec{})
	wantProblem(t, err, 422, "SEQUENCE_TOO_SMALL")
	empty, err := r.svc.Create(context.Background(), agent, records.NewRecord{Title: "empty"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.svc.ProposeWith(context.Background(), agent, empty.ID, empty.Revision, "x", &records.SeqSpec{})
	wantProblem(t, err, 422, "RECORD_EMPTY")
}
