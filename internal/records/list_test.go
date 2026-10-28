package records_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/records"
)

func TestRecords_List_Filters(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	r1, r2 := r.fx.Records["R1"], r.fx.Records["R2"]
	list := func(f records.ListFilter) records.Page {
		t.Helper()
		p, err := r.svc.List(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	ids := func(p records.Page) []string {
		out := []string{}
		for _, it := range p.Items {
			out = append(out, it.ID)
		}
		return out
	}
	// make four more Records, updated in a known order, one hidden, one with a wildcard title
	a := r.newRecord(agent, "S1")
	b := r.newRecord(human, "S2", "S3")
	c, err := r.svc.Create(ctx, agent, records.NewRecord{Title: "100%_Sure", SongIDs: nil})
	if err != nil {
		t.Fatal(err)
	}
	d := r.newRecord(agent, "S4")
	if _, err := r.svc.Update(ctx, agent, d.ID, records.Patch{Revision: 1, Hidden: bp(true)}); err != nil {
		t.Fatal(err)
	}

	same(t, "default order (updated_at desc, id desc), hidden excluded", ids(list(records.ListFilter{})), []string{c.ID, b.ID, a.ID, r2, r1})
	if p := list(records.ListFilter{}); p.Total != 5 {
		t.Errorf("total = %d", p.Total)
	}
	same(t, "with hidden", ids(list(records.ListFilter{IncludeHidden: true}))[0], d.ID)
	same(t, "status published", ids(list(records.ListFilter{Statuses: []records.Status{records.StatusPublished}})), []string{r1})
	same(t, "status draft+published count", list(records.ListFilter{Statuses: []records.Status{records.StatusDraft, records.StatusPublished}}).Total, 5)
	same(t, "q is a case-insensitive substring", ids(list(records.ListFilter{Q: "RECORD two"})), []string{r2})
	same(t, "q escapes wildcards", ids(list(records.ListFilter{Q: "0%_S"})), []string{c.ID})
	same(t, "q underscore is literal", len(ids(list(records.ListFilter{Q: "%"}))), 1)
	same(t, "song_id", ids(list(records.ListFilter{SongID: r.song("S1")})), []string{a.ID, r2})
	// a removed slot no longer matches
	if _, err := r.svc.EditSlots(ctx, agent, a.ID, 1, []records.SlotOp{{Op: "remove", SlotID: r.slotIDs(a.ID)[0]}}); err != nil {
		t.Fatal(err)
	}
	same(t, "song_id after removal", ids(list(records.ListFilter{SongID: r.song("S1")})), []string{r2})

	// keyset: pages of two, stable under a write between pages
	all := ids(list(records.ListFilter{}))
	p1 := list(records.ListFilter{Limit: 2})
	if p1.NextCursor == nil || p1.Total != len(all) {
		t.Fatalf("page 1 = %+v", p1)
	}
	// a refused write (r1 is published, the agent may not edit it) moves nothing
	_, err = r.svc.Update(ctx, agent, r1, records.Patch{Revision: 3, Title: sp("Renamed in between")})
	wantProblem(t, err, 403, "HUMAN_ONLY")
	if _, err := r.svc.Update(ctx, agent, a.ID, records.Patch{Revision: 1, Title: sp("zzz")}); err == nil {
		t.Fatal("update with a stale revision accepted")
	}
	p2 := list(records.ListFilter{Limit: 2, Cursor: *p1.NextCursor})
	p3 := list(records.ListFilter{Limit: 2, Cursor: *p2.NextCursor})
	if p3.NextCursor != nil {
		t.Errorf("the last page has a cursor: %v", *p3.NextCursor)
	}
	same(t, "pages concatenate", append(append(ids(p1), ids(p2)...), ids(p3)...), all)
	if _, err := r.svc.List(ctx, records.ListFilter{Cursor: "%%%"}); err == nil {
		t.Error("a malformed cursor was accepted")
	} else {
		wantProblem(t, err, 422, "VALIDATION_FAILED")
	}

	// summaries: counts, duration and warnings agree with Resolved
	for _, id := range []string{r1, r2, b.ID} {
		rs, err := r.svc.Resolved(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		var slots, play, warn int
		var dur float64
		rec := r.get(id)
		for _, x := range rs {
			slots++
			if x.State == records.StateOK {
				play++
				dur += x.Take.DurationS
			}
			for _, w := range x.Warnings {
				if w != records.WarnUndecidedPick || rec.Status == records.StatusPublished {
					warn++
				}
			}
		}
		p := list(records.ListFilter{Q: rec.Title, IncludeHidden: true})
		var got *records.Summary
		for i := range p.Items {
			if p.Items[i].ID == id {
				got = &p.Items[i]
			}
		}
		if got == nil || got.Slots != slots || got.Playable != play || got.Warnings != warn || got.DurationS != dur {
			t.Errorf("summary of %s = %+v, want slots %d playable %d warnings %d duration %v", id, got, slots, play, warn, dur)
		}
	}
	// R1 has an empty slot (S7 has no Pick): playable 4 of 5
	for _, it := range list(records.ListFilter{Statuses: []records.Status{records.StatusPublished}}).Items {
		if it.Slots != 5 || it.Playable != 4 {
			t.Errorf("R1 summary = %+v", it)
		}
	}
}
