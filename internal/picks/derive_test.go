package picks_test

import (
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/picks"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func inst(t *testing.T, s string) int64 {
	t.Helper()
	v, err := store.ParseInstant(s)
	if err != nil {
		t.Fatal(err)
	}
	return v.UnixMilli()
}

var base = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

// tk builds a take recorded `min` minutes after the base instant.
func tk(id string, minutes int, opts ...func(*picks.TakeInput)) picks.TakeInput {
	ms := base.Add(time.Duration(minutes) * time.Minute).UnixMilli()
	t := picks.TakeInput{ID: id, Status: "draft", RecordedAt: ms, CreatedAt: ms}
	for _, o := range opts {
		o(&t)
	}
	return t
}

func stars(n int) func(*picks.TakeInput) { return func(t *picks.TakeInput) { t.HumanStars = &n } }
func us(u, sd float64) func(*picks.TakeInput) {
	return func(t *picks.TakeInput) { t.U, t.SD = &u, &sd }
}
func status(s string) func(*picks.TakeInput) { return func(t *picks.TakeInput) { t.Status = s } }
func sp(s string) *string                    { return &s }

func TestPick_DerivationTable(t *testing.T) {
	t.Parallel()
	type want struct {
		take    string // "" = none
		source  picks.Source
		pinInel bool
		skipped string // ladder_skipped.reason, "" = absent
	}
	rows := []struct {
		name string
		in   picks.Input
		want want
	}{
		{"P01 no takes", picks.Input{}, want{"", picks.SourceNone, false, ""}},
		{"P02 one draft take", picks.Input{Takes: []picks.TakeInput{tk("a", 0)}}, want{"a", picks.SourceNewest, false, ""}},
		{"P03 one discarded", picks.Input{Takes: []picks.TakeInput{tk("a", 0, status("discarded"))}}, want{"", picks.SourceNone, false, ""}},
		{"P04 newer wins", picks.Input{Takes: []picks.TakeInput{tk("a", 0), tk("b", 1)}}, want{"b", picks.SourceNewest, false, ""}},
		{"P05 stars beat newer", picks.Input{Takes: []picks.TakeInput{tk("a", 0, stars(4)), tk("b", 1)}}, want{"a", picks.SourceStars, false, ""}},
		{"P06 tied stars newest", picks.Input{Takes: []picks.TakeInput{tk("a", 0, stars(4)), tk("b", 1, stars(4))}}, want{"b", picks.SourceStars, false, ""}},
		{"P07 higher stars", picks.Input{Takes: []picks.TakeInput{tk("a", 0, stars(5)), tk("b", 1, stars(3))}}, want{"a", picks.SourceStars, false, ""}},
		{"P08 pinned", picks.Input{Takes: []picks.TakeInput{tk("a", 0), tk("b", 1, stars(5))}, PinnedTakeID: sp("a")}, want{"a", picks.SourcePinned, false, ""}},
		{"P09 pinned discarded", picks.Input{Takes: []picks.TakeInput{tk("a", 0, status("discarded")), tk("b", 1, stars(5))}, PinnedTakeID: sp("a")}, want{"b", picks.SourceStars, true, ""}},
		{"P10 ladder leader", picks.Input{IntraDuels: 3, Takes: []picks.TakeInput{tk("a", 0, us(1.2, 0.5)), tk("b", 1, us(0.3, 0.6), stars(5))}}, want{"a", picks.SourceLadder, false, ""}},
		{"P11 few duels", picks.Input{IntraDuels: 2, Takes: []picks.TakeInput{tk("a", 0, us(1.2, 0.5)), tk("b", 1, us(0.3, 0.6), stars(5))}}, want{"b", picks.SourceStars, false, "few_duels"}},
		{"P12 margin too small", picks.Input{IntraDuels: 3, Takes: []picks.TakeInput{tk("a", 0, us(0.6, 0.5)), tk("b", 1, us(0.3, 0.6))}}, want{"b", picks.SourceNewest, false, "margin"}},
		{"P13 exactly 0.5 passes", picks.Input{IntraDuels: 3, Takes: []picks.TakeInput{tk("a", 0, us(0.5, 0.6)), tk("b", 1, us(0, 0.8))}}, want{"a", picks.SourceLadder, false, ""}},
		{"P14 z over the eligible only", picks.Input{IntraDuels: 3, Takes: []picks.TakeInput{tk("a", 0, us(1.30, 0.5), status("discarded")), tk("b", 1, us(0.90, 0.5)), tk("c", 2, us(0.20, 0.6))}}, want{"b", picks.SourceLadder, false, ""}},
		{"P15 pin beats ladder", picks.Input{IntraDuels: 3, PinnedTakeID: sp("b"), Takes: []picks.TakeInput{tk("a", 0, us(1.2, 0.5)), tk("b", 1, us(0.3, 0.6))}}, want{"b", picks.SourcePinned, false, ""}},
		{"P16 absent from the fit", picks.Input{IntraDuels: 3, Takes: []picks.TakeInput{tk("a", 0, us(1.5, 0.4)), tk("b", 1, us(-0.2, 0.5)), tk("c", 2)}}, want{"a", picks.SourceLadder, false, ""}},
		{"P17 critic stars never count", picks.Input{Takes: []picks.TakeInput{tk("a", 0), tk("b", 1)}}, want{"b", picks.SourceNewest, false, ""}},
		{"P18 lossy with stars", picks.Input{Takes: []picks.TakeInput{tk("a", 0, stars(3)), tk("b", 1)}}, want{"a", picks.SourceStars, false, ""}},
		{"P19 equal time, greater id", picks.Input{Takes: []picks.TakeInput{tk("a", 0), tk("b", 0)}}, want{"b", picks.SourceNewest, false, ""}},
		{"P20 all discarded after a pick", picks.Input{Takes: []picks.TakeInput{tk("a", 0, status("discarded")), tk("b", 1, status("discarded"))}}, want{"", picks.SourceNone, false, ""}},
		{"P21 ladder tie falls back", picks.Input{IntraDuels: 3, Takes: []picks.TakeInput{tk("a", 0, us(0.5, 0.5)), tk("b", 1, us(0.5, 0.5))}}, want{"b", picks.SourceNewest, false, "margin"}},
		{"P22 single eligible take", picks.Input{IntraDuels: 3, Takes: []picks.TakeInput{tk("a", 0, us(1, 0.5)), tk("b", 1, status("discarded"))}}, want{"a", picks.SourceNewest, false, "single_take"}},
		{"P23 cleared stars", picks.Input{Takes: []picks.TakeInput{tk("a", 0), tk("b", 1)}}, want{"b", picks.SourceNewest, false, ""}},
		{"P24 star on discarded ignored", picks.Input{Takes: []picks.TakeInput{tk("a", 0, stars(5), status("discarded")), tk("b", 1)}}, want{"b", picks.SourceNewest, false, ""}},
		{"P25 incumbent held", picks.Input{IntraDuels: 3, IncumbentTakeID: sp("a"), Takes: []picks.TakeInput{tk("a", 0, us(0.70, 0.5)), tk("b", 1, us(0.46, 0.6))}}, want{"a", picks.SourceLadder, false, ""}},
		{"P26 no incumbent same margin", picks.Input{IntraDuels: 3, Takes: []picks.TakeInput{tk("a", 0, us(0.70, 0.5)), tk("b", 1, us(0.46, 0.6))}}, want{"b", picks.SourceNewest, false, "margin"}},
		{"P27 incumbent below keep", picks.Input{IntraDuels: 3, IncumbentTakeID: sp("a"), Takes: []picks.TakeInput{tk("a", 0, us(0.70, 0.5)), tk("b", 1, us(0.54, 0.6))}}, want{"b", picks.SourceNewest, false, "margin"}},
		{"P28 challenger displaces", picks.Input{IntraDuels: 3, IncumbentTakeID: sp("b"), Takes: []picks.TakeInput{tk("a", 0, us(1.00, 0.5)), tk("b", 1, us(0.60, 0.6))}}, want{"a", picks.SourceLadder, false, ""}},
		{"P29 incumbent held against weak challenger", picks.Input{IntraDuels: 3, IncumbentTakeID: sp("b"), Takes: []picks.TakeInput{tk("a", 0, us(0.90, 0.5)), tk("b", 1, us(0.60, 0.6))}}, want{"b", picks.SourceLadder, false, ""}},
		{"P30 discarded incumbent ignored", picks.Input{IntraDuels: 3, IncumbentTakeID: sp("b"), Takes: []picks.TakeInput{tk("a", 0, us(0.90, 0.5)), tk("b", 1, us(0.60, 0.6), status("discarded")), tk("c", 2, us(0.1, 0.5))}}, want{"a", picks.SourceLadder, false, ""}},
		{"P31 as P14 with B 0.45", picks.Input{IntraDuels: 3, Takes: []picks.TakeInput{tk("a", 0, us(1.30, 0.5), status("discarded")), tk("b", 1, us(0.45, 0.5)), tk("c", 2, us(0.20, 0.6))}}, want{"c", picks.SourceNewest, false, "margin"}},
		{"P32 mixed widths", picks.Input{Takes: []picks.TakeInput{
			{ID: "a", Status: "draft", RecordedAt: inst(t, "2026-03-01T10:00:05Z"), CreatedAt: inst(t, "2026-03-01T10:00:05Z")},
			{ID: "b", Status: "draft", RecordedAt: inst(t, "2026-03-01T10:00:05.250Z"), CreatedAt: inst(t, "2026-03-01T10:00:05.250Z")}}}, want{"b", picks.SourceNewest, false, ""}},
	}
	if len(rows) != 32 {
		t.Fatalf("table has %d rows, want 32", len(rows))
	}
	for _, tc := range rows {
		got := picks.Derive(tc.in)
		gt := ""
		if got.TakeID != nil {
			gt = *got.TakeID
		}
		if gt != tc.want.take || got.Source != tc.want.source || got.PinIneligible != tc.want.pinInel {
			t.Errorf("%s: got %q/%s pinInel=%v, want %q/%s pinInel=%v (%+v)", tc.name, gt, got.Source, got.PinIneligible, tc.want.take, tc.want.source, tc.want.pinInel, got.Evidence)
			continue
		}
		if got.Evidence.Rule != string(got.Source) || got.Evidence.Def != picks.EvidenceDef {
			t.Errorf("%s: evidence rule/def %q/%q", tc.name, got.Evidence.Rule, got.Evidence.Def)
		}
		sk := ""
		if got.Evidence.LadderSkipped != nil {
			sk = got.Evidence.LadderSkipped.Reason
		}
		if sk != tc.want.skipped {
			t.Errorf("%s: ladder_skipped %q, want %q", tc.name, sk, tc.want.skipped)
		}
	}
}

