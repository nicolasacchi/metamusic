package desk_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/desk"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// renderJob turns a fixture job into a render_plan job of the plan (the writer of such jobs is a later lane; the triggers that
// freeze a job's operator are dropped in this test database only).
func (e *env) renderJob(job, planID string) {
	e.t.Helper()
	e.dropJobTriggers()
	mut, _ := json.Marshal(map[string]any{"operator": "render_plan", "params": map[string]any{"plan_id": planID}})
	e.exec(`UPDATE jobs SET operator = 'render_plan', mutation_json = ? WHERE id = ?`, string(mut), job)
}

// TestGet_RendersFromJobs: renders come from the jobs that name the plan; rendered counts those that made a take.
func TestGet_RendersFromJobs(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	pl := e.plan()
	ctx := context.Background()
	got, err := e.svc.Get(ctx, alice, pl.ID, desk.DefaultInclude)
	if err != nil || got.Renders == nil || len(*got.Renders) != 0 || got.Rendered != 0 {
		t.Fatalf("before: %v %+v", err, got.Renders)
	}
	e.renderJob(lt.JobDone, pl.ID)
	e.renderJob(lt.JobAwaiting1, pl.ID)
	got, err = e.svc.Get(ctx, alice, pl.ID, desk.DefaultInclude)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rendered != 1 || got.Renders == nil || len(*got.Renders) != 2 {
		t.Fatalf("rendered %d renders %+v", got.Rendered, got.Renders)
	}
	var done, queued *desk.RenderRef
	for i, r := range *got.Renders {
		if r.JobID == lt.JobDone {
			done = &(*got.Renders)[i]
		} else {
			queued = &(*got.Renders)[i]
		}
	}
	if done == nil || queued == nil || done.TrackID == nil || *done.TrackID != lt.TrackJobChild || done.BatchID != lt.BatchDone || done.State != "completed" || queued.TrackID != nil {
		t.Errorf("done %+v queued %+v", done, queued)
	}
	if got.Auditions == nil || len(*got.Auditions) != 0 {
		t.Errorf("auditions %+v", got.Auditions)
	}
	// include trims the optional parts
	slim, err := e.svc.Get(ctx, alice, pl.ID, desk.Include{})
	if err != nil || slim.ABC != nil || slim.Renders != nil || slim.Auditions != nil || slim.Display != nil || slim.BarSpans != nil {
		t.Errorf("slim = %+v %v", slim, err)
	}
	bars, err := e.svc.Get(ctx, alice, pl.ID, desk.Include{Bars: true})
	if err != nil || len(bars.BarSpans) != 6 || bars.BarSpans[1].Voice != "1" || bars.BarSpans[1].StartS == nil || *bars.BarSpans[1].StartS != 1 {
		t.Errorf("bar spans %+v %v", bars.BarSpans, err)
	}
	if _, err := e.svc.Get(ctx, alice, "no-such-plan", desk.DefaultInclude); err == nil {
		t.Error("an unknown plan was found")
	}
}

// TestGet_AuditionsAreSigned: audition rows are listed with a link signed for the caller.
func TestGet_AuditionsListed(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	pl := e.plan()
	h := lt.Sha("one")
	e.exec(`INSERT INTO desk_auditions(id,plan_id,spec_hash,soundfont,soundfont_sha256,synth_version,chords,mp3_path,mp3_sha256,mp3_bytes,pcm_sha256,seconds,cpu_ms,report_json,principal_id,created_at)
		VALUES('a1',?,?,'gm',?,'2.3','block','derived/auditions/ab/x.mp3',?,10,?,12.5,5,'{}','human:alice','2026-11-21T10:00:00.000Z')`, pl.ID, h, h, h, h)
	got, err := e.svc.Get(context.Background(), alice, pl.ID, desk.DefaultInclude)
	if err != nil || got.Auditions == nil || len(*got.Auditions) != 1 {
		t.Fatalf("%v %+v", err, got.Auditions)
	}
	if a := (*got.Auditions)[0]; a.ID != "a1" || a.Chords != "block" || a.Seconds != 12.5 || a.Soundfont != "gm" || a.URL != "" {
		t.Errorf("audition = %+v", a) // no Signer in this test service: the link is empty
	}
}

