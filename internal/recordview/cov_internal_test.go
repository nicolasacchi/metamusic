package recordview

import (
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/tape"
)

func sp(s string) *string { return &s }

func TestCov_PartOf(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		b     SongBrief
		known bool
		want  cover.Part
	}{
		{"root", SongBrief{RootTrackID: "t1", RootSeed: 7}, true, cover.Part{RootID: "t1", Seed: 7}},
		{"draft", SongBrief{DraftID: sp("d1")}, true, cover.Part{RootID: "d1"}},
		{"root wins over draft", SongBrief{RootTrackID: "t1", DraftID: sp("d1")}, true, cover.Part{RootID: "t1"}},
		{"known but bare", SongBrief{}, true, cover.Part{RootID: "s1"}},
		{"unknown", SongBrief{RootTrackID: "t1"}, false, cover.Part{RootID: "s1"}},
	} {
		if got := partOf("s1", tc.b, tc.known); got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestCov_GainDB(t *testing.T) {
	t.Parallel()
	if gainDB(tape.GainPlan{}, false) != nil {
		t.Error("absent plan has a gain")
	}
	if gainDB(tape.GainPlan{Status: tape.GainUnmeasured, GainCDB: 100}, true) != nil {
		t.Error("unmeasured plan has a gain")
	}
	if g := gainDB(tape.GainPlan{GainCDB: -220}, true); g == nil || *g != -2.2 {
		t.Errorf("gain = %v, want -2.2", g)
	}
}

func TestCov_Rounding(t *testing.T) {
	t.Parallel()
	if got := round2(1.005 + 0.2349); got != 1.24 {
		t.Errorf("round2 = %v", got)
	}
	if got := round2(-3.456); got != -3.46 {
		t.Errorf("round2 = %v", got)
	}
}

func TestCov_Day(t *testing.T) {
	t.Parallel()
	if day("2026-10-06T10:00:00Z") != "2026-10-06" || day("short") != "short" {
		t.Error("day")
	}
	if nz(nil) == nil || len(nz(nil)) != 0 || !slices.Equal(nz([]string{"a"}), []string{"a"}) {
		t.Error("nz")
	}
	pub := "2026-09-01T00:00:00Z"
	if got := linerDate(records.Record{CreatedAt: "2026-08-01T00:00:00Z", FirstPublishedAt: &pub}); got != "2026-09-01" {
		t.Errorf("linerDate = %q", got)
	}
}

func TestCov_ChangeViews(t *testing.T) {
	t.Parallel()
	a, b := "ta", "tb"
	cs := []records.Change{{FromTakeID: &a, ToTakeID: &b}, {ToTakeID: &a}, {}}
	if got, want := takeIDsOf(cs), []string{"ta", "tb", "ta"}; !slices.Equal(got, want) {
		t.Errorf("takeIDsOf = %v, want %v", got, want)
	}
	if takeIDsOf(nil) != nil {
		t.Error("no changes names takes")
	}
	titles := map[string]string{"ta": "Alpha", "tb": "Beta"}
	vs := changeViews(cs, func(id string) string { return titles[id] })
	if len(vs) != 3 {
		t.Fatalf("views = %d", len(vs))
	}
	if diff := cmp.Diff(&TakeRef{ID: "ta", Title: "Alpha"}, vs[0].From); diff != "" {
		t.Error(diff)
	}
	if vs[0].To == nil || vs[0].To.Title != "Beta" || vs[1].From != nil || vs[1].To.Title != "Alpha" || vs[2].From != nil || vs[2].To != nil {
		t.Errorf("splice: %+v", vs)
	}
	if vs[0].Text != records.ChangeText(cs[0], func(id string) string { return titles[id] }) {
		t.Errorf("text = %q", vs[0].Text)
	}
	if len(changeViews(nil, nil)) != 0 {
		t.Error("empty")
	}
}

func TestCov_FirstSongsAndCoverInput(t *testing.T) {
	t.Parallel()
	var rs []records.ResolvedSlot
	for i := 0; i < records.CoverParts+3; i++ {
		rs = append(rs, records.ResolvedSlot{Slot: records.Slot{SongID: string(rune('a' + i))}})
	}
	ids := firstSongs(rs)
	if len(ids) != records.CoverParts || ids[0] != "a" {
		t.Fatalf("firstSongs = %v", ids)
	}
	if firstSongs(nil) != nil {
		t.Error("no slots")
	}
	in := coverInput(records.Record{ID: "r", CoverSeed: 9}, ids, map[string]SongBrief{"a": {RootTrackID: "ra", RootSeed: 3}})
	if in.ID != "r" || in.Seed != 9 || in.Kind != cover.KindRecord || len(in.Parts) != records.CoverParts {
		t.Fatalf("input = %+v", in)
	}
	if in.Parts[0] != (cover.Part{RootID: "ra", Seed: 3}) || in.Parts[1].RootID != "b" {
		t.Errorf("parts = %+v", in.Parts[:2])
	}
}

func TestCov_EnergySlots(t *testing.T) {
	t.Parallel()
	bpm := 120.0
	mk := func(id, song, take string, ok bool) records.ResolvedSlot {
		r := records.ResolvedSlot{Slot: records.Slot{ID: id, SongID: song}, State: records.SlotState("empty")}
		if ok {
			r.State = records.StateOK
			r.Take = &records.TakeInfo{ID: take, DurationS: 100}
		}
		return r
	}
	d := &data{
		rs:     []records.ResolvedSlot{mk("sl1", "s1", "t1", true), mk("sl2", "s2", "", false), mk("sl3", "s3", "t3", true)},
		briefs: map[string]SongBrief{"s1": {BPM: &bpm}},
		loud: map[string]Loudness{
			"t1": {Measure: tape.Measure{OK: true, LUFS: -14, TruePeakDBTP: -3}, ShortTerm1Hz: []float64{-14, -16}},
		},
	}
	e := energy(d)
	if len(e.Slots) != 3 || e.TotalDurationS != 200 {
		t.Fatalf("energy = %+v", e)
	}
	if e.Slots[0].BPM == nil || *e.Slots[0].BPM != 120 || e.Slots[0].MeanLUFS == nil || len(e.Slots[0].LUFS) != 2 {
		t.Errorf("slot1 = %+v", e.Slots[0])
	}
	if s := e.Slots[1]; s.DurationS != 0 || s.GainDB != nil || s.MeanLUFS != nil || s.LUFS == nil || len(s.LUFS) != 0 {
		t.Errorf("empty slot = %+v", s)
	}
	if s := e.Slots[2]; s.DurationS != 100 || s.MeanLUFS != nil || s.GainDB != nil || len(s.LUFS) != 0 {
		t.Errorf("unanalysed slot = %+v", s)
	}
}
