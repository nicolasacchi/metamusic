package runpod_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
)

// deskStub is the scripted REST server of the Desk cases: the Ears stub (provider caps) plus the plan and track reads. Nothing here reaches a
// real server.
type deskStub struct {
	*earsStub
	plans        []map[string]any   // get_plan bodies, by id; the first is also listed for job-1
	tracks       []map[string]any   // search_tracks rows
	operator     string             // the rendered take's lineage operator
	renderedT    string             // the track the plan lists among its renders
	made         []string           // create_plan_from_take calls
	pages        [][]map[string]any // paged search_tracks rows (cursor = page index), newest first
	trackQueries []string
}

func deskPlanDoc(id string, over map[string]any) map[string]any {
	d := map[string]any{"id": id, "source": "job", "seed": 11, "cot": "full", "bars": 16, "meter": "4/4", "expected_audio_s": 30,
		"abc": "X:1\nM:4/4\nK:C\nV:1\nCDEF|\n", "score": map[string]any{"sha256": "ab", "bytes": 40, "dialect": "yue2", "aligned": true, "abc_tokens": 120}}
	for k, v := range over {
		d[k] = v
	}
	return d
}

func newDeskStub(t *testing.T, caps []string, jobs ...map[string]any) *deskStub {
	t.Helper()
	d := &deskStub{earsStub: newEarsStub(t, caps, nil, jobs...), operator: "render_plan", renderedT: "trk-1"}
	d.plans = []map[string]any{deskPlanDoc("plan-1", nil), deskPlanDoc("plan-2", nil)}
	inner := d.Config.Handler
	d.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		p := r.URL.Path
		switch {
		case r.Method == http.MethodGet && p == "/api/plans":
			var items []any
			for _, pl := range d.plans {
				items = append(items, map[string]any{"id": pl["id"]})
			}
			_ = enc.Encode(map[string]any{"items": items, "next_cursor": nil})
		case r.Method == http.MethodGet && strings.HasPrefix(p, "/api/plans/"):
			id := strings.TrimPrefix(p, "/api/plans/")
			for _, pl := range d.plans {
				if pl["id"] == id {
					out := map[string]any{}
					for k, v := range pl {
						out[k] = v
					}
					out["renders"] = []any{map[string]any{"job_id": "job-1", "track_id": d.renderedT}}
					_ = enc.Encode(out)
					return
				}
			}
			w.WriteHeader(404)
			_ = enc.Encode(map[string]any{"code": "NOT_FOUND", "message": "no plan"})
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/plan"):
			d.made = append(d.made, p)
			made := deskPlanDoc("plan-take", map[string]any{"source": "take"})
			d.plans = append(d.plans, made)
			_ = enc.Encode(made)
		case r.Method == http.MethodGet && p == "/api/tracks":
			d.trackQueries = append(d.trackQueries, r.URL.RawQuery)
			if d.pages != nil {
				i, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
				var next any
				if i+1 < len(d.pages) {
					next = strconv.Itoa(i + 1)
				}
				_ = enc.Encode(map[string]any{"items": d.pages[i], "next_cursor": next})
				return
			}
			_ = enc.Encode(map[string]any{"items": d.tracks})
		case r.Method == http.MethodGet && strings.HasPrefix(p, "/api/tracks/trk-"):
			_ = enc.Encode(map[string]any{"id": "trk-1", "lineage": map[string]any{"operator": d.operator}})
		default:
			inner.ServeHTTP(w, r)
		}
	})
	return d
}

func planJobSeq() []map[string]any {
	j := job("completed", map[string]any{"outcome": "clean", "timings": map[string]any{"execution_ms": 20000, "cold_start_s": 95}})
	return []map[string]any{job("queued", nil), job("starting", nil), job("generating", nil), j}
}

func canaryTakes() []map[string]any {
	return []map[string]any{
		{"id": "trk-old", "request_id": "mm-canary-20261001-cot-full-1", "recorded_at": "2026-10-01T10:00:00Z"},
		{"id": "trk-1", "request_id": "mm-canary-20261010-cot-full-1", "recorded_at": "2026-10-10T10:00:00Z"},
		{"id": "trk-main", "request_id": "mm-canary-20261012-main-1", "recorded_at": "2026-10-12T10:00:00Z"},
		{"id": "trk-song", "request_id": "alice-song", "recorded_at": "2026-10-13T10:00:00Z"},
	}
}

