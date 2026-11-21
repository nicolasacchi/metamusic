package mandates

import (
	"slices"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// Reasons Cover reports, in the order it reports them (C368.1).
const (
	ReasonHolder        = "holder"
	ReasonNotLive       = "not_live"
	ReasonEngine        = "engine"
	ReasonMode          = "mode"
	ReasonMinStars      = "min_stars"
	ReasonRootFilter    = "root_filter"
	ReasonPerLineage    = "per_lineage"
	ReasonPerBatchTakes = "per_batch_takes"
	ReasonBatchCeiling  = "batch_ceiling"
	ReasonBatchesPerDay = "batches_per_day"
	ReasonTierB         = "tier_b"
	ReasonBudget        = "budget"
)

// CoverTake is one planned take as the mandate sees it. ParentStars is the Owner's stars on the parent (nil: unrated).
type CoverTake struct {
	Mode, ParentTrackID, RootTrackID string
	ParentStars                      *int
}

// CoverInput is one plan to cover. Loc is the zone of "per day" (the spend Location); nil is the process zone.
type CoverInput struct {
	PrincipalKey, Engine string
	Takes                []CoverTake
	Ceiling              money.MicroUSD
	TierB                bool
	Now                  time.Time
	Loc                  *time.Location
}

// Cover says whether mandate m, with usage u, covers the plan, and every reason it does not (C368.1). It reads nothing: the caller
// supplies the usage through the same transaction that will write the batch.
func Cover(m Mandate, u spend.MandateUsage, in CoverInput) (covered bool, reasons []string) {
	sc := m.Scope
	fail := func(r string) { reasons = append(reasons, r) }
	if in.PrincipalKey != m.HolderKey {
		fail(ReasonHolder)
	}
	if m.EffectiveState(in.Now) != EffLive {
		fail(ReasonNotLive)
	}
	if !sc.hasEngine(in.Engine) {
		fail(ReasonEngine)
	}
	if slices.ContainsFunc(in.Takes, func(t CoverTake) bool { return !sc.hasMode(t.Mode) }) {
		fail(ReasonMode)
	}
	if slices.ContainsFunc(in.Takes, func(t CoverTake) bool { return t.ParentStars == nil || *t.ParentStars < sc.MinStars }) {
		fail(ReasonMinStars)
	}
	if sc.RootTrackIDs != nil && slices.ContainsFunc(in.Takes, func(t CoverTake) bool { return !slices.Contains(sc.RootTrackIDs, t.RootTrackID) }) {
		fail(ReasonRootFilter)
	}
	perRoot := map[string]int{}
	for _, t := range in.Takes {
		perRoot[t.RootTrackID]++
	}
	for root, n := range perRoot {
		if u.ByRoot[root]+n > sc.MaxTakesPerLineage {
			fail(ReasonPerLineage)
			break
		}
	}
	if len(in.Takes) > sc.MaxTakesPerBatch {
		fail(ReasonPerBatchTakes)
	}
	if in.Ceiling > sc.MaxBatchCeilingMicroUSD {
		fail(ReasonBatchCeiling)
	}
	loc := in.Loc
	if loc == nil {
		loc = time.Local
	}
	if u.DayBatches[in.Now.In(loc).Format(time.DateOnly)]+1 > sc.MaxBatchesPerDay {
		fail(ReasonBatchesPerDay)
	}
	if in.TierB && !sc.TierB {
		fail(ReasonTierB)
	}
	if !spend.MandateFits(m.Budget, u.Committed, in.Ceiling) {
		fail(ReasonBudget)
	}
	return len(reasons) == 0, reasons
}
