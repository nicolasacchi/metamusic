package recordview_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/recordview"
	"github.com/nicolasacchi/metamusic/internal/tape"
)

type errSongs struct{ err error }

func (e errSongs) Briefs(context.Context, []string) (map[string]recordview.SongBrief, error) {
	return nil, e.err
}

type errCovers struct{ err error }

func (e errCovers) Links(context.Context, cover.Input) (recordview.CoverRef, error) {
	return recordview.CoverRef{}, e.err
}

type okCovers struct{ n *int }

func (o okCovers) Links(_ context.Context, in cover.Input) (recordview.CoverRef, error) {
	*o.n++
	return recordview.CoverRef{URL512: "u512/" + in.ID}, nil
}

type errTapes struct{ err error }

func (e errTapes) Brief(context.Context, string, int) ([]tape.Brief, error) { return nil, e.err }

func TestCov_GetAllIncludes(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	id := idOf(t, r, "records", r1Title)
	v, err := r.Services.RecordView.Get(ctx, human, id, recordview.GetOptions{Include: recordview.Includes, ChangesLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if v.Liner == nil || v.Liner.Markdown == "" || len(v.Liner.Summary) == 0 {
		t.Errorf("liner = %+v", v.Liner)
	}
	if v.Energy == nil || len(v.Energy.Slots) != len(v.Slots) || v.Energy.TotalDurationS <= 0 {
		t.Fatalf("energy = %+v", v.Energy)
	}
	for i, es := range v.Energy.Slots {
		if es.SlotID != v.Slots[i].ID {
			t.Errorf("energy slot %d = %s, want %s", i, es.SlotID, v.Slots[i].ID)
		}
		if len(es.LUFS) > 120 {
			t.Errorf("slot %d has %d points", i, len(es.LUFS))
		}
		if (es.GainDB == nil) != (v.Slots[i].GainDB == nil) {
			t.Errorf("slot %d gain differs from the slot view", i)
		}
	}
	if len(v.Changes) > 2 {
		t.Errorf("ChangesLimit 2 returned %d", len(v.Changes))
	}
	if v.Preview == nil {
		t.Error("no preview")
	}
}

func TestCov_ChangeViews(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	id := idOf(t, r, "records", r1Title)
	// an upgrade produces a changelog row naming Takes
	song := idOf(t, r, "songs", "Synthetic Song S3")
	b2 := idOf(t, r, "tracks", "Synthetic Take B2")
	if resp := r.Human(t).Do(t, "PUT", "/api/songs/"+song+"/pin", map[string]any{"take_id": b2}); resp.Status != 200 {
		t.Fatalf("pin: %d %s", resp.Status, resp.Body)
	}
	r.Settle(t)
	// default limit (0) and an over-limit value both work
	for _, lim := range []int{0, 500} {
		v, err := r.Services.RecordView.Get(ctx, human, id, recordview.GetOptions{Include: []string{recordview.IncludeChanges}, ChangesLimit: lim})
		if err != nil {
			t.Fatal(err)
		}
		if len(v.Changes) == 0 || len(v.Changes) > 50 {
			t.Fatalf("limit %d: %d changes", lim, len(v.Changes))
		}
	}
	page, err := r.Services.Records.Changes(ctx, recordsFilter(id))
	if err != nil || len(page.Items) == 0 {
		t.Fatalf("changes: %v %v", page.Items, err)
	}
	vs, err := r.Services.RecordView.ChangeViews(ctx, page.Items)
	if err != nil || len(vs) != len(page.Items) {
		t.Fatalf("ChangeViews: %d %v", len(vs), err)
	}
	var named bool
	for _, cv := range vs {
		if cv.Text == "" {
			t.Errorf("row %s has no text", cv.Change.ID)
		}
		for _, ref := range []*recordview.TakeRef{cv.From, cv.To} {
			if ref != nil {
				named = true
				if ref.Title == "" {
					t.Errorf("take %s has no title", ref.ID)
				}
			}
		}
	}
	if !named {
		t.Error("no row named a Take")
	}
	if vs, err := r.Services.RecordView.ChangeViews(ctx, nil); err != nil || len(vs) != 0 {
		t.Errorf("empty = %v %v", vs, err)
	}
}

func TestCov_CoverRefsAndInput(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	r1 := idOf(t, r, "records", r1Title)
	r2 := idOf(t, r, "records", "Synthetic Record Two")
	er, err := r.Services.Records.Create(ctx, human, recordsNew("Empty Synthetic"))
	if err != nil {
		t.Fatal(err)
	}
	empty := er.ID
	refs, err := r.Services.RecordView.CoverRefs(ctx, human, []string{r1, r2, r1, empty, "unknown", ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 3 {
		t.Fatalf("refs = %v", refs)
	}
	for _, id := range []string{r1, r2, empty} {
		if refs[id].URL512 == "" {
			t.Errorf("record %s has no cover", id)
		}
	}
	if _, ok := refs["unknown"]; ok {
		t.Error("unknown id has a cover")
	}
	if got, err := r.Services.RecordView.CoverRefs(ctx, human, nil); err != nil || len(got) != 0 {
		t.Errorf("no ids = %v %v", got, err)
	}
	in, err := r.Services.RecordView.CoverInput(ctx, r1)
	if err != nil || in.ID != r1 || in.Kind != cover.KindRecord || len(in.Parts) == 0 {
		t.Errorf("CoverInput = %+v %v", in, err)
	}
	in, err = r.Services.RecordView.CoverInput(ctx, empty)
	if err != nil || in.Seed != er.CoverSeed || len(in.Parts) != 0 {
		t.Errorf("empty CoverInput = %+v %v", in, err)
	}
}

func TestCov_ServiceDegrades(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	id := idOf(t, r, "records", r1Title)
	boom := errors.New("boom")

	// A bare service (no readers, no store) still assembles slots; cover and pending are empty.
	bare := recordview.New(recordview.Options{Records: r.Services.Records})
	v, err := bare.Get(ctx, human, id, recordview.GetOptions{Include: []string{recordview.IncludeEnergy, recordview.IncludeTapes}})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Slots) != 5 || v.Cover != (recordview.CoverRef{}) || v.Pending || v.Tapes != nil {
		t.Errorf("bare view: %d slots cover %+v pending %v", len(v.Slots), v.Cover, v.Pending)
	}
	refs, err := bare.CoverRefs(ctx, human, []string{id})
	if err != nil || len(refs) != 0 {
		t.Errorf("bare CoverRefs = %v %v", refs, err)
	}

	// Reader failures surface (wrapped), cover failures do not.
	failing := recordview.New(recordview.Options{Records: r.Services.Records, Songs: errSongs{boom}})
	if _, err := failing.Get(ctx, human, id, recordview.GetOptions{}); !errors.Is(err, boom) {
		t.Errorf("songs error = %v", err)
	}
	if _, err := failing.CoverInput(ctx, id); !errors.Is(err, boom) {
		t.Errorf("CoverInput songs error = %v", err)
	}
	soft := recordview.New(recordview.Options{Records: r.Services.Records, Covers: errCovers{boom}})
	if v, err := soft.Get(ctx, human, id, recordview.GetOptions{}); err != nil || v.Cover != (recordview.CoverRef{}) {
		t.Errorf("cover failure: %+v %v", v.Cover, err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	hard := recordview.New(recordview.Options{Records: r.Services.Records, Covers: errCovers{boom}})
	if _, err := hard.Get(cctx, human, id, recordview.GetOptions{}); err == nil {
		t.Error("cancelled context did not fail the View")
	}

	// Tapes: ErrUnsupported means no tapes, any other error fails the View.
	unsup := recordview.New(recordview.Options{Records: r.Services.Records, Tapes: errTapes{errors.ErrUnsupported}})
	if v, err := unsup.Get(ctx, human, id, recordview.GetOptions{Include: []string{recordview.IncludeTapes}}); err != nil || v.Tapes != nil {
		t.Errorf("unsupported tapes: %v %v", v.Tapes, err)
	}
	bad := recordview.New(recordview.Options{Records: r.Services.Records, Tapes: errTapes{boom}})
	if _, err := bad.Get(ctx, human, id, recordview.GetOptions{Include: []string{recordview.IncludeTapes}}); !errors.Is(err, boom) {
		t.Errorf("tapes error = %v", err)
	}

	// CoverRefs calls the linker once per distinct Record.
	n := 0
	counting := recordview.New(recordview.Options{Records: r.Services.Records, Store: r.Store, Covers: okCovers{&n}})
	got, err := counting.CoverRefs(ctx, human, []string{id, id})
	if err != nil || n != 1 || got[id].URL512 != "u512/"+id {
		t.Errorf("CoverRefs: n=%d %v %v", n, got, err)
	}
}

func recordsFilter(id string) records.ChangeFilter {
	return records.ChangeFilter{RecordID: id, Order: "desc", Limit: 50}
}