// the dry run is the worst-case golden; each case stays under the default per-case --max-usd.
func TestCanary_DeskCases_DryRun(t *testing.T) {
	t.Parallel()
	p := runpod.DeskBudgetParams{}
	cs := runpod.DeskWorstCase(p)
	golden(t, "canary_worst_case_desk.golden", runpod.FormatDeskWorstCase(p, cs))
	if len(cs) != 2 || cs[0].Case != runpod.CasePlan || cs[1].Case != runpod.CasePlanRender {
		t.Fatalf("cases %+v", cs)
	}
	for _, c := range cs {
		if c.CeilingMicro <= 0 || c.CeilingMicro > runpod.DefaultCanaryMaxUSD {
			t.Errorf("%s ceiling %d is outside (0, %d]", c.Case, c.CeilingMicro, runpod.DefaultCanaryMaxUSD)
		}
	}
	if dear := runpod.DeskWorstCase(runpod.DeskBudgetParams{PricePPH: 2 * runpod.IllustrativePricePPH}); dear[0].CeilingMicro <= cs[0].CeilingMicro {
		t.Error("the plan ceiling did not rise with the price")
	}
	if got := runpod.FormatDeskWorstCase(p, cs); !strings.Contains(got, "written approval") {
		t.Errorf("the dry run does not name the written approval:\n%s", got)
	}
}

func TestParseCanaryCase_DeskNames(t *testing.T) {
	t.Parallel()
	for _, n := range []string{"plan", "plan-render"} {
		if c, err := runpod.ParseCanaryCase(n); err != nil || string(c) != n {
			t.Errorf("ParseCanaryCase(%q) = %q, %v", n, c, err)
		}
	}
	_, err := runpod.ParseCanaryCase("bogus")
	if err == nil || !strings.Contains(err.Error(), "plan or plan-render)") {
		t.Errorf("error %v", err)
	}
	if got := mustSpec(runpod.CasePlan).NeedCaps; len(got) != 2 || got[0] != "plan" || got[1] != "v2" {
		t.Errorf("plan needs %v", got)
	}
	if got := mustSpec(runpod.CasePlanRender).NeedCaps; len(got) != 0 {
		t.Errorf("plan-render needs %v: D-DESK-2 says none", got)
	}
}

