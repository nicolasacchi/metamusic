package criticbench_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/criticbench"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

func TestBench_Empty(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.bench(criticbench.Query{IncludeUnknown: true})
	if b.Critics == nil || len(b.Critics) != 0 || b.Window.Since != nil || b.Window.RatingPairs != 0 {
		t.Fatalf("%+v", b)
	}
	if b.Rule.MinN != 30 || b.Rule.TrustedLower != 0.55 || b.Rule.ContraryUpper != 0.45 || b.Note != criticbench.Note {
		t.Errorf("rule/note: %+v", b)
	}
}

// Tier A critics exist only when analyses rows do; the latest row per take wins; the direction is a hypothesis.
func TestBench_TierACritics(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	lines := e.lineages(12, 4) // 12 lineages x 6 pairs
	// a stale row must lose against the newer one for the same take
	e.analysis(lines[0][0], "loudness", "mm-loudness/0 ffmpeg n9", `{"true_peak_dbtp":-50,"lra_lu":5}`)
	for _, l := range lines {
		for k, id := range l {
			// true peak falls as stars rise: the hypothesis (lower is better) holds for every pair
			e.analysis(id, "loudness", "mm-loudness/1 ffmpeg n9", fmt.Sprintf(`{"true_peak_dbtp":%d,"lra_lu":5}`, 10-3*k))
		}
	}
	b := e.bench(criticbench.Query{IncludeUnknown: true})
	tp := find(b, "tier_a.true_peak")
	if tp == nil {
		t.Fatalf("no true peak critic: %+v", b.Critics)
	}
	if find(b, "tier_a.bandwidth") != nil || find(b, "tier_a.energy") != nil {
		t.Error("a critic without any analyses row must be absent")
	}
	if tp.Direction == nil || *tp.Direction != -1 || *tp.MinDelta != 0.5 || tp.Kind != "metric" || tp.Tier != "A" {
		t.Errorf("definition: %+v", tp)
	}
	r := tp.Scores.Ratings
	if r == nil || r.Pairs != 72 || r.Agree != 72 || r.Rate != 1 {
		t.Fatalf("ratings score: %+v", r)
	}
	if tp.Scores.Lines != nil || tp.Scores.Duels == nil || tp.Scores.Duels.Pairs != 0 {
		t.Errorf("scores: %+v", tp.Scores)
	}
	// 12 lineages x 4 takes = 48 distinct takes, 72 pairs => NEff 48: trusted
	if r.NEff != 48 || tp.State != criticbench.StateTrusted {
		t.Errorf("NEff %d state %s", r.NEff, tp.State)
	}
	lra := find(b, "tier_a.loudness_range")
	if lra == nil || lra.Scores.Ratings.Predicted != 0 || lra.State != criticbench.StateInsufficient {
		t.Errorf("a constant metric makes no call: %+v", lra)
	}
	if len(tp.Scores.Ratings.Disagreements) != 0 {
		t.Error("disagreements only when a critic is requested")
	}
}

func TestBench_TierAExtractors(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	root := e.take("")
	other := e.take(root)
	e.ownerStars(root, 2)
	e.ownerStars(other, 4)
	for tk, v := range map[string]float64{root: 1, other: 9} {
		e.analysis(tk, "spectrum", "s", fmt.Sprintf(`{"bandwidth_hz":%g}`, v*1000))
		e.analysis(tk, "clipping", "c", fmt.Sprintf(`{"clipped_s":%g}`, v))
		e.analysis(tk, "silence", "s", fmt.Sprintf(`{"internal_s":%g}`, v))
		e.analysis(tk, "energy", "e", fmt.Sprintf(`{"energy":%g}`, v/10))
		e.analysis(tk, "token_loops", "t", fmt.Sprintf(`{"streams":[{"longest_repeat":{"s":%g}},{"longest_repeat":{"s":%g}}]}`, v, v+1))
	}
	e.analysis(root, "loudness", "l", `"oops"`)        // unreadable rows are skipped
	e.analysis(other, "loudness", "l", `{"lra_lu":3}`) // a row without the true_peak key
	b := e.bench(criticbench.Query{IncludeUnknown: true})
	if find(b, "tier_a.true_peak") != nil {
		t.Error("a row lacking the metric key must not create the critic")
	}
	for _, id := range []string{"tier_a.bandwidth", "tier_a.clipped_s", "tier_a.silence_internal_s", "tier_a.energy", "tier_a.token_loop_s", "tier_a.true_peak"} {
		c := find(b, id)
		if c == nil && id == "tier_a.true_peak" {
			continue
		}
		if c == nil {
			t.Fatalf("%s absent", id)
		}
		if id == "tier_a.true_peak" {
			t.Fatalf("a critic whose only row is unreadable must be absent: %+v", c)
		}
		if c.Scores.Ratings.Pairs != 1 || c.Scores.Ratings.Predicted != 1 {
			t.Errorf("%s: %+v", id, c.Scores.Ratings)
		}
		// the higher-rated take has the larger value; direction +1 agrees, -1 does not
		wantAgree := 0
		if *c.Direction > 0 {
			wantAgree = 1
		}
		if c.Scores.Ratings.Agree != wantAgree {
			t.Errorf("%s: agree %d want %d", id, c.Scores.Ratings.Agree, wantAgree)
		}
	}
}

