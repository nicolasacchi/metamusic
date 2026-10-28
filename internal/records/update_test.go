package records_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/records"
)

func TestRecords_Update_Rules(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	rec := r.newRecord(agent, "S1", "S2")
	upd := func(p records.Patch) (records.Record, error) { return r.svc.Update(ctx, agent, rec.ID, p) }

	// an empty patch
	_, err := upd(records.Patch{Revision: 1})
	wantProblem(t, err, 422, "VALIDATION_FAILED")
	// field rules
	for _, p := range []records.Patch{
		{Revision: 1, Title: sp("")}, {Revision: 1, Title: sp(strings.Repeat("x", 121))}, {Revision: 1, Subtitle: sp(strings.Repeat("x", 201))},
		{Revision: 1, LinerNote: sp("a\x00")}, {Revision: 1, Title: sp("a\x1bb")},
	} {
		_, err := upd(p)
		wantProblem(t, err, 422, "VALIDATION_FAILED")
	}
	// unknown record
	_, err = r.svc.Update(ctx, agent, "nope", records.Patch{Revision: 1, Title: sp("x")})
	wantProblem(t, err, 404, "NOT_FOUND")

	// text edits: renamed, then retexted (one row for subtitle and note together)
	got, err := upd(records.Patch{Revision: 1, Title: sp("  Renamed   Record "), Subtitle: sp("sub"), LinerNote: sp("note")})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Renamed Record" || got.Subtitle != "sub" || got.LinerNote != "note" || got.Revision != 2 || got.UpdatedBy != agent.Key() {
		t.Errorf("after update: %+v", got)
	}
	same(t, "kinds", kinds(r.changes(rec.ID)[1+2:]), []string{"renamed", "retexted"})

	// the revision lock: 409 with the current revision and status
	_, err = upd(records.Patch{Revision: 1, Title: sp("late")})
	pe := wantProblem(t, err, 409, "STALE_REVISION")
	same(t, "details", pe.Details, map[string]any{"revision": 2, "status": "draft"})

	// no effective change: no write, no revision, no row
	before := len(r.changes(rec.ID))
	got, err = upd(records.Patch{Revision: 2, Title: sp("Renamed Record"), Subtitle: sp("sub")})
	if err != nil || got.Revision != 2 || len(r.changes(rec.ID)) != before {
		t.Errorf("no-op update: rev %d rows %d->%d err %v", got.Revision, before, len(r.changes(rec.ID)), err)
	}

	// cover re-roll: counter, seed, row
	got, err = upd(records.Patch{Revision: 2, CoverReroll: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.CoverRerolls != 1 || got.CoverSeed != records.CoverSeed(rec.ID, 1) || got.CoverSeed == rec.CoverSeed || got.Revision != 3 {
		t.Errorf("after re-roll: %+v", got)
	}
	got, err = upd(records.Patch{Revision: 3, CoverReroll: true})
	if err != nil || got.CoverRerolls != 2 || got.CoverSeed != records.CoverSeed(rec.ID, 2) {
		t.Errorf("second re-roll: %+v %v", got, err)
	}

	// hide, hide again (no row), unhide
	got, err = upd(records.Patch{Revision: 4, Hidden: bp(true)})
	if err != nil || got.HiddenAt == nil {
		t.Fatalf("hide: %+v %v", got, err)
	}
	got, err = upd(records.Patch{Revision: 5, Hidden: bp(true)})
	if err != nil || got.Revision != 5 {
		t.Fatalf("hide again: %+v %v", got, err)
	}
	got, err = upd(records.Patch{Revision: 5, Hidden: bp(false)})
	if err != nil || got.HiddenAt != nil || got.Revision != 6 {
		t.Fatalf("unhide: %+v %v", got, err)
	}
	cs := r.changes(rec.ID)
	same(t, "kinds", kinds(cs[len(cs)-4:]), []string{"cover_rerolled", "cover_rerolled", "hidden", "unhidden"})
}

func bp(b bool) *bool { return &b }
