package songpr_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/callbacks"
	"github.com/nicolasacchi/metamusic/internal/engine/registry"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/songpr"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// propTimelines is the number of seeded timelines (MM_PROPERTY_TIMELINES raises it, like the spend property suite of M3).
func propTimelines(t *testing.T, def int) int {
	t.Helper()
	if v := os.Getenv("MM_PROPERTY_TIMELINES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("MM_PROPERTY_TIMELINES=%q", v)
		}
		return n
	}
	return def
}

// TestProperty_SongPR_NeverExceedsCaps: seeded random lives of an agent and the operator over one lineage root with the real
// generation, spend, songpr and callbacks services on a manual clock (the clock only moves when the timeline moves it, so a
// failing seed replays exactly). Steps: agent and human estimate + create of Song PR renders and of plain variants of the root's
// takes, takes landing (the real take hook writes the callbacks), slow human verdicts, hours to days passing. After every step:
//
//   - no batch ceiling exceeds the batch cap, month (settled + outstanding) never exceeds the monthly cap, and each principal's
//     day never exceeds its own daily limit (the caps also hold while the lowered caps of the timeline refuse most creates);
//   - PatchMaxRenders holds for every patch: at most 3 live batches render it;
//   - the callback backlog paces agents: an agent estimate or create that involves the root is never accepted while the
//     unanswered callbacks of the root are at the limit (another refusal, such as the render limit, may answer first), and a
//     human is never refused by it.
func TestProperty_SongPR_NeverExceedsCaps(t *testing.T) {
	t.Parallel()
	n := propTimelines(t, 12)
	if testing.Short() && os.Getenv("MM_PROPERTY_TIMELINES") == "" {
		n = 4 // make check runs -short: a smaller sample, without the non-vacuity floor below
	}
	var seen capsTally
	t.Run("timelines", func(t *testing.T) {
		for seed := uint64(1); seed <= uint64(n); seed++ {
			t.Run("seed"+strconv.FormatUint(seed, 10), func(t *testing.T) {
				t.Parallel()
				runCapsTimeline(t, seed, &seen)
			})
		}
	})
	// the property is not vacuous: across the timelines creates succeeded, the backlog gate and the caps and the render limit all refused
	if n >= 12 {
		if seen.created.Load() == 0 || seen.gated.Load() == 0 || seen.capped.Load() == 0 || seen.renderLimit.Load() == 0 || seen.humanBeatGate.Load() == 0 {
			t.Errorf("a refusal never happened: created %d, backlog %d, caps %d, render limit %d, human past the gate %d",
				seen.created.Load(), seen.gated.Load(), seen.capped.Load(), seen.renderLimit.Load(), seen.humanBeatGate.Load())
		}
	}
}

// capsTally counts what the timelines exercised.
type capsTally struct{ created, gated, capped, renderLimit, humanBeatGate atomic.Int64 }