// TestGet_DisplayForDivergentScore: a yue2 score that reads differently under the standard rule also comes with a display text.
func TestGet_DisplayForDivergentScore(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// ^F then f in the same bar: yue2 carries the sharp to the other octave, standard does not
	const divergent = "X:1\nT:t\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\n% verse\n^F f G A B c d e|e d c B A G F E|\n"
	e.writeScore("takes/"+lt.TrackLuna+"/score.abc", divergent)
	e.exec(`DROP TRIGGER scores_immutable`)
	e.exec(`UPDATE scores SET abc_sha256 = ?, bars = 2 WHERE track_id = ?`, lt.Sha(divergent), lt.TrackLuna)
	pl, _, err := e.svc.FromTake(context.Background(), alice, lt.TrackLuna)
	if err != nil {
		t.Fatal(err)
	}
	if pl.Display == nil || pl.Display.ABC == "" || len(pl.Display.BarSpans) != 2 || pl.Display.ABC == divergent {
		t.Fatalf("display = %+v", pl.Display)
	}
	plain, err := e.svc.Get(context.Background(), alice, pl.ID, desk.Include{})
	if err != nil || plain.Display != nil {
		t.Errorf("display without include: %+v", plain.Display)
	}
}

func ids(p desk.Page) []string {
	var out []string
	for _, it := range p.Items {
		out = append(out, it.ID)
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestList_Filters: the filters, the order, the verdict view of people and agents, and paging.
func TestList_Filters(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	p1 := e.plan()
	e.advance(time.Minute)
	c1, _, err := e.svc.Edit(ctx, alice, p1.ID, replaceBar1("CDEFGABc"))
	if err != nil {
		t.Fatal(err)
	}
	e.advance(time.Minute)
	c2, _, err := e.svc.Edit(ctx, bob, p1.ID, replaceBar1("GABcCDEF"))
	if err != nil {
		t.Fatal(err)
	}
	e.renderJob(lt.JobDone, c2.ID)
	if _, err := e.svc.SetVerdict(ctx, alice, c1.ID, desk.VerdictIn{Verdict: "liked"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetVerdict(ctx, robot, c2.ID, desk.VerdictIn{Verdict: "rejected", Note: "muddy"}); err != nil {
		t.Fatal(err)
	}
	list := func(p desk.Filter) desk.Page {
		t.Helper()
		pg, err := e.svc.List(ctx, alice, p)
		if err != nil {
			t.Fatalf("%+v: %v", p, err)
		}
		return pg
	}
	if got, want := ids(list(desk.Filter{})), []string{c2.ID, c1.ID, p1.ID}; !eq(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
	yes, no := true, false
	for _, c := range []struct {
		name string
		f    desk.Filter
		want []string
	}{
		{"source edit", desk.Filter{Source: "edit"}, []string{c2.ID, c1.ID}},
		{"source take", desk.Filter{Source: "take"}, []string{p1.ID}},
		{"source job", desk.Filter{Source: "job"}, nil},
		{"base track", desk.Filter{BaseTrackID: lt.TrackLuna}, []string{c2.ID, c1.ID, p1.ID}},
		{"other base", desk.Filter{BaseTrackID: lt.TrackShort}, nil},
		{"liked by the owner", desk.Filter{Verdict: "liked"}, []string{c1.ID}},
		{"rejected by the owner", desk.Filter{Verdict: "rejected"}, nil},
		{"no owner verdict", desk.Filter{Verdict: "none"}, []string{c2.ID, p1.ID}},
		{"rendered", desk.Filter{Rendered: &yes}, []string{c2.ID}},
		{"not rendered", desk.Filter{Rendered: &no}, []string{c1.ID, p1.ID}},
		{"batch", desk.Filter{BatchID: "no-batch"}, nil},
	} {
		if got := ids(list(c.f)); !eq(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	// an agent reads the critic's row
	ag, err := e.svc.List(ctx, robot, desk.Filter{Verdict: "rejected"})
	if err != nil || !eq(ids(ag), []string{c2.ID}) {
		t.Errorf("agent rejected: %v %v", ids(ag), err)
	}
	// the summary carries both raters' verdicts
	for _, it := range list(desk.Filter{Source: "edit"}).Items {
		switch it.ID {
		case c1.ID:
			if it.Verdict == nil || *it.Verdict != "liked" || it.CriticVerdict != nil {
				t.Errorf("c1 verdicts %v %v", it.Verdict, it.CriticVerdict)
			}
		case c2.ID:
			if it.Verdict != nil || it.CriticVerdict == nil || *it.CriticVerdict != "rejected" || it.Rendered != 1 {
				t.Errorf("c2 verdicts %v %v rendered %d", it.Verdict, it.CriticVerdict, it.Rendered)
			}
		}
	}
	// paging: one at a time, in order, ending with a null cursor
	var seen []string
	cur := ""
	for i := 0; i < 5; i++ {
		pg := list(desk.Filter{Limit: 1, Cursor: cur})
		seen = append(seen, ids(pg)...)
		if pg.NextCursor == nil {
			break
		}
		cur = *pg.NextCursor
	}
	if !eq(seen, []string{c2.ID, c1.ID, p1.ID}) {
		t.Errorf("paged = %v", seen)
	}
	for _, bad := range []desk.Filter{{Source: "x"}, {Verdict: "x"}, {Limit: 101}, {Limit: -1}, {Cursor: "!!"}} {
		if _, err := e.svc.List(ctx, alice, bad); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
}

func TestParseInclude(t *testing.T) {
	t.Parallel()
	if inc, err := desk.ParseInclude(""); err != nil || inc != desk.DefaultInclude {
		t.Errorf("default %+v %v", inc, err)
	}
	if inc, err := desk.ParseInclude("bars, abc"); err != nil || (inc != desk.Include{ABC: true, Bars: true}) {
		t.Errorf("bars,abc %+v %v", inc, err)
	}
	if _, err := desk.ParseInclude("abc,loud"); err == nil {
		t.Error("an unknown part was accepted")
	}
}

// TestCheck_Report: check_abc parses and reports, with a base it lists the violations, and it never writes.
func TestCheck_Report(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	rep, err := e.svc.Check(ctx, desk.CheckIn{ABC: aligned})
	if err != nil || !rep.OK || rep.Bars != 6 || rep.Dialect != "yue2" || rep.Violations != nil {
		t.Fatalf("%v %+v", err, rep)
	}
	rep, err = e.svc.Check(ctx, desk.CheckIn{ABC: "X:1\nK:C\nCDEF|\n", BaseABC: aligned, Dialect: "standard"})
	if err != nil || rep.Dialect != "standard" || rep.Violations == nil {
		t.Fatalf("%v %+v", err, rep)
	}
	rep, err = e.svc.Check(ctx, desk.CheckIn{ABC: "X:1\nK:C\nCD&EF|\n"})
	if err != nil || rep.OK {
		t.Errorf("garbage: %v %+v", err, rep.OK)
	}
	for _, bad := range []desk.CheckIn{{}, {ABC: "x", Dialect: "abc3"}} {
		if _, err := e.svc.Check(ctx, bad); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
	if n := e.scalar(`SELECT COUNT(*) FROM desk_plans`); n != "0" {
		t.Errorf("check wrote %s plans", n)
	}
}

// dropJobTriggers drops the update triggers of jobs in this test database, so a test can shape a job row freely.
func (e *env) dropJobTriggers() {
	e.t.Helper()
	rows, err := e.st.R.Query(`SELECT name FROM sqlite_master WHERE type = 'trigger' AND tbl_name = 'jobs' AND name <> 'jobs_no_delete'`)
	if err != nil {
		e.t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			e.t.Fatal(err)
		}
		names = append(names, n)
	}
	_ = rows.Close()
	for _, n := range names {
		e.exec(`DROP TRIGGER ` + n)
	}
}
