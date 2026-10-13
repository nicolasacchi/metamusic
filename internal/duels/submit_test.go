package duels_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/duels/duelstest"
)

func TestNextDuel_QueuePerSource(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	// the fixture holds one open duel; add two more (oldest first by creation order)
	d2 := e.create(t, human, duelstest.TrackAlpha, duelstest.TrackBeta, true)
	e.now = e.now.Add(time.Minute)
	d3 := e.create(t, human, duelstest.TrackBeta, duelstest.TrackAlpha, true)
	next := func(p auth.Principal, exclude ...string) duels.NextResult {
		t.Helper()
		r, err := e.svc.Next(ctx, p, exclude)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	idOf := func(r duels.NextResult) string {
		if r.Duel == nil {
			return ""
		}
		return r.Duel.ID
	}
	if r := next(human); idOf(r) != duelstest.DuelOpen || r.OpenTotal != 3 {
		t.Errorf("human first = %s/%d", idOf(r), r.OpenTotal)
	}
	if r := next(device); idOf(r) != duelstest.DuelOpen {
		t.Errorf("device = %s", idOf(r))
	}
	if r := next(human, duelstest.DuelOpen); idOf(r) != d2.ID || r.OpenTotal != 3 {
		t.Errorf("exclude: %s/%d (open_total ignores exclude)", idOf(r), r.OpenTotal)
	}
	if r := next(human, duelstest.DuelOpen, d2.ID, d3.ID); r.Duel != nil || r.OpenTotal != 3 {
		t.Errorf("all excluded: %+v", r)
	}
	// a critic judging the oldest moves on to the next one for itself only
	e.judge(t, agent, duelstest.DuelOpen, "a")
	if r := next(agent); idOf(r) != d2.ID || r.OpenTotal != 2 {
		t.Errorf("critic after own verdict = %s/%d", idOf(r), r.OpenTotal)
	}
	if r := next(agent2); idOf(r) != duelstest.DuelOpen || r.OpenTotal != 3 {
		t.Errorf("other critic = %s/%d", idOf(r), r.OpenTotal)
	}
	if r := next(human); idOf(r) != duelstest.DuelOpen {
		t.Errorf("a critic verdict does not take the duel away from the human: %s", idOf(r))
	}
	// a human-source verdict removes the duel for everyone, via the device too
	e.judge(t, device, duelstest.DuelOpen, "b")
	for name, p := range map[string]auth.Principal{"human": human, "device": device, "agent": agent, "agent2": agent2} {
		if r := next(p); idOf(r) != d2.ID || r.OpenTotal != 2 {
			t.Errorf("%s after human verdict = %s/%d", name, idOf(r), r.OpenTotal)
		}
	}
	// expired duels are skipped
	e.now = e.now.Add(31 * 24 * time.Hour)
	if r := next(human); r.Duel != nil || r.OpenTotal != 0 {
		t.Errorf("expired queue = %+v", r)
	}
	// next writes nothing and takes a bounded exclude list
	before := e.count(t, `SELECT count(*) FROM duel_verdicts`)
	_, _ = e.svc.Next(ctx, human, nil)
	if e.count(t, `SELECT count(*) FROM duel_verdicts`) != before {
		t.Error("next wrote")
	}
	asProblem(t, func() error { _, err := e.svc.Next(ctx, human, make([]string, 51)); return err }(), 422, "VALIDATION_FAILED")
	if _, err := e.svc.Next(ctx, auth.CLI, nil); err == nil {
		t.Error("system principal must not be served a queue")
	}
}

func TestSubmitDuel_HumanThenDevice(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	first, err := e.svc.Submit(ctx, human, duelstest.DuelOpen, duels.VerdictInput{Verdict: "a", Note: sp("ok ")})
	if err != nil || first.Replayed || first.Verdict.Source != "human" || first.Verdict.PrincipalKind != "human" || first.Verdict.Note == nil || *first.Verdict.Note != "ok" {
		t.Fatalf("first = %+v, %v", first, err)
	}
	r, err := e.svc.Submit(ctx, device, duelstest.DuelOpen, duels.VerdictInput{Verdict: "a", Note: sp("ok")})
	if err != nil || !r.Replayed || r.Verdict.ID != first.Verdict.ID || r.Verdict.PrincipalKind != "human" {
		t.Fatalf("device replay = %+v, %v", r, err)
	}
	_, err = e.svc.Submit(ctx, device, duelstest.DuelOpen, duels.VerdictInput{Verdict: "b"})
	if pe := asProblem(t, err, 409, "DUEL_ALREADY_JUDGED"); pe.Details["verdict_id"] != first.Verdict.ID {
		t.Errorf("details = %v", pe.Details)
	}
	d, err := e.svc.Get(ctx, device, duelstest.DuelOpen)
	if err != nil || d.MyVerdict == nil || d.MyVerdict.ID != first.Verdict.ID || d.MyVerdict.PrincipalKind != "human" {
		t.Errorf("device my_verdict = %+v, %v", d.MyVerdict, err)
	}
	if nx, _ := e.svc.Next(ctx, device, nil); nx.Duel != nil && nx.Duel.ID == duelstest.DuelOpen {
		t.Error("device next must skip a duel the human decided")
	}
	if n := e.count(t, `SELECT count(*) FROM duel_verdicts WHERE duel_id = ? AND source = 'human'`, duelstest.DuelOpen); n != 1 {
		t.Errorf("human rows = %d", n)
	}
}

func TestSubmitDuel_WinnerMapping(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(o *duels.Options) { o.Rand = func() bool { return true } })
	d := e.create(t, human, duelstest.TrackAlpha, duelstest.TrackBeta, true) // swapped: A holds Beta
	a, b := e.slots(t, d.ID)
	if a != duelstest.TrackBeta || b != duelstest.TrackAlpha {
		t.Fatalf("slots = %s %s", a, b)
	}
	r := e.judge(t, human, d.ID, "a")
	if r.Verdict.WinnerTrackID == nil || *r.Verdict.WinnerTrackID != duelstest.TrackBeta {
		t.Errorf("winner = %v, want the requested b track", r.Verdict.WinnerTrackID)
	}
	d2 := e.create(t, agent, duelstest.TrackAlpha, duelstest.TrackBeta, true)
	r = e.judge(t, agent, d2.ID, "b")
	if *r.Verdict.WinnerTrackID != duelstest.TrackAlpha {
		t.Errorf("critic winner = %v", *r.Verdict.WinnerTrackID)
	}
	for _, v := range []string{"tie", "both_bad"} {
		d := e.create(t, human, duelstest.TrackAlpha, duelstest.TrackBeta, true)
		if r := e.judge(t, human, d.ID, v); r.Verdict.WinnerTrackID != nil {
			t.Errorf("%s winner = %v", v, *r.Verdict.WinnerTrackID)
		}
	}
}