func runCapsTimeline(t *testing.T, seed uint64, seen *capsTally) {
	t.Helper()
	rng := mrand.New(mrand.NewPCG(seed, 0x5eed))
	r := newRig(t)
	ctx := context.Background()
	clock := lt.FixedNow
	now := func() time.Time { return clock }

	caps := spend.Caps{MonthlyMicro: 2_000_000 + money.MicroUSD(rng.IntN(4_000_000)), BatchMicro: 500_000 + money.MicroUSD(rng.IntN(600_000)),
		HumanDailyMicro: 1_000_000 + money.MicroUSD(rng.IntN(1_500_000)), AgentDailyMicro: 600_000 + money.MicroUSD(rng.IntN(700_000)), AutoApproveTakes: 8, AutoApproveMicro: 5_000_000}
	r.exec(`UPDATE spend_caps SET monthly_micro_usd = ?, batch_micro_usd = ?, human_daily_micro_usd = ?, agent_daily_micro_usd = ?, auto_approve_takes = ?, auto_approve_micro_usd = ? WHERE id = 1`,
		int64(caps.MonthlyMicro), int64(caps.BatchMicro), int64(caps.HumanDailyMicro), int64(caps.AgentDailyMicro), caps.AutoApproveTakes, int64(caps.AutoApproveMicro))

	svcSpend := spend.New(spend.Options{Store: r.st, Now: now, NewID: r.id, ConfirmKey: []byte("test-key"), Location: time.UTC, Rand: rand.Reader})
	cb := callbacks.New(callbacks.Options{Store: r.st, Now: now, NewID: r.id, BacklogMax: 6})
	lib := library.New(r.st, nil, now)
	lib.UseIDFunc(r.id)
	sp := songpr.New(songpr.Options{Store: r.st, Now: now, NewID: r.id})
	gen := generation.New(generation.Options{
		Store: r.st, Library: lib, Lineage: lineage.New(lineage.Options{Store: r.st, Now: now}), Spend: svcSpend, Engines: registry.Default(registry.Options{}),
		Gate:   provider.NewGate(r.st.R, provider.GateConfig{Mode: provider.ModeFake, KeysLoaded: true, ColdStartS: 180, MaxRuntime: 15 * time.Minute}, now),
		Notify: notify.Nop{}, PublicURL: "http://mm.example.com", Now: now, NewID: r.id, ColdStartS: 180, KillSlackS: 15, MaxRuntime: 15 * time.Minute,
		PatchSource: sp, BacklogGate: cb,
	})
	r.exec(`INSERT INTO runner_lease(id, owner, acquired_at, heartbeat_at, expires_at) VALUES (1, 'runner-1', ?, ?, ?)`,
		store.FormatTime(clock), store.FormatTime(clock), store.FormatTime(clock.AddDate(5, 0, 0)))

	g, cm := r.fx.Lineage, r.fx.Comments
	root := g[0]
	patches := []string{
		r.propose(agent, g[2], []string{cm["fix_chorus"]}, replace(1, 0, "line one")).ID,
		r.propose(agent, g[2], []string{cm["fix_verse"]}, replace(0, 0, "line two")).ID,
		r.propose(human, g[0], []string{cm["fix_chorus"], cm["fix_verse"]}, replace(1, 1, "line three"), replace(0, 1, "line four")).ID,
	}
	var landed [2]bool // G1 and G2 take their callbacks at most once each (the hook is idempotent per child and comment)
	var live []liveJob

	invariants := func(step string) {
		t.Helper()
		var maxCeil int64
		if err := r.st.R.QueryRow(`SELECT COALESCE(MAX(ceiling_micro_usd), 0) FROM batches`).Scan(&maxCeil); err != nil {
			t.Fatal(err)
		}
		if maxCeil > int64(caps.BatchMicro) {
			t.Fatalf("%s: a batch ceiling %d exceeds the batch cap %d", step, maxCeil, caps.BatchMicro)
		}
		for _, who := range []auth.Principal{agent, human} {
			u, err := svcSpend.Usage(ctx, r.st.R, who.Key(), now())
			if err != nil {
				t.Fatal(err)
			}
			if used := u.MonthSettled + u.Outstanding; used > caps.MonthlyMicro {
				t.Fatalf("%s: month used %d exceeds the cap %d", step, used, caps.MonthlyMicro)
			}
			if day := u.PrincipalDaySettled + u.PrincipalOutstanding; day > spend.PrincipalDaily(who, spend.TokenLimits{}, caps) {
				t.Fatalf("%s: %s day used %d exceeds %d", step, who.Kind, day, spend.PrincipalDaily(who, spend.TokenLimits{}, caps))
			}
		}
		for _, p := range patches {
			c, err := sp.RenderCount(ctx, r.st.R, p)
			if err != nil || c > songpr.PatchMaxRenders {
				t.Fatalf("%s: patch %s has %d renders (%v), limit %d", step, p, c, err, songpr.PatchMaxRenders)
			}
		}
	}

	codeOf := func(err error) string {
		if err == nil {
			return ""
		}
		var pe *problem.Error
		if errors.As(err, &pe) {
			return pe.Code
		}
		t.Fatalf("not a problem: %v", err)
		return ""
	}

	for step := 0; step < 60; step++ {
		who := agent
		if rng.IntN(3) == 0 {
			who = human
		}
		via := spend.ViaAPI
		if who.Kind == auth.KindHuman {
			via = spend.ViaHumanCreate
		}
		label := fmt.Sprintf("seed %d step %d", seed, step)
		switch a := rng.IntN(10); {
		case a < 5: // estimate + create
			var item generation.Item
			if rng.IntN(4) == 0 {
				parent := g[rng.IntN(3)]
				item = generation.Item{Mode: lineage.ModeNewSeed, ParentTrackID: &parent, NSeeds: 1 + rng.IntN(2)}
			} else {
				pid := patches[rng.IntN(len(patches))]
				item = generation.Item{Mode: lineage.ModeSongPR, PatchID: &pid, NSeeds: 1 + rng.IntN(4)}
			}
			pending, err := cb.Backlog(ctx, r.st.R, root)
			if err != nil {
				t.Fatal(err)
			}
			gated := who.Kind == auth.KindAgent && pending >= cb.Limit()
			est, err := gen.Estimate(ctx, who, generation.EstimateIn{Items: []generation.Item{item}, Via: via})
			if c := codeOf(err); gated && err == nil {
				t.Fatalf("%s: agent estimate at backlog %d/%d was accepted", label, pending, cb.Limit())
			} else if !gated && c == "CALLBACKS_BACKLOG" {
				t.Fatalf("%s: %s estimate at backlog %d/%d was refused by the gate", label, who.Kind, pending, cb.Limit())
			}
			switch codeOf(err) {
			case "CALLBACKS_BACKLOG":
				seen.gated.Add(1)
			case "PATCH_RENDER_LIMIT":
				seen.renderLimit.Add(1)
			}
			if err == nil {
				// the backlog may grow between estimate and create (a take lands): re-read it right before
				if rng.IntN(3) == 0 {
					landOne(t, r, cb, g, patches, &landed, &live, clock)
				}
				pending, _ = cb.Backlog(ctx, r.st.R, root)
				gated = who.Kind == auth.KindAgent && pending >= cb.Limit()
				c, err := gen.Create(ctx, who, generation.CreateIn{Token: est.Token, Via: via, Rationale: "property"}, encodeStub)
				if code := codeOf(err); gated && err == nil {
					t.Fatalf("%s: agent create at backlog %d/%d was accepted", label, pending, cb.Limit())
				} else if !gated && code == "CALLBACKS_BACKLOG" {
					t.Fatalf("%s: %s create at backlog %d/%d was refused by the gate", label, who.Kind, pending, cb.Limit())
				}
				switch code := codeOf(err); {
				case err == nil:
					seen.created.Add(1)
					live = append(live, liveJobs(t, r, c.BatchID)...)
				case code == "CALLBACKS_BACKLOG":
					seen.gated.Add(1)
				case code == "BUDGET_EXCEEDED":
					seen.capped.Add(1)
				case code == "PATCH_RENDER_LIMIT":
					seen.renderLimit.Add(1)
				}
				if who.Kind == auth.KindHuman && pending >= cb.Limit() && err == nil {
					seen.humanBeatGate.Add(1)
				}
			}
		case a < 7: // a take lands
			landOne(t, r, cb, g, patches, &landed, &live, clock)
		case a < 9: // a slow human answer
			ids := pendingCallbacks(t, r)
			if len(ids) > 0 {
				id := ids[rng.IntN(len(ids))]
				verdict := []string{"fixed", "not_yet", "worse", "kept", "lost"}[rng.IntN(5)]
				if _, err := cb.Submit(ctx, human, id, callbacks.VerdictInput{Verdict: verdict}); err != nil {
					if c := codeOf(err); c != "VALIDATION_FAILED" { // a fix verdict on a keep callback and the reverse
						t.Fatalf("%s: submit %s: %v", label, verdict, err)
					}
				}
			}
		default: // time passes: hours, sometimes days or a month boundary
			clock = clock.Add(time.Duration(1+rng.IntN(40)) * time.Hour)
			if rng.IntN(8) == 0 {
				clock = clock.AddDate(0, 0, 10+rng.IntN(25))
			}
		}
		invariants(label)
	}
}

