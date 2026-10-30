package records_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/records"
)

func TestCov_SlotsAndPending(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	rec := r.newRecord(human, "S1", "S2", "S3")
	if got := r.slots(rec.ID); len(got) != 3 || got[0].SongID != r.song("S1") || got[1].SongID != r.song("S2") {
		t.Errorf("slots = %+v", got)
	}
	if _, err := r.svc.Slots(ctx, "no-such-record"); err == nil {
		t.Error("Slots of an unknown Record succeeded")
	} else {
		wantProblem(t, err, 404, "NOT_FOUND")
	}
	if _, err := r.svc.Resolved(ctx, "no-such-record"); err == nil {
		t.Error("Resolved of an unknown Record succeeded")
	}
	// a Record without slots exists and has no slots, which is not an error
	empty, err := r.svc.Create(ctx, human, newEmpty())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r.svc.Slots(ctx, empty.ID); err != nil || len(got) != 0 {
		t.Errorf("empty Slots = %v %v", got, err)
	}
	y := r.syncer()
	before, err := y.Pending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r.setPick(r.song("S3"), r.take("b2"), "ladder", "ladder", "")
	after, err := y.Pending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after <= before {
		t.Errorf("pending %d -> %d, want an increase", before, after)
	}
	r.settle()
	if n, err := y.Pending(ctx); err != nil || n != 0 {
		t.Errorf("pending after Settle = %d %v", n, err)
	}
}

func newEmpty() records.NewRecord { return records.NewRecord{Title: "Empty Synthetic Record"} }
