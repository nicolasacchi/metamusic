package runpod

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// The M5b Desk canary cases (C361.6, DESK-C1). Both spend real money and run only with the operator's written approval (the case list and a
// --max-usd per case); neither is ever run by an agent or by CI, whose tests use the scripted REST stub (TestCanary_DeskCases_DryRun).
const (
	// CasePlan is one plan job of 2 seeds (the canary recipe without abc, cot=full, a synthetic two-section "la la" lyric). It needs
	// capabilities plan and v2 open (`runpod contract open --caps plan,v2`) and is refused before any spend when the provider status
	// lacks one. It passes when at least one plan is stored, its score parses (yue2 dialect) with at least one bar and the plan has every
	// field; it records the seconds and ABC tokens per second of the planning rate (D-DESK-5).
	CasePlan CanaryCase = "plan"
	// CasePlanRender renders, through render_plan, the plan of a kept cot-full canary take (the newest one, or --plan-id). It runs on the
	// plain /1 wire and needs no capability (D-DESK-2). It passes when the take completes with operator render_plan and the plan lists
	// it among its renders (params.plan_id); it records the timings that decide whether the planning phase ran (D-DESK-4).
	CasePlanRender CanaryCase = "plan-render"
)

func isDeskCase(c CanaryCase) bool { return c == CasePlan || c == CasePlanRender }

// deskNeeds are the capabilities each Desk case needs open: plan_seeds needs the plan kind; render_plan needs none.
func deskNeeds(c CanaryCase) []string {
	if c == CasePlan {
		return []string{"plan", "v2"}
	}
	return nil
}

// planSeedsPerJob is the seed count of the plan case.
const planSeedsPerJob = 2

// deskLyrics are two synthetic sections of nothing a person wrote.
const deskLyrics = "[verse]\nla la la\nla la la\n\n[chorus]\nla la la\nla la la\n"

// newDeskSpec builds a Desk case's synthetic take. The plan case's recipe has no abc: the planner writes the score.
func newDeskSpec(c CanaryCase, day time.Time, n, bars int) (CanarySpec, error) {
	id := fmt.Sprintf("mm-canary-%s-%s-%d", day.UTC().Format("20060102"), c, n)
	req := yue2.Request{ID: id, Style: "instrumental, ambient, 80 BPM, no vocals", Lyrics: deskLyrics, Cot: "full", Seed: 1}
	raw, err := json.Marshal(req)
	if err != nil {
		return CanarySpec{}, err
	}
	secs, err := yue2.Engine{}.ExpectedAudioSeconds(raw)
	if err != nil {
		return CanarySpec{}, err
	}
	s := CanarySpec{Case: c, RequestID: id, Recipe: raw, Bars: bars, Cot: "full", ExpectedAudioS: secs, NeedCaps: deskNeeds(c)}
	switch c {
	case CasePlan:
		s.Engine = "yue2"
		s.Item = map[string]any{"mode": "plan_seeds", "recipe": json.RawMessage(raw),
			"desk": map[string]any{"n_seeds": planSeedsPerJob, "cot": "full"}}
		s.assert = func(c *canary, _ cProvider, j cJob, m map[string]any) { assertPlan(c, j, m) }
	case CasePlanRender:
		s.prepare = preparePlanRender
	}
	return s, nil
}

// cPlan is the part of get_plan the canary reads.
type cPlan struct {
	ID             string  `json:"id"`
	Source         string  `json:"source"`
	Seed           *int64  `json:"seed"`
	Cot            string  `json:"cot"`
	Bars           int     `json:"bars"`
	Meter          string  `json:"meter"`
	ExpectedAudioS int     `json:"expected_audio_s"`
	ABC            *string `json:"abc"`
	Score          struct {
		SHA256    string `json:"sha256"`
		Bytes     int    `json:"bytes"`
		Dialect   string `json:"dialect"`
		ABCTokens *int   `json:"abc_tokens"`
	} `json:"score"`
	Renders *[]struct {
		JobID   string  `json:"job_id"`
		TrackID *string `json:"track_id"`
	} `json:"renders"`
}

