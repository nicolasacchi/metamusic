package mandates

import (
	"slices"
	"time"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// Env is what validation needs from the world: the global batch cap, adoption and the existing roots. Nil funcs read as "nothing is
// adopted but yue2" and "every root exists" so a pure test can leave them out.
type Env struct {
	BatchCap money.MicroUSD
	Adopted  func(engine string) (bool, error)
	RootsOK  func(ids []string) (missing string, err error) // the first id that is not an existing root, "" when all are
}

// ScopeError is the 422 of a scope that breaks a rule: MANDATE_SCOPE_INVALID {field, rule}.
func ScopeError(field, rule string) *problem.Error {
	return problem.E(422, "MANDATE_SCOPE_INVALID", "the mandate scope breaks the rule "+rule+" on "+field, map[string]any{"field": field, "rule": rule})
}

func invalid(field, rule string) *problem.Error {
	return problem.E(422, "VALIDATION_FAILED", field+": "+rule, map[string]any{"field": field, "rule": rule})
}

// ValidateScope checks a scope against C366.2 for a mandate of the given budget. The first broken rule is returned.
func ValidateScope(s Scope, budget money.MicroUSD, env Env) error {
	if len(s.Modes) == 0 {
		return ScopeError("scope.modes", "modes_empty")
	}
	for i, m := range s.Modes {
		switch {
		case !slices.Contains(AllowedModes, m):
			return ScopeError("scope.modes", "mode_unknown")
		case slices.Contains(s.Modes[:i], m):
			return ScopeError("scope.modes", "modes_duplicate")
		}
	}
	if len(s.Engines) == 0 {
		return ScopeError("scope.engines", "engines_empty")
	}
	for i, e := range s.Engines {
		if slices.Contains(s.Engines[:i], e) {
			return ScopeError("scope.engines", "engines_duplicate")
		}
		ok := e == "yue2"
		if !ok && env.Adopted != nil {
			var err error
			if ok, err = env.Adopted(e); err != nil {
				return err
			}
		}
		if !ok {
			return ScopeError("scope.engines", "engine_not_adopted")
		}
	}
	if s.MinStars < 1 || s.MinStars > 5 {
		return ScopeError("scope.min_stars", "min_stars_range")
	}
	if s.RootTrackIDs != nil {
		if len(s.RootTrackIDs) < 1 || len(s.RootTrackIDs) > MaxRoots {
			return ScopeError("scope.root_track_ids", "root_track_ids_range")
		}
		for i, id := range s.RootTrackIDs {
			if slices.Contains(s.RootTrackIDs[:i], id) {
				return ScopeError("scope.root_track_ids", "root_track_ids_duplicate")
			}
		}
		if env.RootsOK != nil {
			missing, err := env.RootsOK(s.RootTrackIDs)
			if err != nil {
				return err
			}
			if missing != "" {
				return ScopeError("scope.root_track_ids", "root_not_found")
			}
		}
	}
	if s.MaxTakesPerLineage < 1 || s.MaxTakesPerLineage > MaxTakesPerLineage {
		return ScopeError("scope.max_takes_per_lineage", "max_takes_per_lineage_range")
	}
	if s.MaxTakesPerBatch < 1 || s.MaxTakesPerBatch > MaxTakesPerBatch {
		return ScopeError("scope.max_takes_per_batch", "max_takes_per_batch_range")
	}
	switch {
	case s.MaxBatchCeilingMicroUSD < 1:
		return ScopeError("scope.max_batch_ceiling_micro_usd", "max_batch_ceiling_range")
	case env.BatchCap > 0 && s.MaxBatchCeilingMicroUSD > env.BatchCap:
		return ScopeError("scope.max_batch_ceiling_micro_usd", "ceiling_above_batch_cap")
	case s.MaxBatchCeilingMicroUSD > budget:
		return ScopeError("scope.max_batch_ceiling_micro_usd", "ceiling_above_budget")
	}
	if s.MaxBatchesPerDay < 1 || s.MaxBatchesPerDay > MaxBatchesPerDay {
		return ScopeError("scope.max_batches_per_day", "max_batches_per_day_range")
	}
	return nil
}

// ValidateHeader checks the fields around the scope and returns the resolved window (valid_from defaults to now).
func ValidateHeader(holder, title string, budget money.MicroUSD, from *time.Time, until, now time.Time) (holderKey string, vf time.Time, err error) {
	holderKey, ok := Holders[holder]
	if !ok {
		return "", vf, invalid("holder", "holder_unknown")
	}
	if n := utf8.RuneCountInString(title); n < 1 || n > MaxTitle {
		return "", vf, invalid("title", "title_length")
	}
	if budget < 1 || budget > MaxBudget {
		return "", vf, invalid("budget_micro_usd", "budget_range")
	}
	vf = now
	if from != nil {
		vf = *from
	}
	if vf.Before(now.Add(-FromSlack)) {
		return "", vf, invalid("valid_from", "valid_from_past")
	}
	if !until.After(vf) {
		return "", vf, invalid("valid_until", "valid_until_before_from")
	}
	if until.Sub(vf) > MaxValidity {
		return "", vf, invalid("valid_until", "valid_until_too_far")
	}
	return holderKey, vf, nil
}