func TestRatingsReference_OwnerOnlyWithinLineage(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.take("")
	b := e.take(a)
	c := e.take("") // another lineage
	e.ownerStars(a, 5)
	e.ownerStars(b, 1)
	e.ownerStars(c, 3)
	e.bobStars(a, 1) // another user's rating is not the reference
	e.bobStars(b, 5)
	e.agentStars(c, "t1", 5)
	bn := e.bench(criticbench.Query{IncludeUnknown: true})
	if bn.Window.RatingPairs != 1 || bn.Window.Since == nil {
		t.Fatalf("window %+v", bn.Window)
	}
	e.analysis(a, "energy", "e", `{"energy":0.9}`)
	e.analysis(b, "energy", "e", `{"energy":0.1}`)
	e.analysis(c, "energy", "e", `{"energy":0.5}`)
	en := find(e.bench(criticbench.Query{IncludeUnknown: true}), "tier_a.energy")
	if en.Scores.Ratings.Pairs != 1 || en.Scores.Ratings.Agree != 1 {
		t.Errorf("%+v", en.Scores.Ratings)
	}
}

func TestBench_DuelsReferenceBlindOnly(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	root := e.take("")
	mk := func() (string, string) { return e.take(root), e.take(root) }
	// blind, the Owner chose a
	a1, b1 := mk()
	d1 := e.duel(a1, b1, true)
	e.verdict(d1, a1, b1, "human", "human:alice", "a", false)
	// open (not blind): excluded
	a2, b2 := mk()
	d2 := e.duel(a2, b2, false)
	e.verdict(d2, a2, b2, "human", "human:alice", "a", false)
	// blind but the Owner saw the reveal: excluded
	a3, b3 := mk()
	d3 := e.duel(a3, b3, true)
	e.verdict(d3, a3, b3, "human", "human:alice", "b", true)
	// a tie is not a pair
	a4, b4 := mk()
	d4 := e.duel(a4, b4, true)
	e.verdict(d4, a4, b4, "human", "human:alice", "tie", false)
	// an unjudged duel
	a5, b5 := mk()
	e.duel(a5, b5, true)
	// an agent critic that judged duels 1 and 2 (the second is outside the reference)
	for _, d := range [][3]string{{d1, a1, b1}, {d2, a2, b2}} {
		e.verdict(d[0], d[1], d[2], "critic", "agent:cr", "b", false)
	}
	b := e.bench(criticbench.Query{IncludeUnknown: true})
	if b.Window.DuelPairs != 1 {
		t.Fatalf("duel pairs %d, want 1 (blind, unrevealed, decided)", b.Window.DuelPairs)
	}
	c := find(b, "agent.duels:agent:cr")
	if c == nil || c.Scores.Duels.Pairs != 1 || c.Scores.Duels.Predicted != 1 || c.Scores.Duels.Agree != 0 {
		t.Fatalf("%+v", c)
	}
	if c.Scores.Ratings != nil || c.State != criticbench.StateInsufficient || c.Kind != "pairwise" {
		t.Errorf("%+v", c)
	}
	if c.SlotBias == nil || c.SlotBias.N != 2 || c.SlotBias.Share != 0 || c.SlotBias.Biased {
		t.Errorf("slot bias %+v", c.SlotBias)
	}
	// a metric critic is compared on the duels reference too
	e.analysis(a1, "energy", "e", `{"energy":0.9}`)
	e.analysis(b1, "energy", "e", `{"energy":0.1}`)
	en := find(e.bench(criticbench.Query{IncludeUnknown: true}), "tier_a.energy")
	if en.Scores.Duels.Pairs != 1 || en.Scores.Duels.Agree != 1 {
		t.Errorf("%+v", en.Scores.Duels)
	}
}

