package nightshift

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/ladder"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/mandates"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	os.Exit(m.Run())
}

var t0 = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

type fakeStrengths struct {
	m   map[string]ladder.Strength
	err error
}

func (f fakeStrengths) Strengths(_ context.Context, ids []string) (map[string]ladder.Strength, float64, error) {
	out := map[string]ladder.Strength{}
	for _, id := range ids {
		if s, ok := f.m[id]; ok {
			out[id] = s
		}
	}
	return out, 0.5, f.err
}

type fakeBacklog struct {
	limit int
	n     map[string]int
	err   error
}

func (f fakeBacklog) Backlog(_ context.Context, _ lineage.Querier, root string) (int, error) {
	return f.n[root], f.err
}
func (f fakeBacklog) Limit() int { return f.limit }

type world struct {
	t  *testing.T
	st *store.Store
	sc *librarytest.Scene
	n  int
}

func newWorld(t *testing.T) *world {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &world{t: t, st: st, sc: librarytest.NewScene(t, st, t0)}
}

// recipe inserts a recipe with its recipe_keys row (a render).
func (w *world) recipe(id, style string) string {
	w.t.Helper()
	r := w.sc.Recipe(id, style)
	if _, err := w.st.W.Exec(`INSERT INTO recipe_keys(recipe_id, content_hash, render_hash) VALUES(?,?,?)`, r, "c"+r, "r"+r); err != nil {
		w.t.Fatal(err)
	}
	return r
}

func (w *world) exec(q string, args ...any) {
	w.t.Helper()
	if _, err := w.st.W.Exec(q, args...); err != nil {
		w.t.Fatal(err)
	}
}

// batch inserts a finished one-take batch of a principal created at the given time (an estimate it consumed comes with it).
func (w *world) batch(principal string, at time.Time) string {
	w.t.Helper()
	w.n++
	eid, bid := fmt.Sprintf("est-%d", w.n), fmt.Sprintf("bat-%d", w.n)
	ts := store.FormatTime(at)
	w.exec(`INSERT INTO estimates(id,principal_id,principal_kind,engine,backend,items_json,recipe_hashes,n_takes,quote_json,
		expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,workers_max,idle_s,
		requires_approval,approval_reasons,token_hash,created_at,expires_at,consumed_at,consumed_batch_id)
		VALUES(?,?,'agent','yue2','fake','[{"item_index":0}]','[]',1,'{}',10,20,5,25,690000,2,20,0,'[]',?,?,?,?,?)`,
		eid, principal, librarytest.Sha("tok"+eid), ts, ts, ts, bid)
	w.exec(`INSERT INTO batches(id,estimate_id,principal_id,principal_kind,principal_name,via,engine,backend,n_takes,rationale,
		expected_micro_usd,max_micro_usd,retry_allowance_micro_usd,ceiling_micro_usd,price_pph_micro_usd,attempt_overhead_s,kill_slack_s,
		workers_max,idle_s,state,created_at,finished_at) VALUES(?,?,?,'agent','n','nightshift','yue2','fake',1,NULL,10,20,5,25,690000,200,15,2,20,'finished',?,?)`,
		bid, eid, principal, ts, ts)
	return bid
}

func scopeYue2() mandates.Scope { return scope12() }