func (c *canary) readPlan(ctx context.Context, id, include string) (cPlan, error) {
	var p cPlan
	err := c.do(ctx, http.MethodGet, "/api/plans/"+url.PathEscape(id)+"?include="+include, nil, nil, &p)
	return p, err
}

// planComplete reports whether the plan carries every field the Desk reads.
func planComplete(p cPlan) bool {
	return p.ID != "" && p.Seed != nil && p.Cot != "" && p.Bars > 0 && p.Meter != "" && p.ExpectedAudioS > 0 &&
		p.Score.SHA256 != "" && p.Score.Bytes > 0 && p.Score.ABCTokens != nil
}

// assertPlan is the outcome of case plan (C361.6): the job completed, stored at least one plan, and the plans are well formed.
func assertPlan(c *canary, job cJob, meas map[string]any) {
	if !c.check("completed", job.State == "completed" && job.Outcome != nil && (*job.Outcome == "clean" || *job.Outcome == "truncated"), "state %s", job.State) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	err := c.do(ctx, http.MethodGet, "/api/plans?job_id="+url.QueryEscape(job.ID), nil, nil, &page)
	if !c.check("plans_stored", err == nil && len(page.Items) >= 1, "%d plans listed for the job (%v)", len(page.Items), errText(err)) {
		return
	}
	parsed, complete, tokens := 0, 0, 0
	for _, it := range page.Items {
		p, err := c.readPlan(ctx, it.ID, "abc")
		if err != nil {
			continue
		}
		if p.ABC != nil && *p.ABC != "" && p.Score.Dialect == "yue2" && p.Bars >= 1 {
			parsed++
		}
		if planComplete(p) {
			complete++
			tokens += *p.Score.ABCTokens
		}
	}
	c.check("score_parses", parsed >= 1, "%d of %d plans have a yue2 score with at least one bar", parsed, len(page.Items))
	c.check("plan_fields", complete == len(page.Items), "%d of %d plans carry every field", complete, len(page.Items))
	meas["plans_stored"] = len(page.Items)
	meas["abc_tokens"] = tokens
	if job.Timings.ExecutionMS != nil && *job.Timings.ExecutionMS > 0 {
		ms := *job.Timings.ExecutionMS
		meas["plan_seconds_per_seed_milli"] = ms / int64(len(page.Items)) // the planning rate (D-DESK-5), thousandths of a second
		meas["abc_tokens_per_s_milli"] = int64(tokens) * 1_000_000 / ms
	}
}

// cTrackRow is a search_tracks row.
type cTrackRow struct {
	ID         string `json:"id"`
	RequestID  string `json:"request_id"`
	RecordedAt string `json:"recorded_at"`
}

// newestCotFullTake finds the newest kept cot-full canary take with a score, or "". It pages through the scored job takes until a
// match is found or the results run out (at most deskTakePages pages), so later takes do not hide the canary take.
func (c *canary) newestCotFullTake(ctx context.Context) (string, error) {
	var hits []cTrackRow
	cursor := ""
	for range deskTakePages {
		var page struct {
			Items      []cTrackRow `json:"items"`
			NextCursor *string     `json:"next_cursor"`
		}
		path := "/api/tracks?origin=job&has_score=true&limit=100"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		if err := c.do(ctx, http.MethodGet, path, nil, nil, &page); err != nil {
			return "", err
		}
		for _, t := range page.Items {
			if strings.HasPrefix(t.RequestID, "mm-canary-") && strings.Contains(t.RequestID, "-"+string(CaseCotFull)+"-") {
				hits = append(hits, t)
			}
		}
		if len(hits) > 0 || page.NextCursor == nil || *page.NextCursor == "" {
			break
		}
		cursor = *page.NextCursor
	}
	if len(hits) == 0 {
		return "", nil
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].RecordedAt > hits[j].RecordedAt })
	return hits[0].ID, nil
}

// deskTakePages bounds the take search of newestCotFullTake.
const deskTakePages = 20

