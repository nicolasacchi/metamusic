package mandates

import (
	"slices"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/spend"
)

func stars(n int) *int { return &n }

func baseMandate() Mandate {
	sc := goodScope()
	return Mandate{ID: "m1", HolderKey: "agent:nightshift", Budget: 1_000_000, State: StateApproved,
		ValidFrom: t0.Add(-time.Hour), ValidUntil: t0.Add(24 * time.Hour), Scope: sc}
}

func baseInput() CoverInput {
	return CoverInput{PrincipalKey: "agent:nightshift", Engine: "yue2", Ceiling: 500_000, Now: t0, Loc: time.UTC,
		Takes: []CoverTake{{Mode: "new_seed", ParentTrackID: "p1", RootTrackID: "r1", ParentStars: stars(4)}}}
}

func TestCover_Table(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		mut    func(*Mandate, *spend.MandateUsage, *CoverInput)
		reason []string
	}
	takes := func(n int, root string) []CoverTake {
		var out []CoverTake
		for range n {
			out = append(out, CoverTake{Mode: "new_seed", ParentTrackID: "p", RootTrackID: root, ParentStars: stars(5)})
		}
		return out
	}
	cases := []tc{
		{"covered", func(*Mandate, *spend.MandateUsage, *CoverInput) {}, nil},
		{"holder", func(_ *Mandate, _ *spend.MandateUsage, in *CoverInput) { in.PrincipalKey = "agent:other" }, []string{"holder"}},
		{"revoked", func(m *Mandate, _ *spend.MandateUsage, _ *CoverInput) { m.State = StateRevoked }, []string{"not_live"}},
		{"scheduled", func(m *Mandate, _ *spend.MandateUsage, _ *CoverInput) { m.ValidFrom = t0.Add(time.Minute) }, []string{"not_live"}},
		{"ended exactly now", func(m *Mandate, _ *spend.MandateUsage, _ *CoverInput) { m.ValidUntil = t0 }, []string{"not_live"}},
		{"live at from", func(m *Mandate, _ *spend.MandateUsage, _ *CoverInput) { m.ValidFrom = t0 }, nil},
		{"engine", func(_ *Mandate, _ *spend.MandateUsage, in *CoverInput) { in.Engine = "acestep15" }, []string{"engine"}},
		{"mode style_edit allowed", func(_ *Mandate, _ *spend.MandateUsage, in *CoverInput) { in.Takes[0].Mode = "style_edit" }, nil},
		{"mode lyric_edit", func(_ *Mandate, _ *spend.MandateUsage, in *CoverInput) { in.Takes[0].Mode = "lyric_edit" }, []string{"mode"}},
		{"mode narrowed", func(m *Mandate, _ *spend.MandateUsage, _ *CoverInput) { m.Scope.Modes = []string{"style_edit"} }, []string{"mode"}},
		{"stars equal min", func(_ *Mandate, _ *spend.MandateUsage, in *CoverInput) { in.Takes[0].ParentStars = stars(3) }, nil},
		{"stars below min", func(_ *Mandate, _ *spend.MandateUsage, in *CoverInput) { in.Takes[0].ParentStars = stars(2) }, []string{"min_stars"}},
		{"unrated parent", func(_ *Mandate, _ *spend.MandateUsage, in *CoverInput) { in.Takes[0].ParentStars = nil }, []string{"min_stars"}},
		{"root filter hit", func(m *Mandate, _ *spend.MandateUsage, _ *CoverInput) { m.Scope.RootTrackIDs = []string{"r1", "r2"} }, nil},
		{"root filter miss", func(m *Mandate, _ *spend.MandateUsage, _ *CoverInput) { m.Scope.RootTrackIDs = []string{"r2"} }, []string{"root_filter"}},
		{"per lineage equal", func(_ *Mandate, u *spend.MandateUsage, _ *CoverInput) { u.ByRoot = map[string]int{"r1": 2} }, nil},
		{"per lineage over", func(_ *Mandate, u *spend.MandateUsage, _ *CoverInput) { u.ByRoot = map[string]int{"r1": 3} }, []string{"per_lineage"}},
		{"per lineage other root", func(_ *Mandate, u *spend.MandateUsage, _ *CoverInput) { u.ByRoot = map[string]int{"r9": 50} }, nil},
		{"per lineage in batch", func(_ *Mandate, _ *spend.MandateUsage, in *CoverInput) { in.Takes = takes(4, "r1") }, []string{"per_lineage"}},
		{"per batch equal", func(m *Mandate, _ *spend.MandateUsage, in *CoverInput) {
			m.Scope.MaxTakesPerLineage = 50
			in.Takes = takes(6, "r1")
		}, nil},
		{"per batch over", func(m *Mandate, _ *spend.MandateUsage, in *CoverInput) {
			m.Scope.MaxTakesPerLineage = 50
			in.Takes = takes(7, "r1")
		}, []string{"per_batch_takes"}},
		{"ceiling equal", func(_ *Mandate, _ *spend.MandateUsage, in *CoverInput) { in.Ceiling = 943_587 }, nil},
		{"ceiling over", func(_ *Mandate, _ *spend.MandateUsage, in *CoverInput) { in.Ceiling = 943_588 }, []string{"batch_ceiling"}},
		{"day batches under", func(m *Mandate, u *spend.MandateUsage, _ *CoverInput) {
			m.Scope.MaxBatchesPerDay = 2
			u.DayBatches = map[string]int{"2026-10-14": 1}
		}, nil},
		{"day batches at max", func(_ *Mandate, u *spend.MandateUsage, _ *CoverInput) { u.DayBatches = map[string]int{"2026-10-14": 1} }, []string{"batches_per_day"}},
		{"yesterday does not count", func(_ *Mandate, u *spend.MandateUsage, _ *CoverInput) { u.DayBatches = map[string]int{"2026-10-13": 4} }, nil},
		{"tier b refused", func(m *Mandate, _ *spend.MandateUsage, in *CoverInput) { m.Scope.TierB = false; in.TierB = true }, []string{"tier_b"}},
		{"tier b not asked", func(m *Mandate, _ *spend.MandateUsage, _ *CoverInput) { m.Scope.TierB = false }, nil},
		{"budget equal", func(_ *Mandate, u *spend.MandateUsage, in *CoverInput) { in.Ceiling = 56_413; u.Committed = 943_587 }, nil},
		{"budget over", func(_ *Mandate, u *spend.MandateUsage, in *CoverInput) { in.Ceiling = 56_414; u.Committed = 943_587 }, []string{"budget"}},
		{"several, in order", func(m *Mandate, u *spend.MandateUsage, in *CoverInput) {
			m.State = StateRevoked
			in.Engine = "x"
			in.Ceiling = 2_000_000
			u.Committed = 1
		}, []string{"not_live", "engine", "batch_ceiling", "budget"}},
		{"everything", func(m *Mandate, u *spend.MandateUsage, in *CoverInput) {
			m.State = StateRevoked
			m.Scope.TierB = false
			m.Scope.RootTrackIDs = []string{"r2"}
			in.PrincipalKey, in.Engine, in.TierB, in.Ceiling = "agent:x", "x", true, 2_000_000
			in.Takes = []CoverTake{{Mode: "lyric_edit", RootTrackID: "r1"}}
			u.ByRoot, u.DayBatches, u.Committed = map[string]int{"r1": 3}, map[string]int{"2026-10-14": 1}, 1
		}, []string{"holder", "not_live", "engine", "mode", "min_stars", "root_filter", "per_lineage", "batch_ceiling", "batches_per_day", "tier_b", "budget"}},
		{"local day boundary", func(_ *Mandate, u *spend.MandateUsage, in *CoverInput) {
			rome, err := time.LoadLocation("Europe/Rome")
			if err != nil {
				t.Skip("no tzdata")
			}
			in.Loc, in.Now = rome, time.Date(2026, 10, 14, 23, 30, 0, 0, time.UTC) // 01:30 on the 15th in Rome
			u.DayBatches = map[string]int{"2026-10-14": 1}
		}, nil},
		{"nil zone is the process zone", func(_ *Mandate, _ *spend.MandateUsage, in *CoverInput) { in.Loc = nil }, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			m, u, in := baseMandate(), spend.MandateUsage{ByRoot: map[string]int{}, DayBatches: map[string]int{}}, baseInput()
			c.mut(&m, &u, &in)
			ok, reasons := Cover(m, u, in)
			if ok != (len(c.reason) == 0) || !slices.Equal(reasons, c.reason) {
				t.Fatalf("covered %v reasons %v, want %v", ok, reasons, c.reason)
			}
		})
	}
	if len(cases) < 26 {
		t.Fatalf("%d rows, the contract wants at least 26", len(cases))
	}
}