func TestPick_LadderZValues(t *testing.T) {
	t.Parallel()
	r := picks.Derive(picks.Input{IntraDuels: 3, Takes: []picks.TakeInput{tk("a", 0, us(1.2, 0.5)), tk("b", 1, us(0.3, 0.6))}})
	if r.Evidence.Z == nil || float64(*r.Evidence.Z) < 1.14 || float64(*r.Evidence.Z) > 1.16 {
		t.Errorf("P10 z = %v, want about 1.15", r.Evidence.Z)
	}
	r = picks.Derive(picks.Input{IntraDuels: 3, Takes: []picks.TakeInput{tk("a", 0, us(1.5, 0.4)), tk("b", 1, us(-0.2, 0.5)), tk("c", 2)}})
	if r.Evidence.Z == nil || float64(*r.Evidence.Z) < 0.96 || float64(*r.Evidence.Z) > 0.98 {
		t.Errorf("P16 z = %v, want about 0.97", r.Evidence.Z)
	}
	r = picks.Derive(picks.Input{IntraDuels: 3, Takes: []picks.TakeInput{
		tk("a", 0, us(1.30, 0.5), status("discarded")), tk("b", 1, us(0.90, 0.5)), tk("c", 2, us(0.20, 0.6))}})
	if r.Evidence.Z == nil || float64(*r.Evidence.Z) < 0.89 || float64(*r.Evidence.Z) > 0.91 {
		t.Errorf("P14 z = %v, want about 0.90", r.Evidence.Z)
	}
}

func TestPick_MixedTimestampWidths(t *testing.T) {
	t.Parallel()
	a, err := store.ParseInstant("2026-03-01T10:00:05Z")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := store.ParseInstant("2026-03-01T10:00:05.250Z")
	r := picks.Derive(picks.Input{Takes: []picks.TakeInput{
		{ID: "old-import", Status: "draft", RecordedAt: a.UnixMilli(), CreatedAt: a.UnixMilli()},
		{ID: "new-job", Status: "draft", RecordedAt: b.UnixMilli(), CreatedAt: b.UnixMilli()},
	}})
	if r.TakeID == nil || *r.TakeID != "new-job" {
		t.Errorf("newest = %v, want the 24-character take (a string compare would pick the 20-character one)", r.TakeID)
	}
}