func TestBench_AgentCriticsPerPrincipal(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	lines := e.lineages(20, 4)
	// agent 1 agrees with the Owner on every take; agent 2 inverts; a take rated by both goes to whoever rated last
	for _, l := range lines {
		for k, id := range l {
			e.agentStars(id, "good", k+1)
		}
	}
	for _, l := range lines[:10] {
		for k, id := range l {
			e.agentStars(id, "bad", 4-k)
		}
	}
	b := e.bench(criticbench.Query{IncludeUnknown: true})
	var ids []string
	for _, c := range b.Critics {
		ids = append(ids, c.ID)
	}
	if !strings.Contains(strings.Join(ids, ","), "agent.stars:agent:bad") || find(b, "agent.stars:agent:good") == nil {
		t.Fatalf("critics %v", ids)
	}
	good, bad := find(b, "agent.stars:agent:good"), find(b, "agent.stars:agent:bad")
	// "bad" rated last on 10 lineages (40 takes), which are now credited to it alone; "good" keeps the other 10 lineages
	if bad.Scores.Ratings.Pairs != 60 || bad.Scores.Ratings.Agree != 0 {
		t.Errorf("bad: %+v", bad.Scores.Ratings)
	}
	if good.Scores.Ratings.Pairs != 60 || good.Scores.Ratings.Agree != 60 {
		t.Errorf("good: %+v", good.Scores.Ratings)
	}
	if good.State != criticbench.StateTrusted || bad.State != criticbench.StateContrary {
		t.Errorf("states %s %s", good.State, bad.State)
	}
	if good.Tier != "agent" || good.Kind != "metric" || good.Direction == nil || *good.Direction != 1 {
		t.Errorf("%+v", good)
	}
	if !strings.Contains(good.Label, "good") {
		t.Errorf("label %q", good.Label)
	}
}

func TestBench_SlotBiasProbe(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	root := e.take("")
	for i := 0; i < 24; i++ {
		a, b := e.take(root), e.take(root)
		d := e.duel(a, b, true)
		e.verdict(d, a, b, "critic", "agent:lazy", "a", false) // always slot A
	}
	c := find(e.bench(criticbench.Query{IncludeUnknown: true}), "agent.duels:agent:lazy")
	if c == nil || c.SlotBias == nil || !c.SlotBias.Biased || c.SlotBias.N != 24 || c.SlotBias.Share != 1 {
		t.Fatalf("%+v", c)
	}
}

func TestBench_FilterAndDisagreements(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	lines := e.lineages(12, 4)
	for _, l := range lines {
		for k, id := range l {
			e.analysis(id, "energy", "e", fmt.Sprintf(`{"energy":%g}`, 1-0.2*float64(k))) // inverted: wrong every time
			e.analysis(id, "clipping", "c", `{"clipped_s":0}`)
		}
	}
	all := e.bench(criticbench.Query{IncludeUnknown: true})
	if got := find(all, "tier_a.energy").Scores.Ratings.Disagreements; len(got) != 0 {
		t.Errorf("unrequested disagreements %v", got)
	}
	one := e.bench(criticbench.Query{Critic: "tier_a.energy", IncludeUnknown: true})
	if len(one.Critics) != 1 || len(one.Critics[0].Scores.Ratings.Disagreements) != 10 {
		t.Fatalf("%+v", one.Critics)
	}
	// the cached bench was not mutated by either view
	if got := find(e.bench(criticbench.Query{IncludeUnknown: true}), "tier_a.energy").Scores.Ratings.Disagreements; len(got) != 0 {
		t.Errorf("view leaked disagreements %v", got)
	}
	// the constant critic says nothing
	if c := find(all, "tier_a.clipped_s"); c == nil || c.State != criticbench.StateInsufficient {
		t.Errorf("%+v", c)
	}
	known := e.bench(criticbench.Query{IncludeUnknown: false})
	if find(known, "tier_a.clipped_s") != nil || find(known, "tier_a.energy") == nil {
		t.Errorf("include_unknown=false must drop the critics that say nothing yet: %+v", known.Critics)
	}
	_, err := e.svc.Bench(context.Background(), criticbench.Query{Critic: "nope"})
	var pe *problem.Error
	if !errors.As(err, &pe) || pe.Status != 404 {
		t.Errorf("unknown critic: %v", err)
	}
}

