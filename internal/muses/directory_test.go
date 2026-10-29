package muses_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/muses"
)

// TestMuses_Directory: Ordinals carries the frozen ordinal and the hidden flag of every Muse (hidden ones keep their number),
// Version moves on create, update and hide, and Labels formats title and title · artist and omits unknown ids.
func TestMuses_Directory(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := context.Background()
	v0, err := w.svc.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ref := w.create(muses.KindReference, "Synthetic Muse 700", ptr("Synthetic Artist 700"), nil)
	noArtist := w.create(muses.KindReference, "Synthetic Muse 701", nil, nil)
	brief := w.create(muses.KindBrief, "Synthetic Brief 702", nil, ptr("synthetic brief text"))
	v1, _ := w.svc.Version(ctx)
	if v1 == v0 {
		t.Error("Version did not move on create")
	}

	got, err := w.svc.Ordinals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]muses.MuseMeta{ref.ID: {Ordinal: 1}, noArtist.ID: {Ordinal: 2}, brief.ID: {Ordinal: 3}}
	if d := cmp.Diff(want, got); d != "" {
		t.Errorf("Ordinals (-want +got):\n%s", d)
	}

	hidden := true
	if _, err := w.svc.Update(ctx, human, noArtist.ID, muses.Patch{Revision: noArtist.Revision, Hidden: &hidden}); err != nil {
		t.Fatal(err)
	}
	v2, _ := w.svc.Version(ctx)
	if v2 == v1 {
		t.Error("Version did not move on hide")
	}
	if got, _ = w.svc.Ordinals(ctx); got[noArtist.ID] != (muses.MuseMeta{Ordinal: 2, Hidden: true}) || got[brief.ID].Ordinal != 3 {
		t.Errorf("hidden Muse keeps its number: %+v", got)
	}
	title := "Synthetic Muse 700 renamed"
	if _, err := w.svc.Update(ctx, human, ref.ID, muses.Patch{Revision: ref.Revision, Title: &title}); err != nil {
		t.Fatal(err)
	}
	if v3, _ := w.svc.Version(ctx); v3 == v2 {
		t.Error("Version did not move on update")
	}

	labels, err := w.svc.Labels(ctx, []string{ref.ID, noArtist.ID, brief.ID, "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	wantLabels := map[string]string{ref.ID: "Synthetic Muse 700 renamed · Synthetic Artist 700", noArtist.ID: "Synthetic Muse 701", brief.ID: "Synthetic Brief 702"}
	if d := cmp.Diff(wantLabels, labels); d != "" {
		t.Errorf("Labels (-want +got):\n%s", d)
	}
	if l, err := w.svc.Labels(ctx, nil); err != nil || len(l) != 0 {
		t.Errorf("Labels(nil) = %v, %v", l, err)
	}
}

// TestMuses_DirectoryErrors: a closed store surfaces as an error from all three calls.
func TestMuses_DirectoryErrors(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := context.Background()
	if err := w.st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Ordinals(ctx); err == nil {
		t.Error("Ordinals on a closed store")
	}
	if _, err := w.svc.Version(ctx); err == nil {
		t.Error("Version on a closed store")
	}
	if _, err := w.svc.Labels(ctx, []string{"x"}); err == nil {
		t.Error("Labels on a closed store")
	}
}
