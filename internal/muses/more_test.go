package muses_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/muses"
)

func (w *world) create(kind muses.Kind, title string, artist, brief *string) muses.Muse {
	w.t.Helper()
	m, err := w.svc.Create(context.Background(), human, muses.NewMuse{Kind: kind, Title: title, Artist: artist, Brief: brief})
	if err != nil {
		w.t.Fatal(err)
	}
	return m
}

func TestMuses_RefAndRefs(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := context.Background()
	a := w.create(muses.KindReference, "Synthetic Muse 100", ptr("Synthetic Artist 100"), nil)
	b := w.create(muses.KindBrief, "Synthetic Brief 100", nil, ptr("a warm synthetic idea"))
	hidden := true
	if _, err := w.svc.Update(ctx, human, b.ID, muses.Patch{Revision: b.Revision, Hidden: &hidden}); err != nil {
		t.Fatal(err)
	}

	r, ok, err := w.svc.Ref(ctx, a.ID)
	if err != nil || !ok || r.Title != a.Title || r.Artist == nil || *r.Artist != "Synthetic Artist 100" || r.Kind != "reference" {
		t.Fatalf("Ref = %+v %v %v", r, ok, err)
	}
	if r, ok, err = w.svc.Ref(ctx, b.ID); err != nil || !ok || r.Artist != nil { // a hidden Muse still resolves
		t.Fatalf("Ref of the hidden brief = %+v %v %v", r, ok, err)
	}
	if _, ok, err = w.svc.Ref(ctx, "missing"); err != nil || ok {
		t.Fatalf("Ref of a missing Muse: %v %v", ok, err)
	}

	// 405 ids force a second chunk of 400; unknown ids are absent
	ids := []string{a.ID, b.ID}
	for i := range 403 {
		ids = append(ids, fmt.Sprintf("missing-%d", i))
	}
	got, err := w.svc.Refs(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[a.ID].Title != a.Title || got[b.ID].Artist != nil || got[a.ID].Artist == nil {
		t.Fatalf("Refs = %+v", got)
	}
	if got, err = w.svc.Refs(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("Refs(nil) = %v %v", got, err)
	}
}

func TestMuses_ListPagingAndClamp(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := context.Background()
	for i := range 5 {
		w.create(muses.KindReference, fmt.Sprintf("Synthetic Muse %03d", i), nil, nil)
	}
	brief := w.create(muses.KindBrief, "Synthetic Brief", nil, ptr("x"))
	hidden := true
	if _, err := w.svc.Update(ctx, human, brief.ID, muses.Patch{Revision: brief.Revision, Hidden: &hidden}); err != nil {
		t.Fatal(err)
	}
	// a limit over the cap is clamped (all fit); a small one pages through every live Muse exactly once
	if pg, err := w.svc.List(ctx, muses.ListFilter{Limit: 9999}); err != nil || len(pg.Items) != 5 || pg.NextCursor != nil {
		t.Fatalf("clamped list: %d items, cursor %v, %v", len(pg.Items), pg.NextCursor, err)
	}
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		pg, err := w.svc.List(ctx, muses.ListFilter{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range pg.Items {
			if seen[m.ID] {
				t.Fatalf("Muse %s on two pages", m.ID)
			}
			seen[m.ID] = true
		}
		if pg.NextCursor == nil {
			break
		}
		if pages > 5 {
			t.Fatal("paging does not end")
		}
		cursor = *pg.NextCursor
	}
	if len(seen) != 5 {
		t.Errorf("paged %d Muses, want 5", len(seen))
	}
	if pg, err := w.svc.List(ctx, muses.ListFilter{IncludeHidden: true, Kind: muses.KindBrief}); err != nil || len(pg.Items) != 1 {
		t.Errorf("hidden briefs: %d, %v", len(pg.Items), err)
	}
	if pg, err := w.svc.List(ctx, muses.ListFilter{Q: "Muse 003"}); err != nil || len(pg.Items) != 1 {
		t.Errorf("text search: %d, %v", len(pg.Items), err)
	}
	if _, err := w.svc.List(ctx, muses.ListFilter{Cursor: "!!"}); err == nil {
		t.Error("a malformed cursor was accepted")
	}
}

func TestMuses_VerifyFaults(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := context.Background()
	a := w.create(muses.KindReference, "Synthetic Muse 200", nil, nil)
	f, err := muses.VerifyMuses(ctx, w.st.R)
	if err != nil || len(f.KeyMismatch) != 0 || len(f.DanglingSongs) != 0 {
		t.Fatalf("clean library: %+v %v", f, err)
	}
	if _, err := w.st.W.Exec(`UPDATE muses SET key = 'zzz' WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	songs := w.songs(1)
	if _, err := w.st.W.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.st.W.Exec(`UPDATE songs SET muse_id = 'ghost', muse_source = 'manual' WHERE id = ?`, songs[0]); err != nil {
		t.Fatal(err)
	}
	f, err = muses.VerifyMuses(ctx, w.st.R)
	if err != nil || len(f.KeyMismatch) != 1 || f.KeyMismatch[0] != a.ID || len(f.DanglingSongs) != 1 || f.DanglingSongs[0] != songs[0] {
		t.Fatalf("faults = %+v %v", f, err)
	}
}

func TestMuses_TitleFoldAndAssignable(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := context.Background()
	if muses.TitleFold("Synthetic  MUSE") != muses.TitleFold("synthetic muse") {
		t.Error("TitleFold does not fold case and spacing")
	}
	live := w.create(muses.KindReference, "Synthetic Muse 300", nil, nil)
	gone := w.create(muses.KindReference, "Synthetic Muse 301", nil, nil)
	hidden := true
	if _, err := w.svc.Update(ctx, human, gone.ID, muses.Patch{Revision: gone.Revision, Hidden: &hidden}); err != nil {
		t.Fatal(err)
	}
	check := func(id string) error {
		tx, err := w.st.R.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		return muses.CheckAssignableTx(ctx, tx, id)
	}
	if err := check(live.ID); err != nil {
		t.Errorf("live Muse refused: %v", err)
	}
	if c, st := code(check("nope")); c != "UNKNOWN_MUSE" || st != 422 {
		t.Errorf("unknown Muse = %s %d", c, st)
	}
	if c, st := code(check(gone.ID)); c != "MUSE_HIDDEN" || st != 409 {
		t.Errorf("hidden Muse = %s %d", c, st)
	}
}

func TestMuses_EnsureReferenceRefusesLongArtist(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := context.Background()
	tx, err := w.st.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	long := strings.Repeat("x", 201)
	_, _, err = muses.EnsureReferenceTx(ctx, tx, librarytest.FixedNow, func() string { return idOf(1) }, "Synthetic Muse 400", &long, "system:test")
	if c, st := code(err); c != "VALIDATION_FAILED" || st != 422 {
		t.Fatalf("long artist = %v", err)
	}
}

func TestMuses_OrderCaseInsensitive(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ctx := context.Background()
	for _, title := range []string{"Zebra", "apple", "Mango"} {
		w.create(muses.KindReference, title, nil, nil)
	}
	var got []string
	cursor := ""
	for pages := 0; ; pages++ {
		pg, err := w.svc.List(ctx, muses.ListFilter{Limit: 1, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range pg.Items {
			got = append(got, m.Title)
		}
		if pg.NextCursor == nil {
			break
		}
		if pages > 5 {
			t.Fatal("paging does not end")
		}
		cursor = *pg.NextCursor
	}
	if strings.Join(got, ",") != "apple,Mango,Zebra" {
		t.Errorf("order = %v, want apple, Mango, Zebra with no skipped or duplicated rows", got)
	}
}