func TestBench_GETNeverWrites(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.lineages(3, 3)
	count := func() string {
		var out []string
		rows, err := e.st.R.Query(`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for rows.Next() {
			var n string
			_ = rows.Scan(&n)
			names = append(names, n)
		}
		_ = rows.Close()
		for _, n := range names {
			var c int
			if err := e.st.R.QueryRow(`SELECT count(*) FROM "` + n + `"`).Scan(&c); err != nil {
				t.Fatal(err)
			}
			out = append(out, fmt.Sprint(n, "=", c))
		}
		return strings.Join(out, ",")
	}
	var v1, v2 int
	_ = e.st.R.QueryRow(`PRAGMA data_version`).Scan(&v1)
	before := count()
	e.bench(criticbench.Query{IncludeUnknown: true})
	_, _ = e.svc.Bench(context.Background(), criticbench.Query{Critic: "x"}) // an error path too
	if count() != before {
		t.Error("a table changed")
	}
	_ = e.st.R.QueryRow(`PRAGMA data_version`).Scan(&v2)
	if v1 != v2 {
		t.Error("data_version moved")
	}
}

type spySource struct {
	version string
	calls   int
	lines   int
}

func (s *spySource) Critics(context.Context, criticbench.Reader, *criticbench.Reference) ([]criticbench.Critic, error) {
	s.calls++
	c := criticbench.Critic{ID: "diction.spy", Label: "spy", Kind: criticbench.KindLabels, Tier: criticbench.TierDiction,
		Scores: criticbench.Scores{Lines: &criticbench.Score{State: criticbench.StateTrusted}}, State: criticbench.StateTrusted}
	return []criticbench.Critic{c}, nil
}
func (s *spySource) Version(context.Context) (string, error) { return s.version, nil }
func (s *spySource) Lines(context.Context) (int, error)      { return s.lines, nil }

// The answer is cached under (analyses, ratings, duel verdicts, source versions) and recomputed when any of them moves.
func TestBench_CacheKey(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	spy := &spySource{version: "v1", lines: 7}
	e.svc.Use(spy)
	root := e.take("")
	other := e.take(root)
	e.ownerStars(root, 3)
	q := criticbench.Query{IncludeUnknown: true}
	b := e.bench(q)
	e.bench(q)
	e.bench(criticbench.Query{Critic: "diction.spy"}) // other queries share the computation
	if spy.calls != 1 {
		t.Fatalf("recomputed %d times without a change", spy.calls)
	}
	if b.Window.Lines != 7 {
		t.Errorf("lines %d", b.Window.Lines)
	}
	if c := find(b, "diction.spy"); c == nil || c.State != criticbench.StateTrusted {
		t.Errorf("source critic missing: %+v", b.Critics)
	}
	steps := []func(){
		func() { e.ownerStars(other, 1) },                                                                        // ratings
		func() { e.analysis(root, "energy", "e", `{"energy":1}`) },                                               // analyses
		func() { d := e.duel(root, other, true); e.verdict(d, root, other, "human", "human:alice", "a", false) }, // duel verdicts
		func() { spy.version = "v2" },                                                                            // a source's version
	}
	for i, step := range steps {
		before := spy.calls
		step()
		e.bench(q)
		if spy.calls != before+1 {
			t.Errorf("step %d did not invalidate the cache", i)
		}
	}
}

func TestBench_SourcesSortedByTier(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.svc.Use(&spySource{version: "v"})
	r := e.take("")
	o := e.take(r)
	e.ownerStars(r, 2)
	e.ownerStars(o, 4)
	e.analysis(r, "energy", "e", `{"energy":1}`)
	e.analysis(o, "energy", "e", `{"energy":2}`)
	e.agentStars(r, "x", 3)
	b := e.bench(criticbench.Query{IncludeUnknown: true})
	var tiers []string
	for _, c := range b.Critics {
		tiers = append(tiers, c.Tier)
	}
	if got := strings.Join(tiers, ","); got != "A,agent,diction" {
		t.Errorf("order %s", got)
	}
}

// Rule pinned (amendment M6N-B1): a Tier A critic is listed only when a readable value exists for some take in the reference
// windows. Values on takes outside any pair, with no pairs at all, do not list it; the moment a pair exists it is listed.
func TestBench_TierAValuesWithoutPairsAbsent(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	root := e.take("")
	other := e.take(root)
	e.analysis(root, "energy", "e", `{"energy":0.2}`)
	e.analysis(other, "energy", "e", `{"energy":0.8}`)
	if b := e.bench(criticbench.Query{IncludeUnknown: true}); find(b, "tier_a.energy") != nil {
		t.Fatalf("values but no reference pairs must not list the critic: %+v", b.Critics)
	}
	e.ownerStars(root, 2)
	e.ownerStars(other, 4)
	c := find(e.bench(criticbench.Query{IncludeUnknown: true}), "tier_a.energy")
	if c == nil || c.Scores.Ratings.Pairs != 1 {
		t.Fatalf("with a pair the critic is listed: %+v", c)
	}
}
