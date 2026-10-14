package library_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

func newEnvM3(t *testing.T) *env { return newEnvWith(t, lt.FixtureM3) }

func TestSearchTracks_OriginFilter(t *testing.T) {
	t.Parallel()
	e := newEnvM3(t)
	sameSet(t, "origin=job", search(t, e, library.SearchParams{Origin: []string{"job"}, Status: []string{"draft", "to_relisten", "final", "discarded"}}), lt.TrackJobChild)
	imp := search(t, e, library.SearchParams{Origin: []string{"import"}, Status: []string{"draft", "to_relisten", "final", "discarded"}})
	for _, id := range imp {
		if id == lt.TrackJobChild {
			t.Error("a job take listed as imported")
		}
	}
	all := search(t, e, library.SearchParams{Origin: []string{"job", "import"}, Status: []string{"draft", "to_relisten", "final", "discarded"}})
	none := search(t, e, library.SearchParams{Status: []string{"draft", "to_relisten", "final", "discarded"}})
	sameSet(t, "both origins = no filter", all, none...)
	if _, err := e.svc.SearchTracks(context.Background(), library.SearchParams{Origin: []string{"teleport"}}); err == nil {
		t.Error("an unknown origin was accepted")
	}
	// the exports use the same selection
	got, err := e.svc.MatchTrackIDs(context.Background(), library.SearchParams{Origin: []string{"job"}, Status: []string{"draft"}})
	if err != nil || len(got) != 1 || got[0] != lt.TrackJobChild {
		t.Errorf("MatchTrackIDs: %v %v", got, err)
	}
	// summaries: origin and cost
	page, err := e.svc.SearchTracks(context.Background(), library.SearchParams{Origin: []string{"job"}, Status: []string{"draft"}})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(err)
	}
	it := page.Items[0]
	if it.Origin != "job" || it.CostDisplayMicroUSD == nil || *it.CostDisplayMicroUSD != 18216+12000 || it.CostDisplayUSD == nil || *it.CostDisplayUSD != "0.030216" {
		t.Errorf("job summary %+v", it)
	}
	page, _ = e.svc.SearchTracks(context.Background(), library.SearchParams{Origin: []string{"import"}, Status: []string{"draft"}})
	for _, it := range page.Items {
		if it.Origin != "import" || it.CostDisplayMicroUSD != nil || it.CostDisplayUSD != nil {
			t.Errorf("imported summary %+v", it)
		}
	}
}

func TestTrackDetail_GenerationBlock(t *testing.T) {
	t.Parallel()
	e := newEnvM3(t)
	d, err := e.svc.Track(context.Background(), lt.TrackJobChild)
	if err != nil {
		t.Fatal(err)
	}
	g := d.Generation
	if g == nil || g.JobID != lt.JobDone || g.BatchID != lt.BatchDone || g.Operator != "reroll_seed" || g.Rationale != nil {
		t.Fatalf("generation %+v", g)
	}
	if g.Cost == nil || g.Cost.Basis != "reconciled" || g.Cost.DisplayMicroUSD != 18216+12000 || g.Cost.DisplayUSD != "0.030216" {
		t.Errorf("cost %+v", g.Cost)
	}
	if g.Env == nil || g.Env.EnvID != lt.EnvFixture || g.Env.GPUClass != "RTX 4090" || g.Env.Detail.GPUName != "Fixture GPU" || g.Env.Weights["fixture/stage1"] != "rev1" {
		t.Errorf("env %+v", g.Env)
	}
	if d.Origin != "job" {
		t.Errorf("origin %q", d.Origin)
	}
	imp, err := e.svc.Track(context.Background(), lt.TrackLuna)
	if err != nil || imp.Generation != nil {
		t.Errorf("an imported take has no generation block: %+v %v", imp.Generation, err)
	}
}

func TestJobCost_Basis(t *testing.T) {
	t.Parallel()
	e := newEnvM3(t)
	ctx := context.Background()
	c, err := library.JobCost(ctx, e.st.R, lt.JobDone)
	if err != nil || c.Basis != "reconciled" || c.Display != 30216 || c.Expected != 18400 {
		t.Errorf("reconciled: %+v %v", c, err)
	}
	c, _ = library.JobCost(ctx, e.st.R, lt.JobAwaiting1)
	if c.Basis != "estimate" || c.Display != c.Expected {
		t.Errorf("not started: %+v", c)
	}
	c, _ = library.JobCost(ctx, e.st.R, lt.JobFailed)
	if c.Basis != "provisional" || c.Display != 0 {
		t.Errorf("failed without spend: %+v", c)
	}
	if _, err := library.JobCost(ctx, e.st.R, "nope"); err == nil {
		t.Error("unknown job")
	}
	env, err := library.EnvByID(ctx, e.st.R, "nope")
	if err != nil || env != nil {
		t.Errorf("unknown env: %v %v", env, err)
	}
	if env, _ = library.EnvByID(ctx, e.st.R, ""); env != nil {
		t.Error("empty env id")
	}
	if g, err := library.GenerationOf(ctx, e.st.R, lt.TrackLuna); g != nil || err != nil {
		t.Errorf("imported track: %v %v", g, err)
	}
}