type liveJob struct{ id, batch, patch string }

func liveJobs(t *testing.T, r *rig, batch string) []liveJob {
	t.Helper()
	rows, err := r.st.R.Query(`SELECT id, batch_id, COALESCE(patch_id, '') FROM jobs WHERE batch_id = ? ORDER BY take_index`, batch)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []liveJob
	for rows.Next() {
		var j liveJob
		if err := rows.Scan(&j.id, &j.batch, &j.patch); err != nil {
			t.Fatal(err)
		}
		out = append(out, j)
	}
	return out
}

// landOne runs the real take hook for a job of a Song PR batch: the child is G1 or G2 of the lineage (the first that has not
// landed), so at most the six callbacks of the fixture exist (the hook is idempotent per child and comment).
func landOne(t *testing.T, r *rig, cb *callbacks.Service, g [3]string, _ []string, landed *[2]bool, live *[]liveJob, clock time.Time) {
	t.Helper()
	slot := -1
	for i, done := range landed {
		if !done {
			slot = i
			break
		}
	}
	var pick *liveJob
	for i := range *live {
		if (*live)[i].patch != "" {
			pick = &(*live)[i]
			break
		}
	}
	if slot < 0 || pick == nil {
		return
	}
	patch := pick.patch
	tx, err := r.st.W.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = cb.AfterTakeTx(context.Background(), tx, jobs.TakeInfo{TrackID: g[slot+1], JobID: pick.id, BatchID: pick.batch, PatchID: &patch,
		Fence: store.LeaseFence{Owner: "runner-1", Now: clock}})
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("take hook: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	landed[slot] = true
	*live = (*live)[1:]
}

func pendingCallbacks(t *testing.T, r *rig) []string {
	t.Helper()
	rows, err := r.st.R.Query(`SELECT c.id FROM callbacks c WHERE NOT EXISTS (SELECT 1 FROM callback_verdicts v WHERE v.callback_id = c.id AND v.source = 'human') ORDER BY c.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}
