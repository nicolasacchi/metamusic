package jobs

import (
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

var bobEditorP = auth.Principal{Kind: auth.KindHuman, Role: auth.RoleEditor, ID: "bob", Name: "bob", User: "bob", Scopes: auth.ScopesFor(auth.RoleEditor)}

// TestCancel_EditorOwnOnly (C305, C310.3): an Editor cancels or withdraws only batches and jobs it created; the Owner cancels
// any, and an Editor's awaiting batch is withdrawn with its reservation released.
func TestCancel_EditorOwnOnly(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	svc := r.service(t.Context())
	other := r.seedBatch(batchSpec{principal: "agent:tok1", jobs: 1})
	if _, err := svc.CancelJob(t.Context(), bobEditorP, other.Jobs[0], ""); !errors.Is(err, ErrNotBatchOwner) {
		t.Errorf("job of another: %v", err)
	}
	if _, err := svc.CancelBatch(t.Context(), bobEditorP, other.Batch, ""); !errors.Is(err, ErrNotBatchOwner) {
		t.Errorf("batch of another: %v", err)
	}
	ownerBatch := r.seedBatch(batchSpec{principal: humanP.Key(), jobs: 1})
	if _, err := svc.CancelBatch(t.Context(), bobEditorP, ownerBatch.Batch, ""); !errors.Is(err, ErrNotBatchOwner) {
		t.Errorf("the Owner's batch: %v", err)
	}
	if r.count(`SELECT COUNT(*) FROM jobs WHERE cancel_requested_at IS NOT NULL`) != 0 {
		t.Error("a refused cancel wrote an intent")
	}
	mine := r.seedBatch(batchSpec{principal: bobEditorP.Key(), jobs: 2})
	if _, err := svc.CancelJob(t.Context(), bobEditorP, mine.Jobs[0], "oops"); err != nil {
		t.Errorf("own job: %v", err)
	}
	if _, err := svc.CancelBatch(t.Context(), bobEditorP, mine.Batch, "oops"); err != nil {
		t.Errorf("own batch: %v", err)
	}
	if _, err := svc.CancelBatch(t.Context(), humanP, mine.Batch, ""); err != nil {
		t.Errorf("the Owner cancels an Editor's batch: %v", err)
	}
}

// TestBatchOwners: the owner lookup the API uses to decide which batches an Editor sees with their cost.
func TestBatchOwners(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	svc := r.service(t.Context())
	a := r.seedBatch(batchSpec{principal: "agent:tok1", jobs: 1})
	b := r.seedBatch(batchSpec{principal: bobEditorP.Key(), jobs: 1})
	got, err := svc.BatchOwners(t.Context(), []string{a.Batch, b.Batch, "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[a.Batch] != "agent:tok1" || got[b.Batch] != bobEditorP.Key() {
		t.Errorf("owners = %v", got)
	}
	if got, err := svc.BatchOwners(t.Context(), nil); err != nil || len(got) != 0 {
		t.Errorf("empty: %v %v", got, err)
	}
}
