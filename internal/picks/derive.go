package picks

import (
	"math"
	"sort"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// Derive is the pure Pick derivation of D-L1-9 and D-L1-38: pinned, then ladder (with incumbent stickiness), then
// human stars, then newest. It performs no I/O and is deterministic.
func Derive(in Input) Result {
	elig := make([]TakeInput, 0, len(in.Takes))
	byID := make(map[string]TakeInput, len(in.Takes))
	for _, t := range in.Takes {
		byID[t.ID] = t
		if t.Status != "discarded" {
			elig = append(elig, t)
		}
	}
	sort.SliceStable(elig, func(i, j int) bool { return newer(elig[i], elig[j]) })
	ev := Evidence{Def: EvidenceDef, Eligible: len(elig)}
	res := Result{Source: SourceNone}

	// 1. pinned
	if in.PinnedTakeID != nil {
		if t, ok := byID[*in.PinnedTakeID]; ok && t.Status != "discarded" {
			ev.Rule = string(SourcePinned)
			return Result{TakeID: ptr(t.ID), Source: SourcePinned, Evidence: ev}
		}
		status := "absent"
		if t, ok := byID[*in.PinnedTakeID]; ok {
			status = t.Status
		}
		res.PinIneligible = true
		ev.PinIneligible = &PinIneligibleEvidence{TakeID: *in.PinnedTakeID, Status: status}
	}
	if len(elig) == 0 {
		ev.Rule = string(SourceNone)
		res.Evidence = ev
		return res
	}

	// 2. ladder with incumbent stickiness
	var incumbent *TakeInput
	if in.IncumbentTakeID != nil {
		for i := range elig {
			if elig[i].ID == *in.IncumbentTakeID {
				incumbent = &elig[i]
			}
		}
	}
	switch {
	case in.IntraDuels <= 0:
	case in.IntraDuels < LadderMinDuels:
		ev.LadderSkipped = &LadderSkipped{DuelsN: in.IntraDuels, Reason: "few_duels"}
	case len(elig) < 2:
		ev.LadderSkipped = &LadderSkipped{DuelsN: in.IntraDuels, Reason: "single_take"}
	default:
		if r, ok := deriveLadder(in, elig, incumbent, &ev); ok {
			r.PinIneligible = res.PinIneligible
			return r
		}
	}

	// 3. stars
	best := -1
	for _, t := range elig {
		if t.HumanStars != nil && *t.HumanStars > best {
			best = *t.HumanStars
		}
	}
	if best >= 0 {
		tied := 0
		var pick *TakeInput
		for i := range elig { // elig is newest first, so the first match is the newest of the tied takes
			if elig[i].HumanStars != nil && *elig[i].HumanStars == best {
				tied++
				if pick == nil {
					pick = &elig[i]
				}
			}
		}
		ev.Rule = string(SourceStars)
		ev.Stars, ev.Tied = &best, &tied
		res.TakeID, res.Source, res.Evidence = ptr(pick.ID), SourceStars, ev
		return res
	}

	// 4. newest
	ev.Rule = string(SourceNewest)
	ev.RecordedAt = store.FormatTime(time.UnixMilli(elig[0].RecordedAt))
	res.TakeID, res.Source, res.Evidence = ptr(elig[0].ID), SourceNewest, ev
	return res
}

type ranked struct {
	t  TakeInput
	u  float64
	sd float64
}

func strength(t TakeInput) ranked {
	if t.U != nil && t.SD != nil {
		return ranked{t, *t.U, *t.SD}
	}
	return ranked{t, 0, LadderPriorSD}
}

func z(x, y ranked) float64 { return (x.u - y.u) / math.Sqrt(x.sd*x.sd+y.sd*y.sd) }

func seval(r ranked) *StrengthEvidence {
	return &StrengthEvidence{ID: r.t.ID, U: R4(r.u), SD: R4(r.sd)}
}

func deriveLadder(in Input, elig []TakeInput, incumbent *TakeInput, ev *Evidence) (Result, bool) {
	rs := make([]ranked, len(elig))
	for i, t := range elig {
		rs[i] = strength(t)
	}
	sort.SliceStable(rs, func(i, j int) bool { // by u descending, ties by newest (elig is already newest first)
		return rs[i].u > rs[j].u
	})
	a, b := rs[0], rs[1]
	zab := z(a, b)
	decide := func(leader, runner ranked, zz float64) (Result, bool) {
		e := *ev
		e.Rule = string(SourceLadder)
		n, mz, md := in.IntraDuels, R4(LadderChallengeZ), LadderMinDuels
		zr := R4(zz)
		e.DuelsN, e.Leader, e.RunnerUp, e.Z, e.MinZ, e.MinDuels = &n, seval(leader), seval(runner), &zr, &mz, &md
		return Result{TakeID: ptr(leader.t.ID), Source: SourceLadder, Evidence: e}, true
	}
	fail := func(zz float64) {
		zr := R4(zz)
		ev.LadderSkipped = &LadderSkipped{DuelsN: in.IntraDuels, Z: &zr, Reason: "margin"}
	}
	if incumbent == nil {
		if zab >= LadderChallengeZ-ladderEpsilon {
			return decide(a, b, zab)
		}
		fail(zab)
		return Result{}, false
	}
	if incumbent.ID == a.t.ID {
		zk := R4(zab)
		ev.Incumbent = &IncumbentEvidence{ID: incumbent.ID, Held: zab >= LadderKeepZ-ladderEpsilon, ZKeep: &zk}
		if ev.Incumbent.Held {
			return decide(a, b, zab)
		}
		fail(zab)
		return Result{}, false
	}
	var inc ranked
	for _, r := range rs {
		if r.t.ID == incumbent.ID {
			inc = r
		}
	}
	zai := z(a, inc)
	zc := R4(zai)
	if zai < LadderChallengeZ-ladderEpsilon { // the challenger is not clear of the incumbent: the incumbent holds
		ev.Incumbent = &IncumbentEvidence{ID: incumbent.ID, Held: true, ZChallenge: &zc}
		return decide(inc, a, -zai)
	}
	ev.Incumbent = &IncumbentEvidence{ID: incumbent.ID, Held: false, ZChallenge: &zc}
	if zab >= LadderChallengeZ-ladderEpsilon {
		return decide(a, b, zab)
	}
	fail(zab)
	return Result{}, false
}

// newer reports whether x sorts before y when ordering newest first: (RecordedAt, CreatedAt, ID) descending.
func newer(x, y TakeInput) bool {
	if x.RecordedAt != y.RecordedAt {
		return x.RecordedAt > y.RecordedAt
	}
	if x.CreatedAt != y.CreatedAt {
		return x.CreatedAt > y.CreatedAt
	}
	return x.ID > y.ID
}

func ptr[T any](v T) *T { return &v }
