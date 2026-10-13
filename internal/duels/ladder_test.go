package duels_test

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/duels/duelstest"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/clip"
)

var update = flag.Bool("update", false, "rewrite golden files")

func (e *env) ladder(t *testing.T, q duels.LadderQuery) duels.LadderPage {
	t.Helper()
	p, err := e.svc.Ladder(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func item(t *testing.T, p duels.LadderPage, id string) duels.LadderItem {
	t.Helper()
	for _, it := range p.Items {
		if it.TrackID == id {
			return it
		}
	}
	t.Fatalf("track %s not on the ladder %+v", id, p.Items)
	return duels.LadderItem{}
}

func TestLadder_Counts(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// fixture: one blind human verdict (a on Decided: Beta beats Alpha), one sighted device tie, one critic verdict
	p := e.ladder(t, duels.LadderQuery{})
	wantCounts := duels.LadderCounts{VerdictsHumanCounted: 1, VerdictsCriticExcluded: 1, VerdictsSightedExcluded: 1, Tracks: 2}
	if diff := cmp.Diff(wantCounts, p.Counts); diff != "" {
		t.Errorf("counts (-want +got):\n%s", diff)
	}
	if p.Diagnostics.NAB != 1 || p.Diagnostics.SlotAWinRate == nil || *p.Diagnostics.SlotAWinRate != 1 {
		t.Errorf("diagnostics = %+v", p.Diagnostics)
	}
	if p.Scope != "global" || p.RootTrackID != nil || p.IncludeSighted || p.Total != 2 || p.NextCursor != nil || p.GeneratedAt != "2026-10-07T12:00:00Z" && p.GeneratedAt == "" {
		t.Errorf("page = %+v", p)
	}
	m := p.Model
	if m.Kind != "btd" || m.PriorSD != 1.5 || m.NuPrior != "gamma(2,2)" || m.Anchor != "reject" || m.AnchorU != 0 || !m.Converged || m.Nu <= 0 {
		t.Errorf("model = %+v", m)
	}
	beta, alpha := p.Items[0], p.Items[1]
	if beta.TrackID != duelstest.TrackBeta || beta.Rank != 1 || beta.Wins != 1 || beta.NDuels != 1 || !beta.Provisional || beta.Title != duelstest.TitleBeta ||
		beta.Seed != duelstest.SeedBeta || beta.RootTrackID != duelstest.TrackAlpha {
		t.Errorf("beta = %+v", beta)
	}
	if alpha.Rank != 2 || alpha.Losses != 1 || alpha.U >= beta.U {
		t.Errorf("alpha = %+v", alpha)
	}
	if lo, hi := beta.CI95[0], beta.CI95[1]; lo >= beta.U || hi <= beta.U {
		t.Errorf("ci = %v around %v", beta.CI95, beta.U)
	}
	// both_bad and ties show in the counts
	d := e.create(t, human, lt.TrackTwinA, lt.TrackTwinB, true)
	e.judge(t, human, d.ID, "both_bad")
	d = e.create(t, human, lt.TrackTwinA, lt.TrackTwinB, true)
	e.judge(t, human, d.ID, "tie")
	p = e.ladder(t, duels.LadderQuery{})
	if p.Counts.BothBad != 1 || p.Counts.Ties != 1 || p.Counts.VerdictsHumanCounted != 3 || p.Counts.Tracks != 4 {
		t.Errorf("counts = %+v", p.Counts)
	}
	if it := item(t, p, lt.TrackTwinA); it.BothBad != 1 || it.Ties != 1 || it.NDuels != 2 {
		t.Errorf("twin a = %+v", it)
	}
}

func TestLadder_SightedExcludedByDefault(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	def := e.ladder(t, duels.LadderQuery{})
	with := e.ladder(t, duels.LadderQuery{IncludeSighted: true})
	if def.Counts.VerdictsSightedExcluded != 1 || with.Counts.VerdictsSightedExcluded != 0 {
		t.Errorf("excluded: default %d, included %d", def.Counts.VerdictsSightedExcluded, with.Counts.VerdictsSightedExcluded)
	}
	if with.Counts.VerdictsHumanCounted != 2 || with.Counts.Ties != 1 || !with.IncludeSighted {
		t.Errorf("with sighted = %+v", with.Counts)
	}
	if cmp.Equal(def.Items, with.Items) {
		t.Error("the sighted tie must change the fit")
	}
}

func TestLadder_DeviceVerdictCounted(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	before := e.ladder(t, duels.LadderQuery{})
	r := e.judge(t, device, duelstest.DuelOpen, "a")
	if r.Verdict.Source != "human" || r.Verdict.PrincipalKind != "device" {
		t.Fatalf("verdict = %+v", r.Verdict)
	}
	after := e.ladder(t, duels.LadderQuery{})
	if after.Counts.VerdictsHumanCounted != before.Counts.VerdictsHumanCounted+1 {
		t.Errorf("counted %d -> %d", before.Counts.VerdictsHumanCounted, after.Counts.VerdictsHumanCounted)
	}
}

func TestLadder_CacheInvalidatedByVerdict(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.ladder(t, duels.LadderQuery{})
	if b := e.ladder(t, duels.LadderQuery{}); !cmp.Equal(a, b) {
		t.Error("an unchanged ladder must repeat")
	}
	e.win(t, human, e.create(t, human, duelstest.TrackAlpha, duelstest.TrackBeta, true).ID, duelstest.TrackAlpha)
	c := e.ladder(t, duels.LadderQuery{})
	if c.Counts.VerdictsHumanCounted != 2 || cmp.Equal(a.Items, c.Items) {
		t.Errorf("verdict not reflected: %+v", c.Counts)
	}
	// the lineage cache entry follows too
	l1 := e.ladder(t, duels.LadderQuery{Scope: "lineage", RootTrackID: duelstest.TrackAlpha})
	e.win(t, human, e.create(t, human, duelstest.TrackAlpha, duelstest.TrackBeta, true).ID, duelstest.TrackAlpha)
	if l2 := e.ladder(t, duels.LadderQuery{Scope: "lineage", RootTrackID: duelstest.TrackAlpha}); l2.Counts.VerdictsHumanCounted != l1.Counts.VerdictsHumanCounted+1 {
		t.Errorf("lineage counts %d -> %d", l1.Counts.VerdictsHumanCounted, l2.Counts.VerdictsHumanCounted)
	}
}

func TestLadder_TitlesNotCached(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if got := item(t, e.ladder(t, duels.LadderQuery{}), duelstest.TrackBeta).Title; got != duelstest.TitleBeta {
		t.Fatalf("title = %q", got)
	}
	if _, err := e.st.W.Exec(`UPDATE tracks SET title = 'Renamed Beta' WHERE id = ?`, duelstest.TrackBeta); err != nil {
		t.Fatal(err)
	}
	for _, q := range []duels.LadderQuery{{}, {Scope: "lineage", RootTrackID: duelstest.TrackAlpha}} {
		if got := item(t, e.ladder(t, q), duelstest.TrackBeta).Title; got != "Renamed Beta" {
			t.Errorf("%s: title = %q after rename", q.Scope, got)
		}
	}
}

func TestLadder_ScopeLineageVsGlobal(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// a cross-lineage duel counts globally only
	d := e.create(t, human, duelstest.TrackAlpha, lt.TrackTwinA, true)
	e.win(t, human, d.ID, lt.TrackTwinA)
	g := e.ladder(t, duels.LadderQuery{})
	if g.Counts.Tracks != 3 || g.Counts.VerdictsHumanCounted != 2 {
		t.Errorf("global = %+v", g.Counts)
	}
	l := e.ladder(t, duels.LadderQuery{Scope: "lineage", RootTrackID: duelstest.TrackAlpha})
	if l.Scope != "lineage" || l.RootTrackID == nil || *l.RootTrackID != duelstest.TrackAlpha || l.Counts.VerdictsHumanCounted != 1 || l.Total != 2 {
		t.Errorf("lineage = %+v", l)
	}
	// the other lineage has two tracks and no lineage-internal duels: both zero-duel, provisional
	o := e.ladder(t, duels.LadderQuery{Scope: "lineage", RootTrackID: lt.TrackTwinA})
	if o.Total != 2 || o.Counts.VerdictsHumanCounted != 0 {
		t.Fatalf("other lineage = %+v", o)
	}
	for _, it := range o.Items {
		if it.U != 0 || it.SD != 1.5 || !it.Provisional || it.NDuels != 0 {
			t.Errorf("zero-duel item = %+v", it)
		}
	}
	// critic and sighted exclusions count within the lineage only
	if l.Counts.VerdictsCriticExcluded != 1 {
		t.Errorf("critic excluded = %d", l.Counts.VerdictsCriticExcluded)
	}
	// errors
	_, err := e.svc.Ladder(context.Background(), duels.LadderQuery{Scope: "lineage"})
	asProblem(t, err, 422, "VALIDATION_FAILED")
	_, err = e.svc.Ladder(context.Background(), duels.LadderQuery{Scope: "lineage", RootTrackID: "nope"})
	asProblem(t, err, 404, "NOT_FOUND")
	_, err = e.svc.Ladder(context.Background(), duels.LadderQuery{Scope: "x"})
	asProblem(t, err, 422, "VALIDATION_FAILED")
	// paging
	var got []string
	cursor := ""
	for range 5 {
		pg := e.ladder(t, duels.LadderQuery{Limit: 1, Cursor: cursor})
		if pg.Total != 3 || len(pg.Items) > 1 {
			t.Fatalf("page = %+v", pg)
		}
		for _, it := range pg.Items {
			got = append(got, it.TrackID)
		}
		if pg.NextCursor == nil {
			break
		}
		cursor = *pg.NextCursor
	}
	if len(got) != 3 {
		t.Errorf("paged %v", got)
	}
	if _, err := e.svc.Ladder(context.Background(), duels.LadderQuery{Cursor: "%%"}); err == nil {
		t.Error("bad cursor accepted")
	}
}

func TestLadderInputs_HumanOnly(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	before := e.ladder(t, duels.LadderQuery{})
	beforeL := e.ladder(t, duels.LadderQuery{Scope: "lineage", RootTrackID: duelstest.TrackAlpha})
	for i, v := range []string{"a", "b", "tie", "both_bad"} {
		d := e.create(t, agent, duelstest.TrackAlpha, duelstest.TrackBeta, true)
		e.judge(t, agent, d.ID, v)
		d = e.create(t, agent, duelstest.TrackAlpha, lt.TrackTwinB, false)
		e.judge(t, agent2, d.ID, v)
		_ = i
	}
	after := e.ladder(t, duels.LadderQuery{})
	afterL := e.ladder(t, duels.LadderQuery{Scope: "lineage", RootTrackID: duelstest.TrackAlpha})
	for _, p := range []*duels.LadderPage{&before, &after, &beforeL, &afterL} {
		p.Counts.VerdictsCriticExcluded = 0
	}
	if diff := cmp.Diff(before, after); diff != "" {
		t.Errorf("critic verdicts changed the global ladder:\n%s", diff)
	}
	if diff := cmp.Diff(beforeL, afterL); diff != "" {
		t.Errorf("critic verdicts changed the lineage ladder:\n%s", diff)
	}
}

func TestLadder_Golden(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// a fixed small tournament across both fixture lineages; the first coin value is false throughout
	for _, c := range []struct{ a, b, winner string }{
		{duelstest.TrackAlpha, duelstest.TrackBeta, duelstest.TrackAlpha},
		{duelstest.TrackAlpha, lt.TrackTwinA, lt.TrackTwinA},
		{duelstest.TrackBeta, lt.TrackTwinA, lt.TrackTwinA},
		{lt.TrackTwinA, lt.TrackTwinB, ""},
	} {
		d := e.create(t, human, c.a, c.b, true)
		if c.winner == "" {
			e.judge(t, human, d.ID, "both_bad")
		} else {
			e.win(t, human, d.ID, c.winner)
		}
	}
	got, err := json.MarshalIndent(e.ladder(t, duels.LadderQuery{}), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "ladder.golden.json")
	if *update {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("ladder differs from %s (-want +got):\n%s", path, diff)
	}
}

func TestPairStats(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	// fixture: Decided (A=Beta, human "a" => Beta wins; critic "b" => Alpha wins), Sighted (device tie)
	ps, err := e.svc.PairStats(ctx, duelstest.TrackAlpha, duelstest.TrackBeta)
	if err != nil {
		t.Fatal(err)
	}
	if ps.Human.N != 2 || ps.Human.BWins != 1 || ps.Human.Ties != 1 || ps.Human.AWins != 0 || ps.HumanBlindN != 1 {
		t.Errorf("human = %+v blind %d", ps.Human, ps.HumanBlindN)
	}
	if ps.Critic.N != 1 || ps.Critic.AWins != 1 || ps.Critic.BWins != 0 {
		t.Errorf("critic = %+v", ps.Critic)
	}
	// orientation flips with the arguments
	rev, _ := e.svc.PairStats(ctx, duelstest.TrackBeta, duelstest.TrackAlpha)
	if rev.Human.AWins != 1 || rev.Human.BWins != 0 || rev.Critic.BWins != 1 || rev.Critic.AWins != 0 {
		t.Errorf("reversed = %+v", rev)
	}
	// duels with other tracks do not count; both_bad is its own bucket
	d := e.create(t, human, duelstest.TrackAlpha, lt.TrackTwinA, true)
	e.judge(t, human, d.ID, "a")
	d = e.create(t, human, duelstest.TrackBeta, duelstest.TrackAlpha, false)
	e.judge(t, human, d.ID, "both_bad")
	ps, _ = e.svc.PairStats(ctx, duelstest.TrackAlpha, duelstest.TrackBeta)
	if ps.Human.N != 3 || ps.Human.BothBad != 1 || ps.HumanBlindN != 1 {
		t.Errorf("after = %+v blind %d", ps.Human, ps.HumanBlindN)
	}
	if none, _ := e.svc.PairStats(ctx, lt.TrackLuna, lt.TrackCitta); none.Human.N != 0 || none.Critic.N != 0 {
		t.Errorf("none = %+v", none)
	}
}

func TestStrengths_GlobalSubset(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	d := e.create(t, human, duelstest.TrackAlpha, lt.TrackTwinA, true)
	e.win(t, human, d.ID, lt.TrackTwinA)
	full := e.ladder(t, duels.LadderQuery{})
	got, nu, err := e.svc.Strengths(ctx, []string{duelstest.TrackAlpha, lt.TrackTwinA, lt.TrackLuna, "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || nu != full.Model.Nu {
		t.Fatalf("strengths = %v nu %v (want %v)", got, nu, full.Model.Nu)
	}
	for _, id := range []string{duelstest.TrackAlpha, lt.TrackTwinA} {
		it := item(t, full, id)
		if s := got[id]; s.U != it.U || s.SD != it.SD || s.NDuels != it.NDuels {
			t.Errorf("%s: %+v vs item %+v", id, s, it)
		}
	}
	if _, ok := got[lt.TrackLuna]; ok {
		t.Error("a track without counted duels must be absent")
	}
}

func TestOpenClipKeys(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	keys, err := e.svc.OpenClipKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	refs, _ := e.lib.TrackRefs(ctx, []string{duelstest.TrackAlpha, duelstest.TrackBeta})
	spec := func(id string, cdb int) clip.Spec {
		r := refs[id]
		return clip.Spec{MasterSHA256: r.MasterSHA256, MasterPath: r.MasterPath, SampleRate: r.SampleRate, Channels: r.Channels,
			W: clip.Window{StartMS: 16300, EndMS: 61300}, GainCDB: cdb}
	}
	want := map[string]struct{}{
		clip.Key(spec(duelstest.TrackAlpha, duelstest.GainAcdB), "ffmpeg fake"): {},
		clip.Key(spec(duelstest.TrackBeta, duelstest.GainBcdB), "ffmpeg fake"):  {},
	}
	if diff := cmp.Diff(want, keys); diff != "" {
		t.Errorf("keys (-want +got):\n%s", diff)
	}
	// deciding the duel unpins it; expired duels never pinned; a new one adds its keys
	e.judge(t, human, duelstest.DuelOpen, "a")
	if keys, _ = e.svc.OpenClipKeys(ctx); len(keys) != 0 {
		t.Errorf("decided duel still pinned: %v", keys)
	}
	skip := len(e.rend.specs())
	e.create(t, human, duelstest.TrackAlpha, duelstest.TrackBeta, true)
	keys, _ = e.svc.OpenClipKeys(ctx)
	if len(keys) != 2 {
		t.Errorf("new duel keys = %d", len(keys))
	}
	for _, o := range e.rend.specs()[skip:] {
		if _, ok := keys[clip.Key(o, "ffmpeg fake")]; !ok {
			t.Errorf("rendered clip %+v is not pinned", o)
		}
	}
	// the current tool version is used: a different renderer version gives different keys
	other := newEnv(t, func(o *duels.Options) { o.Clips = toolOverride{e.rend, "ffmpeg other"} })
	okeys, _ := other.svc.OpenClipKeys(ctx)
	for k := range okeys {
		if _, same := keys[k]; same {
			t.Error("keys must depend on the tool version")
		}
	}
}

type toolOverride struct {
	*spyRenderer
	tool string
}

func (t toolOverride) ToolVersion() string { return t.tool }