// preparePlanRender finds or makes the plan to render. Everything here is read-only or free (create_plan_from_take costs nothing); the
// only spend is the estimate that follows, bounded by --max-usd.
func preparePlanRender(ctx context.Context, c *canary, spec CanarySpec, _ cProvider) (CanarySpec, *CanaryReport) {
	planID := c.o.PlanID
	var plan cPlan
	var err error
	if planID != "" {
		if plan, err = c.readPlan(ctx, planID, "renders"); err != nil {
			return spec, c.finish("refused", ExitRefused, "plan %s could not be read (%s); nothing was spent", planID, errText(err))
		}
	} else {
		track, terr := c.newestCotFullTake(ctx)
		if terr != nil {
			return spec, c.finish("fail", ExitFail, "the take search failed (%v); nothing was spent", terr)
		}
		if track == "" {
			return spec, c.finish("refused", ExitRefused, "no kept cot-full canary take with a score: run `canary --case cot-full --keep` first or pass --plan-id; nothing was spent")
		}
		hdr := map[string]string{"Idempotency-Key": "canary-plan-" + track}
		if err = c.do(ctx, http.MethodPost, "/api/tracks/"+url.PathEscape(track)+"/plan", hdr, map[string]any{}, &plan); err != nil {
			return spec, c.finish("refused", ExitRefused, "the take's plan could not be made (%v); nothing was spent", err)
		}
		planID = plan.ID
	}
	if planID == "" {
		return spec, c.finish("refused", ExitRefused, "the plan has no id; nothing was spent")
	}
	if c.rep.Measured == nil {
		c.rep.Measured = map[string]any{}
	}
	c.rep.Measured["plan_id"] = planID
	spec.Engine = "yue2"
	spec.ExpectedAudioS = plan.ExpectedAudioS
	spec.Item = map[string]any{"mode": "render_plan", "desk": map[string]any{"plan_ids": []string{planID}}}
	spec.assert = func(c *canary, _ cProvider, j cJob, m map[string]any) { assertPlanRender(c, spec, planID, j, m) }
	return spec, nil
}