func lineagesOf(t *testing.T, w *world, rd *Reader) map[string]Lineage {
	t.Helper()
	ls, err := rd.Lineages(context.Background(), w.st.R, scopeYue2(), t0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Lineage{}
	for _, l := range ls {
		out[l.RootID] = l
	}
	return out
}

func TestReadLineages_BestTakeRule(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	rec := w.recipe("ra", "pop, 100 BPM")
	// lineage A: three takes; the one with counted duels and the highest U wins even with fewer stars
	rootA := w.sc.Track(rec, "alice song", "", w.sc.At(1))
	a1 := w.sc.Track(rec, "alice take 1", rootA, w.sc.At(2))
	a2 := w.sc.Track(rec, "alice take 2", rootA, w.sc.At(3))
	w.sc.Rate(rootA, 5)
	w.sc.Rate(a1, 3)
	w.sc.Rate(a2, 4)
	// lineage B: no duels; the highest stars wins, the newest id on a tie
	rootB := w.sc.Track(rec, "bob song", "", w.sc.At(4))
	b1 := w.sc.Track(rec, "bob take 1", rootB, w.sc.At(5))
	w.sc.Rate(rootB, 4)
	w.sc.Rate(b1, 4)
	// lineage C: only unrated takes: not a candidate
	rootC := w.sc.Track(rec, "carol song", "", w.sc.At(6))
	_ = rootC
	// lineage D: best by stars is discarded, so the next one counts
	rootD := w.sc.Track(rec, "dave song", "", w.sc.At(7))
	d1 := w.sc.Track(rec, "dave take", rootD, w.sc.At(8))
	w.sc.Rate(rootD, 5)
	w.sc.Rate(d1, 3)
	w.sc.Status(rootD, "discarded")
	// lineage E: a take with 2 duels does not count as measured
	rootE := w.sc.Track(rec, "erin song", "", w.sc.At(9))
	e1 := w.sc.Track(rec, "erin take", rootE, w.sc.At(10))
	w.sc.Rate(rootE, 3)
	w.sc.Rate(e1, 2)
	// a reference stub without recipe_keys is not a render
	stub := w.sc.Recipe("stub", "pop")
	rootF := w.sc.Track(stub, "frank stub", "", w.sc.At(11))
	w.sc.Rate(rootF, 5)

	rd := &Reader{Strengths: fakeStrengths{m: map[string]ladder.Strength{
		rootA: {U: 0.4, SD: 0.5, NDuels: 6},
		a1:    {U: 1.2, SD: 0.6, NDuels: 3},
		a2:    {U: 9, SD: 0.6, NDuels: 2}, // too few duels: ignored
		e1:    {U: 7, SD: 1.0, NDuels: 2},
	}}}
	got := lineagesOf(t, w, rd)

	wantA := Lineage{RootID: rootA, BestTrackID: a1, Engine: "yue2", Style: "pop, 100 BPM", BestStars: ip(3), U: fp(1.2), SD: fp(0.6), NDuels: 3}
	if diff := cmp.Diff(wantA, got[rootA]); diff != "" {
		t.Errorf("A (-want +got):\n%s", diff)
	}
	if b := got[rootB]; b.BestTrackID != b1 || b.BestStars == nil || *b.BestStars != 4 || b.U != nil || b.SD != nil || b.NDuels != 0 {
		t.Errorf("B: %+v (newest id wins a stars tie)", b)
	}
	if _, ok := got[rootC]; ok {
		t.Error("an unrated lineage must not be a candidate")
	}
	if d := got[rootD]; d.BestTrackID != d1 {
		t.Errorf("D: %+v (a discarded take never counts)", d)
	}
	if e := got[rootE]; e.BestTrackID != rootE || e.U != nil {
		t.Errorf("E: %+v (2 duels do not make a measured take; stars decide, the ladder stays unset)", e)
	}
	if _, ok := got[rootF]; ok {
		t.Error("a take without recipe_keys is not a render")
	}
}

func TestReadLineages_EngineAndEmpty(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	rec := w.recipe("ra", "pop")
	root := w.sc.Track(rec, "alice song", "", w.sc.At(1))
	w.sc.Rate(root, 5)
	rd := &Reader{Strengths: fakeStrengths{}}
	sc := scopeYue2()
	sc.Engines = []string{"acestep15"} // only the scope's engines are read
	if ls, err := rd.Lineages(context.Background(), w.st.R, sc, t0); err != nil || len(ls) != 0 {
		t.Errorf("other engine: %v %v", ls, err)
	}
	sc.Engines = nil
	if ls, err := rd.Lineages(context.Background(), w.st.R, sc, t0); err != nil || len(ls) != 0 {
		t.Errorf("no engines: %v %v", ls, err)
	}
	if got := lineagesOf(t, w, rd); got[root].BestStars == nil || *got[root].BestStars != 5 {
		t.Errorf("%+v", got)
	}
}

func TestReadLineages_OwnerRatingsOnly(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	rec := w.recipe("ra", "pop")
	root := w.sc.Track(rec, "alice song", "", w.sc.At(1))
	// an agent critic rated the take 5 stars: it is not the Owner's signal
	w.exec(`INSERT INTO ratings(track_id, source, rater, stars, principal_kind, principal_id, principal_name, created_at, updated_at)
		VALUES(?, 'agent', 'critic', 5, 'agent', 'agent:ag1', 'ag1', ?, ?)`, root, w.sc.At(3), w.sc.At(3))
	rd := &Reader{Strengths: fakeStrengths{}}
	if got := lineagesOf(t, w, rd); len(got) != 0 {
		t.Errorf("an agent rating made a candidate: %+v", got)
	}
	w.sc.Rate(root, 2)
	if got := lineagesOf(t, w, rd); got[root].BestStars == nil || *got[root].BestStars != 2 {
		t.Errorf("owner stars must win: %+v", got[root])
	}
}

func TestReadLineages_RecentUnjudged(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	rec := w.recipe("ra", "pop")
	root := w.sc.Track(rec, "alice song", "", w.sc.At(1))
	w.sc.Rate(root, 5)
	other := w.sc.Track(rec, "bob song", "", w.sc.At(2))
	w.sc.Rate(other, 5)
	old := w.sc.Track(rec, "carol song", "", w.sc.At(3))
	w.sc.Rate(old, 5)

	night := func(principal string, at time.Time, parent string) string {
		take := w.sc.Track(rec, "night take", parent, w.sc.At(4))
		b := w.batch(principal, at)
		j := w.sc.Job(b, 0, "", parent, rec)
		w.sc.JobProduced(j, take)
		return take
	}
	night(HolderKey, t0.Add(-24*time.Hour), root) // a night take of root, unrated: blocks root
	judged := night(HolderKey, t0.Add(-24*time.Hour), other)
	w.sc.Rate(judged, 3)                               // rated: does not block other
	night(HolderKey, t0.Add(-5*24*time.Hour), old)     // older than 3 nights: does not block
	night("agent:someone", t0.Add(-24*time.Hour), old) // another principal: does not block
	gone := night(HolderKey, t0.Add(-24*time.Hour), old)
	w.sc.Status(gone, "discarded") // a discarded unrated take can no longer be judged: does not block

	got := lineagesOf(t, w, &Reader{Strengths: fakeStrengths{}})
	if !got[root].RecentUnjudged || got[other].RecentUnjudged || got[old].RecentUnjudged {
		t.Errorf("root %v other %v old %v", got[root].RecentUnjudged, got[other].RecentUnjudged, got[old].RecentUnjudged)
	}
}

func TestReadLineages_Backlog(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	rec := w.recipe("ra", "pop")
	r1 := w.sc.Track(rec, "alice song", "", w.sc.At(1))
	r2 := w.sc.Track(rec, "bob song", "", w.sc.At(2))
	w.sc.Rate(r1, 5)
	w.sc.Rate(r2, 5)
	got := lineagesOf(t, w, &Reader{Strengths: fakeStrengths{}, Backlog: fakeBacklog{limit: 3, n: map[string]int{r1: 3, r2: 2}}})
	if !got[r1].Backlogged || got[r2].Backlogged {
		t.Errorf("limit 3: %v %v", got[r1].Backlogged, got[r2].Backlogged)
	}
	got = lineagesOf(t, w, &Reader{Strengths: fakeStrengths{}, Backlog: fakeBacklog{limit: 0, n: map[string]int{r1: 99}}})
	if got[r1].Backlogged {
		t.Error("limit 0 switches the gate off")
	}
}

func TestReadLineages_Errors(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	rec := w.recipe("ra", "pop")
	r1 := w.sc.Track(rec, "alice song", "", w.sc.At(1))
	w.sc.Rate(r1, 5)
	boom := errors.New("boom")
	ctx := context.Background()
	if _, err := (&Reader{Strengths: fakeStrengths{err: boom}}).Lineages(ctx, w.st.R, scopeYue2(), t0); !errors.Is(err, boom) {
		t.Errorf("strengths error: %v", err)
	}
	if _, err := (&Reader{Strengths: fakeStrengths{}, Backlog: fakeBacklog{limit: 1, err: boom}}).Lineages(ctx, w.st.R, scopeYue2(), t0); !errors.Is(err, boom) {
		t.Errorf("backlog error: %v", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := (&Reader{Strengths: fakeStrengths{}}).Lineages(cctx, w.st.R, scopeYue2(), t0); err == nil {
		t.Error("cancelled context must fail")
	}
}

// the planner over the reader's output end to end: only the best take of each lineage is a parent
func TestReadThenPlan(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	rec := w.recipe("ra", "pop, 100 BPM")
	var rs []string
	for i := 0; i < 3; i++ {
		r := w.sc.Track(rec, "song", "", w.sc.At(i+1))
		w.sc.Rate(r, 5)
		rs = append(rs, r)
	}
	ls, err := (&Reader{Strengths: fakeStrengths{}}).Lineages(context.Background(), w.st.R, scopeYue2(), t0)
	if err != nil {
		t.Fatal(err)
	}
	in := baseInput(ls...)
	in.MaxTakes = 3
	planned, dropped := Plan(in)
	if len(planned) != 3 || len(dropped) != 0 {
		t.Fatalf("%v %v", planned, dropped)
	}
	for _, p := range planned {
		if p.RootID != *p.Item.ParentTrackID {
			t.Errorf("parent %s of root %s", *p.Item.ParentTrackID, p.RootID)
		}
	}
	_ = rs
}

func TestReadLineages_RecentWindowIs72Hours(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	rec := w.recipe("ra", "pop")
	in := w.sc.Track(rec, "alice song", "", w.sc.At(1))
	w.sc.Rate(in, 5)
	out := w.sc.Track(rec, "bob song", "", w.sc.At(2))
	w.sc.Rate(out, 5)
	for parent, at := range map[string]time.Time{in: t0.Add(-72*time.Hour + time.Minute), out: t0.Add(-72*time.Hour - time.Minute)} {
		take := w.sc.Track(rec, "night take", parent, w.sc.At(4))
		j := w.sc.Job(w.batch(HolderKey, at), 0, "", parent, rec)
		w.sc.JobProduced(j, take)
	}
	got := lineagesOf(t, w, &Reader{Strengths: fakeStrengths{}})
	if !got[in].RecentUnjudged || got[out].RecentUnjudged {
		t.Errorf("inside %v outside %v", got[in].RecentUnjudged, got[out].RecentUnjudged)
	}
}
