package duels_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/duels/duelstest"
	"github.com/nicolasacchi/metamusic/internal/ladder"
)

func TestService_UnconfiguredAnswersUnsupported(t *testing.T) {
	t.Parallel()
	s := duels.New(duels.Options{})
	ctx := context.Background()
	checks := map[string]error{}
	_, checks["create"] = s.Create(ctx, human, duels.CreateInput{})
	_, checks["next"] = s.Next(ctx, human, nil)
	_, checks["get"] = s.Get(ctx, human, "x")
	_, checks["list"] = s.List(ctx, human, duels.ListFilter{})
	_, checks["submit"] = s.Submit(ctx, human, "x", duels.VerdictInput{})
	_, checks["ladder"] = s.Ladder(ctx, duels.LadderQuery{})
	_, checks["pairs"] = s.PairStats(ctx, "a", "b")
	_, _, checks["strengths"] = s.Strengths(ctx, nil)
	_, checks["clipkeys"] = s.OpenClipKeys(ctx)
	for name, err := range checks {
		if !errors.Is(err, errors.ErrUnsupported) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestService_ClipFailures(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	boom := errors.New("render boom")
	e.rend.EnsureErr = boom
	if _, err := e.svc.Get(ctx, human, duelstest.DuelOpen); !errors.Is(err, boom) {
		t.Errorf("get: %v", err)
	}
	if _, err := e.svc.Next(ctx, human, nil); !errors.Is(err, boom) {
		t.Errorf("next: %v", err)
	}
	// the verdict is stored even when the clips cannot be produced; the view then carries empty links
	r, err := e.svc.Submit(ctx, human, duelstest.DuelOpen, duels.VerdictInput{Verdict: "a"})
	if err != nil || r.Duel.Slots.A.Clip.URL != "" || r.Verdict.ID == "" {
		t.Errorf("submit = %+v, %v", r, err)
	}
	// a replay of that verdict still works
	if r2, err := e.svc.Submit(ctx, device, duelstest.DuelOpen, duels.VerdictInput{Verdict: "a"}); err != nil || !r2.Replayed {
		t.Errorf("replay = %+v, %v", r2, err)
	}
	// a system principal can neither judge nor see a queue, but can read a duel
	if _, err := e.svc.Submit(ctx, auth.CLI, duelstest.DuelOpen, duels.VerdictInput{Verdict: "a"}); !errors.Is(err, duels.ErrNoSource) {
		t.Errorf("system submit: %v", err)
	}
	e.rend.EnsureErr = nil
	d, err := e.svc.Get(ctx, auth.CLI, duelstest.DuelDecided)
	if err != nil || d.MyVerdict != nil || !d.Revealed || d.HumanVerdict == nil || len(d.CriticVerdicts) != 0 {
		t.Errorf("system get = %+v, %v", d, err)
	}
}

func TestLadder_CustomOptions(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(o *duels.Options) { o.Ladder = ladder.Options{PriorSD: 2, NuShape: 3, NuRate: 4} })
	p := e.ladder(t, duels.LadderQuery{Scope: "lineage", RootTrackID: duelstest.TrackAlpha})
	if p.Model.PriorSD != 2 || p.Model.NuPrior != "gamma(3,4)" {
		t.Errorf("model = %+v", p.Model)
	}
}

func TestLadder_EmptyAndLineageGrowth(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// Luna's lineage has one track and no duels
	p := e.ladder(t, duels.LadderQuery{Scope: "lineage", RootTrackID: "01900000-0000-7000-8000-000000000101"})
	if p.Total != 1 || p.Items[0].SD != 1.5 || p.Counts.VerdictsHumanCounted != 0 || p.Diagnostics.SlotAWinRate != nil || !p.Model.Converged {
		t.Errorf("page = %+v", p)
	}
}

func TestOpenClipKeys_WithoutClips(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(o *duels.Options) { o.Clips = nil })
	keys, err := e.svc.OpenClipKeys(context.Background())
	if err != nil || len(keys) != 0 {
		t.Errorf("keys = %v, %v", keys, err)
	}
}
