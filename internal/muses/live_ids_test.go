package muses_test

import (
	"context"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/muses"
)

// TestMuseLiveIDs: LiveIDs returns the ids of the non-hidden Muses in id order, no text, and a hidden Muse drops out and
// returns when it is unhidden (C133, C139.1).
func TestMuseLiveIDs(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := context.Background()
	if ids, err := w.svc.LiveIDs(ctx); err != nil || ids == nil || len(ids) != 0 {
		t.Fatalf("empty library: %v %v, want an empty non-nil slice", ids, err)
	}
	a := w.create(muses.KindReference, "Synthetic Muse 200", ptr("Synthetic Artist 200"), nil)
	b := w.create(muses.KindBrief, "Synthetic Brief 200", nil, ptr("a synthetic idea"))
	c := w.create(muses.KindReference, "Synthetic Muse 201", nil, nil)
	want := []string{a.ID, b.ID, c.ID}
	slices.Sort(want)
	if ids, err := w.svc.LiveIDs(ctx); err != nil || !slices.Equal(ids, want) {
		t.Fatalf("LiveIDs = %v %v, want %v", ids, err, want)
	}
	hide := true
	if _, err := w.svc.Update(ctx, human, b.ID, muses.Patch{Revision: b.Revision, Hidden: &hide}); err != nil {
		t.Fatal(err)
	}
	want = slices.DeleteFunc(want, func(id string) bool { return id == b.ID })
	if ids, err := w.svc.LiveIDs(ctx); err != nil || !slices.Equal(ids, want) {
		t.Fatalf("after hiding: %v %v, want %v", ids, err, want)
	}
}