func TestCanary_PlanCase(t *testing.T) {
	t.Parallel()
	t.Run("pass", func(t *testing.T) {
		t.Parallel()
		s := newDeskStub(t, []string{"plan", "v2"}, planJobSeq()...)
		rep := runEars(t, s.earsStub, runpod.CasePlan, nil)
		if rep.Exit != runpod.ExitPass {
			t.Fatalf("rep %+v", rep)
		}
		for _, name := range []string{"completed", "plans_stored", "score_parses", "plan_fields", "settled_le_reserved"} {
			if c, ok := checkOf(rep, name); !ok || !c.OK {
				t.Errorf("check %s = %+v", name, c)
			}
		}
		var body struct {
			Engine string
			Items  []struct {
				Mode   string
				Recipe map[string]any
				Desk   struct {
					NSeeds int `json:"n_seeds"`
					Cot    string
				}
			}
		}
		if err := json.Unmarshal(s.estimate, &body); err != nil || len(body.Items) != 1 || body.Items[0].Mode != "plan_seeds" ||
			body.Items[0].Desk.NSeeds != 2 || body.Items[0].Desk.Cot != "full" {
			t.Fatalf("estimate body %.300s", s.estimate)
		}
		if _, has := body.Items[0].Recipe["abc"]; has || body.Items[0].Recipe["cot"] != "full" {
			t.Errorf("the plan recipe must have cot=full and no abc: %v", body.Items[0].Recipe)
		}
		if rep.Measured["plans_stored"] != 2 || rep.Measured["abc_tokens"] != 240 || rep.Measured["plan_seconds_per_seed_milli"] != int64(10000) ||
			rep.Measured["abc_tokens_per_s_milli"] != int64(12000) {
			t.Errorf("measured %v", rep.Measured)
		}
	})
	t.Run("refused before any spend without the capabilities", func(t *testing.T) {
		t.Parallel()
		for _, caps := range [][]string{nil, {"v2"}, {"plan"}} {
			s := newDeskStub(t, caps, planJobSeq()...)
			rep := runEars(t, s.earsStub, runpod.CasePlan, nil)
			if rep.Exit != runpod.ExitRefused || !strings.Contains(rep.Reason, "not open") || !strings.Contains(rep.Reason, "--caps plan,v2") {
				t.Errorf("caps %v: rep %+v", caps, rep)
			}
			noSpend(t, s.earsStub)
		}
	})
	t.Run("ceiling above --max-usd is refused", func(t *testing.T) {
		t.Parallel()
		s := newDeskStub(t, []string{"plan", "v2"}, planJobSeq()...)
		s.ceiling = 600_000
		rep := runEars(t, s.earsStub, runpod.CasePlan, nil)
		if rep.Exit != runpod.ExitRefused || !strings.Contains(rep.Reason, "exceeds --max-usd") || s.count("POST /api/generations") != 0 {
			t.Errorf("rep %+v", rep)
		}
		rep = runEars(t, s.earsStub, runpod.CasePlan, func(o *runpod.CanaryOptions) { o.MaxUSD = money.MicroUSD(700_000) })
		if rep.Exit != runpod.ExitPass {
			t.Errorf("an approved higher ceiling should pass: %+v", rep)
		}
	})
	t.Run("no plan stored fails", func(t *testing.T) {
		t.Parallel()
		s := newDeskStub(t, []string{"plan", "v2"}, planJobSeq()...)
		s.plans = nil
		rep := runEars(t, s.earsStub, runpod.CasePlan, nil)
		if c, _ := checkOf(rep, "plans_stored"); rep.Exit != runpod.ExitFail || c.OK {
			t.Errorf("rep %+v", rep)
		}
	})
	t.Run("a score that does not parse or a missing field fails", func(t *testing.T) {
		t.Parallel()
		s := newDeskStub(t, []string{"plan", "v2"}, planJobSeq()...)
		s.plans = []map[string]any{deskPlanDoc("plan-1", map[string]any{"bars": 0, "meter": ""})}
		rep := runEars(t, s.earsStub, runpod.CasePlan, nil)
		a, _ := checkOf(rep, "score_parses")
		b, _ := checkOf(rep, "plan_fields")
		if rep.Exit != runpod.ExitFail || a.OK || b.OK {
			t.Errorf("rep %+v", rep)
		}
	})
}