func TestGetStats_GenerationBreakdown(t *testing.T) {
	t.Parallel()
	e := newEnvM3(t)
	// two more real takes share seed 4242, one of them rated 4 stars
	ctx := context.Background()
	tx, err := e.st.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var tracks []string
	for i, id := range []string{"gen-x-s4242", "gen-y-s4242"} {
		rid, _, err := e.svc.InsertRecipe(ctx, tx, jobRecipe(t, id, 4242), "import")
		if err != nil {
			t.Fatal(err)
		}
		tid := "01900000-0000-7000-8000-00000000b00" + string(rune('1'+i))
		nt := newTake(tid, rid, 200+i)
		if err := e.svc.InsertTake(ctx, tx, nt); err != nil {
			t.Fatal(err)
		}
		tracks = append(tracks, tid)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	e.mustExec(t, `INSERT INTO ratings(track_id, source, stars, verdict, principal_kind, principal_id, principal_name, created_at, updated_at) VALUES(?, 'human', 4, NULL, 'human', 'human:fixture', 'fixture', ?, ?)`,
		tracks[0], "2026-10-06T12:00:00Z", "2026-10-06T12:00:00Z")

	st, err := e.svc.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g := st.Generation
	if g.JobsByState["completed"] != 1 || g.JobsByState["awaiting_confirmation"] != 2 || g.JobsByState["failed"] != 1 || len(g.JobsByState) != 3 {
		t.Errorf("jobs_by_state %v", g.JobsByState)
	}
	if g.TakesMonth != 1 {
		t.Errorf("takes_month %d", g.TakesMonth)
	}
	if g.SpendMonthMicroUSD != 30000 || g.SpendMonthUSD != "0.03" {
		t.Errorf("spend_month %d %s", g.SpendMonthMicroUSD, g.SpendMonthUSD)
	}
	if g.OutstandingMicroUSD != 77050 || g.OutstandingUSD != "0.07705" {
		t.Errorf("outstanding %d %s", g.OutstandingMicroUSD, g.OutstandingUSD)
	}
	var seed *library.BreakdownSeed
	for i := range st.Breakdown.BySeed {
		if st.Breakdown.BySeed[i].Seed == 4242 {
			seed = &st.Breakdown.BySeed[i]
		}
	}
	if seed == nil || seed.Tracks != 2 || seed.Rated != 1 || seed.MeanStars == nil || *seed.MeanStars != 4 {
		t.Errorf("by_seed %+v", st.Breakdown.BySeed)
	}
	if len(st.Breakdown.ByStyleTerm) == 0 || len(st.Breakdown.ByStyleTerm) > 30 {
		t.Fatalf("by_style_term %+v", st.Breakdown.ByStyleTerm)
	}
	for i := 1; i < len(st.Breakdown.ByStyleTerm); i++ {
		a, b := st.Breakdown.ByStyleTerm[i-1], st.Breakdown.ByStyleTerm[i]
		if a.Tracks < b.Tracks || a.Tracks == b.Tracks && a.Term > b.Term {
			t.Errorf("by_style_term order at %d: %+v then %+v", i, a, b)
		}
	}
	var pop *library.BreakdownTerm
	for i := range st.Breakdown.ByStyleTerm {
		if st.Breakdown.ByStyleTerm[i].Term == "synth pop" {
			pop = &st.Breakdown.ByStyleTerm[i]
		}
	}
	if pop == nil || pop.Tracks < 3 || pop.Rated != 1 || pop.MeanStars == nil || *pop.MeanStars != 4 {
		t.Errorf("term synth pop: %+v", pop)
	}
	for _, b := range st.Breakdown.ByStyleTerm {
		if b.Rated == 0 && b.MeanStars != nil {
			t.Errorf("an unrated term has a mean: %+v", b)
		}
	}
}

func TestGetStats_BySeedExcludesStubs(t *testing.T) {
	t.Parallel()
	e := newEnvM3(t)
	var stubs int
	if err := e.st.R.QueryRow(`SELECT COUNT(*) FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE r.engine = 'unknown'`).Scan(&stubs); err != nil || stubs < 2 {
		t.Fatalf("the fixture needs at least two reference tracks: %d %v", stubs, err)
	}
	st, err := e.svc.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range st.Breakdown.BySeed {
		if s.Seed == 0 {
			t.Errorf("the placeholder seed 0 of the stubs is listed: %+v", s)
		}
	}
	for _, b := range st.Breakdown.ByStyleTerm {
		if strings.Contains(b.Term, "reference") {
			t.Errorf("stub term %+v", b)
		}
	}
}

func TestDrafts_InFlightAndGeneratedTakes(t *testing.T) {
	t.Parallel()
	e := newEnvM3(t)
	ctx := context.Background()
	// a draft rendered by a live job is in flight and not editable; a finished job's take is counted
	d, err := e.svc.Draft(ctx, lt.Draft1, false)
	if err != nil || d.InFlight || d.GeneratedTakes != 0 || !d.Editable {
		t.Fatalf("idle draft %+v %v", d, err)
	}
	e.mustExec(t, `INSERT INTO jobs(id,batch_id,take_index,kind,engine,backend,recipe_id,draft_id,operator,mutation_json,title,expected_audio_s,expected_s,timeout_s,expected_micro_usd,reserved_micro_usd,state,next_attempt_at,queued_at,created_at,updated_at)
		SELECT 'job-inflight', batch_id, 7, kind, engine, backend, recipe_id, ?, 'from_draft', '{}', 'T', 216, 96, 186, 10, 38525, 'awaiting_confirmation', next_attempt_at, NULL, created_at, updated_at FROM jobs WHERE id = ?`, lt.Draft1, lt.JobAwaiting1)
	d, err = e.svc.Draft(ctx, lt.Draft1, false)
	if err != nil || !d.InFlight || d.Editable {
		t.Fatalf("draft with a live job %+v %v", d, err)
	}
	page, err := e.svc.ListDrafts(ctx, library.DraftParams{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range page.Items {
		if it.ID == lt.Draft1 {
			found = true
			if !it.InFlight || it.Editable {
				t.Errorf("list item %+v", it)
			}
		} else if it.InFlight {
			t.Errorf("draft %s in flight", it.ID)
		}
	}
	if !found {
		t.Fatal("draft not listed")
	}
}

func (e *env) mustExec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := e.st.W.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}