// assertPlanRender is the outcome of case plan-render: a completed render_plan take that the plan lists, plus the timings that show
// whether the planning phase ran (the evidence for D-DESK-4).
func assertPlanRender(c *canary, spec CanarySpec, planID string, job cJob, meas map[string]any) {
	if !c.check("completed", job.State == "completed" && job.Outcome != nil && *job.Outcome == "clean", "state %s", job.State) {
		return
	}
	c.check("artifacts_verified", allVerified(job), "%d artifacts", len(job.Artifacts))
	c.auditAudio(spec, job, meas)
	if job.TrackID == nil || *job.TrackID == "" {
		c.check("operator_render_plan", false, "the job has no track")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var tr struct {
		Lineage struct {
			Operator *string `json:"operator"`
		} `json:"lineage"`
	}
	err := c.do(ctx, http.MethodGet, "/api/tracks/"+url.PathEscape(*job.TrackID), nil, nil, &tr)
	op := ""
	if tr.Lineage.Operator != nil {
		op = *tr.Lineage.Operator
	}
	c.check("operator_render_plan", err == nil && op == "render_plan", "operator %q (%s)", op, errText(err))
	p, err := c.readPlan(ctx, planID, "renders")
	listed := false
	if err == nil && p.Renders != nil {
		for _, r := range *p.Renders {
			listed = listed || (r.TrackID != nil && *r.TrackID == *job.TrackID)
		}
	}
	c.check("plan_id_recorded", listed, "the plan lists the take among its renders (params.plan_id) (%s)", errText(err))
	meas["plan_expected_audio_s"] = spec.ExpectedAudioS
	if len(job.Timings.Extra) > 0 {
		meas["timings_extra"] = job.Timings.Extra // the planning phase evidence, when the worker reports phases
	}
}

// ---- dry run ---------------------------------------------------------------------------------------------------------------------

// DeskBudgetParams are the inputs of the Desk cases' worst case; zero values are the defaults of the quote.
type DeskBudgetParams struct {
	PricePPH    money.MicroUSD // default IllustrativePricePPH
	ColdStartS  int            // default 180
	IdleS       int            // default provisionIdleS
	KillSlackS  int            // default 15
	MaxRuntimeS int            // MM_JOB_MAX_RUNTIME, default 900
	WorkersMax  int            // default 2
	RenderS     int            // the songwriting minimum the plan-render take is forced to, default 180
}

// DeskBudgetCase is one case's worst case: the estimate's ceiling (what --max-usd is compared with) and its worker-seconds.
type DeskBudgetCase struct {
	Case         CanaryCase
	Needs        []string
	Stage        string
	ExpectedS    int
	TimeoutS     int
	CeilingMicro money.MicroUSD
}

// DeskWorstCase prices both Desk cases with the server's own quote arithmetic at an illustrative price. It reads nothing and spends
// nothing; the figure the operator approves comes from this run at the live price, and the real estimate is checked against --max-usd
// again before any spend.
func DeskWorstCase(p DeskBudgetParams) []DeskBudgetCase {
	if p.PricePPH <= 0 {
		p.PricePPH = IllustrativePricePPH
	}
	p.ColdStartS, p.IdleS, p.KillSlackS = orInt(p.ColdStartS, 180), orInt(p.IdleS, provisionIdleS), orInt(p.KillSlackS, 15)
	p.MaxRuntimeS, p.WorkersMax, p.RenderS = orInt(p.MaxRuntimeS, 900), orInt(p.WorkersMax, 2), orInt(p.RenderS, 180)
	qp := spend.QuoteParams{PricePPH: p.PricePPH, RTFMilli: spend.DefaultRTFMilli, ColdStartS: p.ColdStartS, IdleS: p.IdleS,
		WorkersMax: p.WorkersMax, MaxRuntimeS: p.MaxRuntimeS, KillSlackS: p.KillSlackS}
	const minAudioS = 30 // the engine's clamp of the synthetic plan recipe
	quote := func(t spend.TakeIn) DeskBudgetCase {
		q := spend.QuoteTakes(qp, []spend.TakeIn{t})
		return DeskBudgetCase{ExpectedS: q.Takes[0].ExpectedS, TimeoutS: q.Takes[0].TimeoutS, CeilingMicro: q.CeilingMicro}
	}
	plan := quote(spend.TakeIn{AudioS: planSeedsPerJob * minAudioS, RTFMilli: spend.PlanMilli, LoadS: spend.LoadS})
	plan.Case, plan.Needs, plan.Stage = CasePlan, deskNeeds(CasePlan), "plan job of 2 seeds"
	render := quote(spend.TakeIn{AudioS: p.RenderS, Full: true}) // conservative: the planning phase charged (DeskRenderSkipsPlan false)
	render.Case, render.Needs, render.Stage = CasePlanRender, deskNeeds(CasePlanRender), "render_plan take of a kept cot-full take's plan, unless --plan-id"
	return []DeskBudgetCase{plan, render}
}

// FormatDeskWorstCase renders the dry run (golden TestCanary_DeskCases_DryRun).
func FormatDeskWorstCase(p DeskBudgetParams, cs []DeskBudgetCase) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Desk canary worst case (illustrative price USD %s per hour; the real estimate is checked against --max-usd before any spend)\n",
		orMoney(p.PricePPH).USD())
	var sum money.MicroUSD
	for _, c := range cs {
		needs := strings.Join(c.Needs, ",")
		if needs == "" {
			needs = "none"
		}
		fmt.Fprintf(&b, "case %-11s ceiling USD %s  expected %d s  timeout %d s  needs %s  stage: %s\n", c.Case, c.CeilingMicro.USD(), c.ExpectedS, c.TimeoutS, needs, c.Stage)
		sum += c.CeilingMicro
	}
	fmt.Fprintf(&b, "sum of both cases USD %s; default per-case --max-usd USD %s\n", sum.USD(), DefaultCanaryMaxUSD.USD())
	b.WriteString("These canaries spend real money: run them only with the operator's written approval (case list and --max-usd per case).\n")
	return b.String()
}