func TestCanary_PlanRenderCase(t *testing.T) {
	t.Parallel()
	renderJob := func() []map[string]any {
		return []map[string]any{job("queued", nil), job("generating", nil), completedJob(earsArts("audio.flac"))}
	}
	decode := func(t *testing.T, s *deskStub) (mode string, ids []string) {
		t.Helper()
		var body struct {
			Items []struct {
				Mode string
				Desk struct {
					PlanIDs []string `json:"plan_ids"`
				}
			}
		}
		if err := json.Unmarshal(s.estimate, &body); err != nil || len(body.Items) != 1 {
			t.Fatalf("estimate body %.300s", s.estimate)
		}
		return body.Items[0].Mode, body.Items[0].Desk.PlanIDs
	}
	t.Run("renders the plan of the newest kept cot-full take with every capability closed", func(t *testing.T) {
		t.Parallel()
		s := newDeskStub(t, nil, renderJob()...)
		s.tracks = canaryTakes()
		rep := runEars(t, s.earsStub, runpod.CasePlanRender, nil)
		if rep.Exit != runpod.ExitPass {
			t.Fatalf("rep %+v", rep)
		}
		for _, name := range []string{"completed", "operator_render_plan", "plan_id_recorded", "audio_within_engine_max"} {
			if c, ok := checkOf(rep, name); !ok || !c.OK {
				t.Errorf("check %s = %+v", name, c)
			}
		}
		if mode, ids := decode(t, s); mode != "render_plan" || len(ids) != 1 || ids[0] != "plan-take" {
			t.Errorf("mode %q plan ids %v", mode, ids)
		}
		if len(s.made) != 1 || s.made[0] != "/api/tracks/trk-1/plan" {
			t.Errorf("the plan was made from %v, want the newest cot-full canary take trk-1", s.made)
		}
		if rep.Measured["plan_id"] != "plan-take" {
			t.Errorf("measured %v", rep.Measured)
		}
	})
	t.Run("--plan-id renders that plan and makes none", func(t *testing.T) {
		t.Parallel()
		s := newDeskStub(t, nil, renderJob()...)
		rep := runEars(t, s.earsStub, runpod.CasePlanRender, func(o *runpod.CanaryOptions) { o.PlanID = "plan-2" })
		if rep.Exit != runpod.ExitPass {
			t.Fatalf("rep %+v", rep)
		}
		if _, ids := decode(t, s); len(ids) != 1 || ids[0] != "plan-2" || len(s.made) != 0 {
			t.Errorf("ids %v made %v", ids, s.made)
		}
	})
	t.Run("refused before any spend", func(t *testing.T) {
		t.Parallel()
		s := newDeskStub(t, nil, renderJob()...) // no kept cot-full take
		s.tracks = canaryTakes()[2:]
		rep := runEars(t, s.earsStub, runpod.CasePlanRender, nil)
		if rep.Exit != runpod.ExitRefused || !strings.Contains(rep.Reason, "--plan-id") {
			t.Errorf("rep %+v", rep)
		}
		noSpend(t, s.earsStub)
		rep = runEars(t, s.earsStub, runpod.CasePlanRender, func(o *runpod.CanaryOptions) { o.PlanID = "plan-missing" })
		if rep.Exit != runpod.ExitRefused || !strings.Contains(rep.Reason, "could not be read") {
			t.Errorf("rep %+v", rep)
		}
		noSpend(t, s.earsStub)
	})
	t.Run("approval needed is refused before a batch exists", func(t *testing.T) {
		t.Parallel()
		s := newDeskStub(t, nil, renderJob()...)
		s.needApproval = true
		rep := runEars(t, s.earsStub, runpod.CasePlanRender, func(o *runpod.CanaryOptions) { o.PlanID = "plan-1" })
		if rep.Exit != runpod.ExitRefused || !strings.Contains(rep.Reason, "requires human approval") || s.count("POST /api/generations") != 0 {
			t.Errorf("rep %+v", rep)
		}
	})
	t.Run("the take is found on a later page; the search stops at the first page with a match", func(t *testing.T) {
		t.Parallel()
		s := newDeskStub(t, nil, renderJob()...)
		s.pages = [][]map[string]any{
			{{"id": "trk-song", "request_id": "alice-song", "recorded_at": "2026-10-13T10:00:00Z"}},
			{{"id": "trk-old", "request_id": "mm-canary-20261001-cot-full-1", "recorded_at": "2026-10-01T10:00:00Z"},
				{"id": "trk-1", "request_id": "mm-canary-20261010-cot-full-1", "recorded_at": "2026-10-10T10:00:00Z"}},
			{{"id": "trk-late", "request_id": "mm-canary-20261020-cot-full-1", "recorded_at": "2026-10-20T10:00:00Z"}},
		}
		rep := runEars(t, s.earsStub, runpod.CasePlanRender, nil)
		if rep.Exit != runpod.ExitPass || len(s.made) != 1 || !strings.Contains(s.made[0], "trk-1") || len(s.trackQueries) != 2 {
			t.Errorf("rep %+v made %v queries %v", rep, s.made, s.trackQueries)
		}
	})
	t.Run("a take that is not a render_plan, or not listed by the plan, fails", func(t *testing.T) {
		t.Parallel()
		s := newDeskStub(t, nil, renderJob()...)
		s.operator, s.renderedT = "compose_new", "trk-other"
		rep := runEars(t, s.earsStub, runpod.CasePlanRender, func(o *runpod.CanaryOptions) { o.PlanID = "plan-1" })
		a, _ := checkOf(rep, "operator_render_plan")
		b, _ := checkOf(rep, "plan_id_recorded")
		if rep.Exit != runpod.ExitFail || a.OK || b.OK {
			t.Errorf("rep %+v", rep)
		}
	})
}