func TestSubmitDuel_ReplayAndConflict(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	d := e.create(t, agent, duelstest.TrackAlpha, duelstest.TrackBeta, true)
	sub := func(v string, n *string) (duels.SubmitResult, error) {
		return e.svc.Submit(ctx, agent, d.ID, duels.VerdictInput{Verdict: v, Note: n, LatencyMS: sp(int64(5)), Switches: sp(3)})
	}
	first, err := sub("a", sp(" x "))
	if err != nil || first.Replayed || first.Verdict.LatencyMS == nil || *first.Verdict.LatencyMS != 5 {
		t.Fatalf("first = %+v, %v", first, err)
	}
	// latency and switches are not compared; the stored values win
	r, err := e.svc.Submit(ctx, agent, d.ID, duels.VerdictInput{Verdict: "a", Note: sp("x"), LatencyMS: sp(int64(999))})
	if err != nil || !r.Replayed || *r.Verdict.LatencyMS != 5 || r.Verdict.ID != first.Verdict.ID {
		t.Errorf("replay = %+v, %v", r, err)
	}
	for name, c := range map[string]struct {
		v string
		n *string
	}{"other verdict": {"b", sp("x")}, "other note": {"a", sp("y")}, "dropped note": {"a", nil}, "empty note": {"a", sp("  ")}} {
		if _, err := sub(c.v, c.n); err == nil {
			t.Errorf("%s: want conflict", name)
		} else {
			asProblem(t, err, 409, "DUEL_ALREADY_JUDGED")
		}
	}
	// "" and absent are the same note
	d2 := e.create(t, agent, duelstest.TrackAlpha, duelstest.TrackBeta, true)
	if _, err := e.svc.Submit(ctx, agent, d2.ID, duels.VerdictInput{Verdict: "tie", Note: sp("")}); err != nil {
		t.Fatal(err)
	}
	if r, err := e.svc.Submit(ctx, agent, d2.ID, duels.VerdictInput{Verdict: "tie"}); err != nil || !r.Replayed || r.Verdict.Note != nil {
		t.Errorf("absent vs empty = %+v, %v", r, err)
	}
	// another critic has its own verdict on the same duel
	if r, err := e.svc.Submit(ctx, agent2, d.ID, duels.VerdictInput{Verdict: "b"}); err != nil || r.Replayed {
		t.Errorf("second critic = %+v, %v", r, err)
	}
	// input validation
	for name, in := range map[string]duels.VerdictInput{
		"verdict": {Verdict: "x"}, "note": {Verdict: "a", Note: sp(strings.Repeat("é", 2001))},
		"latency_ms": {Verdict: "a", LatencyMS: sp(int64(-1))}, "switches": {Verdict: "a", Switches: sp(100001)},
	} {
		d := e.create(t, human, duelstest.TrackAlpha, duelstest.TrackBeta, true)
		_, err := e.svc.Submit(ctx, human, d.ID, in)
		if pe := asProblem(t, err, 422, "VALIDATION_FAILED"); pe.Details["field"] != name {
			t.Errorf("%s: details = %v", name, pe.Details)
		}
	}
	// the longest allowed note (in runes) is accepted
	d3 := e.create(t, human, duelstest.TrackAlpha, duelstest.TrackBeta, true)
	if _, err := e.svc.Submit(ctx, human, d3.ID, duels.VerdictInput{Verdict: "a", Note: sp(strings.Repeat("é", 2000))}); err != nil {
		t.Errorf("2000 runes: %v", err)
	}
	_, err = e.svc.Submit(ctx, human, "nope", duels.VerdictInput{Verdict: "a"})
	asProblem(t, err, 404, "NOT_FOUND")
	if _, err := e.svc.Submit(ctx, auth.CLI, d3.ID, duels.VerdictInput{Verdict: "a"}); err == nil {
		t.Error("system principal must not judge")
	}
}

func TestSubmitDuel_ConcurrentIdentical(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	const n = 8
	var wg sync.WaitGroup
	results := make([]duels.SubmitResult, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := human
			if i%2 == 1 {
				p = device // the same human source through a different principal
			}
			results[i], errs[i] = e.svc.Submit(ctx, p, duelstest.DuelOpen, duels.VerdictInput{Verdict: "b", Note: sp("same")})
		}()
	}
	wg.Wait()
	created, replayed := 0, 0
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("submit %d: %v", i, errs[i])
		}
		if results[i].Replayed {
			replayed++
		} else {
			created++
		}
		if results[i].Verdict.ID != results[0].Verdict.ID {
			t.Errorf("verdict ids differ: %s vs %s", results[i].Verdict.ID, results[0].Verdict.ID)
		}
	}
	if created != 1 || replayed != n-1 {
		t.Errorf("created %d replayed %d", created, replayed)
	}
	if got := e.count(t, `SELECT count(*) FROM duel_verdicts WHERE duel_id = ? AND source = 'human'`, duelstest.DuelOpen); got != 1 {
		t.Errorf("rows = %d", got)
	}
}

func TestSubmitDuel_Expired(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, err := e.svc.Submit(context.Background(), human, duelstest.DuelExpired, duels.VerdictInput{Verdict: "a"})
	asProblem(t, err, 409, "DUEL_EXPIRED")
	if n := e.count(t, `SELECT count(*) FROM duel_verdicts WHERE duel_id = ?`, duelstest.DuelExpired); n != 0 {
		t.Errorf("rows = %d", n)
	}
}

func TestSubmitDuel_SawReveal(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	saw := func(p auth.Principal, id string) bool {
		t.Helper()
		return e.judge(t, p, id, "tie").Verdict.SawReveal
	}
	if saw(agent2, duelstest.DuelOpen) {
		t.Error("critic on an open blind duel saw no reveal")
	}
	if !saw(agent2, duelstest.DuelDecided) {
		t.Error("critic on a decided duel saw the reveal")
	}
	sighted := e.create(t, human, duelstest.TrackAlpha, duelstest.TrackBeta, false)
	if !saw(human, sighted.ID) {
		t.Error("human on a sighted duel saw the reveal")
	}
	blind := e.create(t, human, duelstest.TrackAlpha, duelstest.TrackBeta, true)
	if saw(human, blind.ID) {
		t.Error("human on a blind duel saw no reveal")
	}
}

func TestListDuels_Filters(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	list := func(p auth.Principal, f duels.ListFilter) duels.Page {
		t.Helper()
		pg, err := e.svc.List(ctx, p, f)
		if err != nil {
			t.Fatal(err)
		}
		return pg
	}
	ids := func(pg duels.Page) []string {
		out := []string{}
		for _, it := range pg.Items {
			out = append(out, it.ID)
		}
		return out
	}
	// newest first: Open (-1h), Sighted (-2d), Decided (-3d), Expired (-40d)
	all := []string{duelstest.DuelOpen, duelstest.DuelSighted, duelstest.DuelDecided, duelstest.DuelExpired}
	if diff := cmp.Diff(all, ids(list(human, duels.ListFilter{}))); diff != "" {
		t.Errorf("all (-want +got):\n%s", diff)
	}
	for state, want := range map[string][]string{
		"open": {duelstest.DuelOpen}, "decided": {duelstest.DuelSighted, duelstest.DuelDecided}, "expired": {duelstest.DuelExpired},
	} {
		if diff := cmp.Diff(want, ids(list(human, duels.ListFilter{States: []string{state}}))); diff != "" {
			t.Errorf("state %s (-want +got):\n%s", state, diff)
		}
	}
	if got := ids(list(human, duels.ListFilter{States: []string{"open", "expired"}})); len(got) != 2 {
		t.Errorf("open+expired = %v", got)
	}
	if diff := cmp.Diff([]string{duelstest.DuelOpen, duelstest.DuelDecided, duelstest.DuelExpired}, ids(list(human, duels.ListFilter{Mode: "blind"}))); diff != "" {
		t.Errorf("blind (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{duelstest.DuelSighted}, ids(list(human, duels.ListFilter{Mode: "sighted"}))); diff != "" {
		t.Errorf("sighted (-want +got):\n%s", diff)
	}
	// track and root match either slot, revealed or not; unrevealed rows keep track null
	for _, f := range []duels.ListFilter{{TrackID: duelstest.TrackAlpha}, {TrackID: duelstest.TrackBeta}, {RootTrackID: duelstest.TrackAlpha}} {
		pg := list(agent2, f)
		if pg.Total != 4 {
			t.Errorf("%+v: total %d", f, pg.Total)
		}
		for _, it := range pg.Items {
			if it.ID == duelstest.DuelOpen && (it.Revealed || it.Slots.A.Track != nil) {
				t.Errorf("unrevealed row leaks: %+v", it)
			}
		}
	}
	if pg := list(human, duels.ListFilter{TrackID: "other"}); pg.Total != 0 || pg.Items == nil || len(pg.Items) != 0 {
		t.Errorf("no match = %+v", pg)
	}
	if pg := list(human, duels.ListFilter{RootTrackID: duelstest.TrackBeta}); pg.Total != 0 {
		t.Errorf("Beta is not a root: %+v", pg)
	}
	// summary content for a revealed row
	pg := list(human, duels.ListFilter{States: []string{"decided"}})
	row := pg.Items[1]
	if row.ID != duelstest.DuelDecided || !row.Revealed || row.Slots.A.Track == nil || row.Slots.A.Track.ID != duelstest.TrackBeta ||
		row.MyVerdict == nil || *row.MyVerdict != "a" || row.HumanVerdict == nil || row.HumanVerdict.Verdict != "a" || row.CriticVerdicts != 1 || !row.LoudnessMatched ||
		row.Window.DurationS != 45 {
		t.Errorf("row = %+v", row)
	}
	// a critic that has not judged sees no machine opinions count
	if r := list(agent2, duels.ListFilter{States: []string{"decided"}}).Items[1]; r.CriticVerdicts != 0 || r.MyVerdict != nil {
		t.Errorf("unjudged critic row = %+v", r)
	}
	// paging without duplicates or gaps, and the page cap
	for i := range 5 {
		e.now = e.now.Add(time.Duration(i+1) * time.Second)
		e.create(t, human, duelstest.TrackAlpha, duelstest.TrackBeta, true)
	}
	var seen []string
	cursor := ""
	for pages := 0; ; pages++ {
		pg := list(human, duels.ListFilter{Limit: 4, Cursor: cursor})
		if pg.Total != 9 || len(pg.Items) > 4 {
			t.Fatalf("page = %d items, total %d", len(pg.Items), pg.Total)
		}
		seen = append(seen, ids(pg)...)
		if pg.NextCursor == nil {
			break
		}
		cursor = *pg.NextCursor
		if pages > 5 {
			t.Fatal("paging does not terminate")
		}
	}
	uniq := map[string]bool{}
	for _, id := range seen {
		uniq[id] = true
	}
	if len(seen) != 9 || len(uniq) != 9 {
		t.Errorf("paged %d ids, %d unique", len(seen), len(uniq))
	}
	if pg := list(human, duels.ListFilter{Limit: 100000}); len(pg.Items) != 9 {
		t.Errorf("big limit = %d", len(pg.Items))
	}
	for name, f := range map[string]duels.ListFilter{"state": {States: []string{"x"}}, "mode": {Mode: "x"}, "cursor": {Cursor: "!!"}} {
		_, err := e.svc.List(ctx, human, f)
		asProblem(t, err, 422, "VALIDATION_FAILED")
		_ = name
	}
}
