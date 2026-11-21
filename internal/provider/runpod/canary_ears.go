package runpod

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// The M5b Ears canary cases (C341.1), worker wave W2. Both spend real money, need the capabilities opened with `metamusic runpod contract
// open --caps v2,ears,analyze` right before the approved run, and refuse before any spend when the provider status does not list what the
// case needs (like v2-wire). Each is bounded by --max-usd (default DefaultCanaryMaxUSD).
const (
	// CaseTierB is a render with synthetic sung lyrics (generic phrases), an ABC score (so the pitch stage runs) and options.tier_b true,
	// forced to the songwriting minimum like main. It passes when the take completes, analysis.json has status ok and three ok stages, the
	// tier_b_raw row exists, measured sections exist or the flag is alignment_failed, and the measurements for calibration are present.
	CaseTierB CanaryCase = "tier-b"
	// CaseAnalyze is an analyze item for the take that case main rendered in the same run (or --track): the job must complete on that take
	// with an analysis artifact and a tier_b_raw row. Without --track the run first spends one main render; --max-usd then bounds the
	// sum of both estimate ceilings.
	CaseAnalyze CanaryCase = "analyze"
)

func isEarsCase(c CanaryCase) bool { return c == CaseTierB || c == CaseAnalyze }

// earsNeeds are the capabilities each case needs open.
func earsNeeds(c CanaryCase) []string {
	if c == CaseAnalyze {
		return []string{"v2", "ears", "analyze"}
	}
	return []string{"v2", "ears"}
}

// earsLyrics are generic phrases: nothing a person wrote.
const earsLyrics = "[verse]\nsoft light on the water\nwalking home in the rain\n\n[chorus]\nsing it again\nsing it again\n"

// newEarsSpec builds a case's synthetic take. tier-b and analyze share the sung recipe: analyze renders it (as case main would) only
// when it has no --track, and then checks that take.
func newEarsSpec(c CanaryCase, day time.Time, n, bars int) (CanarySpec, error) {
	abc := abcScore(bars)
	id := fmt.Sprintf("mm-canary-%s-%s-%d", day.UTC().Format("20060102"), c, n)
	raw, err := json.Marshal(yue2.Request{ID: id, Style: "pop, female vocal, 80 BPM", Lyrics: earsLyrics, Cot: "melody", Seed: 1, ABC: &abc})
	if err != nil {
		return CanarySpec{}, err
	}
	secs, err := yue2.Engine{}.ExpectedAudioSeconds(raw)
	if err != nil {
		return CanarySpec{}, err
	}
	s := CanarySpec{Case: c, RequestID: id, Recipe: raw, Bars: bars, Cot: "melody", ExpectedAudioS: secs, NeedCaps: earsNeeds(c)}
	switch c {
	case CaseTierB:
		s.Options = map[string]any{"tier_b": true}
		s.assert = assertTierB
	case CaseAnalyze:
		s.prepare = prepareAnalyze
	}
	return s, nil
}

// earsRefusal is the capability gate of the Ears cases: the first line of defence before an estimate (the server's own gate is the second).
func earsRefusal(spec CanarySpec, prov cProvider) string {
	var missing []string
	for _, cp := range spec.NeedCaps {
		if !prov.hasCap(cp) {
			missing = append(missing, cp)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf("capabilities %s are not open for this backend's image (open them with `metamusic runpod contract open --caps %s` right before this case, after the operator's written approval)",
		strings.Join(missing, ", "), strings.Join(spec.NeedCaps, ","))
}

// prepareAnalyze finds the take to analyze: --track, or a main render of this run (kept, discarded with the analyze job unless --keep).
// --max-usd bounds the sum of the estimate ceilings, so the analyze estimate is compared with what the main render left.
func prepareAnalyze(ctx context.Context, c *canary, spec CanarySpec, prov cProvider) (CanarySpec, *CanaryReport) {
	track := c.o.SourceTrack
	if track != "" {
		c.keepStage = true // the operator's own take is never discarded
	} else {
		mainSpec, err := NewCanarySpec(CaseMain, c.o.Day, c.o.N)
		if err != nil {
			return spec, c.finish("fail", ExitFail, "the source render could not be built: %v", err)
		}
		so := c.o
		so.Case, so.Keep = CaseMain, true
		sub := &canary{o: so, hc: c.hc, rep: &CanaryReport{Case: CaseMain, RequestID: mainSpec.RequestID}, prefix: "source."}
		rep, err := sub.run(ctx, mainSpec)
		if err != nil {
			return spec, c.finish("fail", ExitFail, "the source render (case main) failed before it finished: %v", err)
		}
		c.rep.Checks = append(c.rep.Checks, rep.Checks...)
		if rep.Exit != ExitPass || sub.lastJob.TrackID == nil || *sub.lastJob.TrackID == "" {
			return spec, c.finish(rep.Outcome, max(rep.Exit, ExitFail), "the source render (case main) did not pass: %s", rep.Reason)
		}
		track = *sub.lastJob.TrackID
		c.o.MaxUSD -= money.MicroUSD(rep.Ceiling)
		if c.o.MaxUSD <= 0 {
			return spec, c.finish("refused", ExitRefused, "the source render's ceiling %s used up --max-usd; nothing more was spent", money.MicroUSD(rep.Ceiling).USD())
		}
		c.logf("source take %s rendered; the analyze estimate must fit %s", track, c.o.MaxUSD.USD())
	}
	spec.Engine = "yue2"
	spec.Item = map[string]any{"mode": "analyze", "track_ids": []string{track}}
	spec.assert = func(c *canary, _ cProvider, job cJob, meas map[string]any) { assertAnalyze(c, track, job, meas) }
	return spec, nil
}

// cTierB is the part of get_track_tier_b the canary reads.
type cTierB struct {
	State  string `json:"state"`
	Status string `json:"status"`
	Stages struct {
		Separate, ASR, Transcribe string
	} `json:"stages"`
	Timings struct {
		LoadS       json.Number `json:"load_s"`
		SeparateS   json.Number `json:"separate_s"`
		ASRS        json.Number `json:"asr_s"`
		TranscribeS json.Number `json:"transcribe_s"`
		TotalS      json.Number `json:"total_s"`
	} `json:"timings"`
	Resources struct {
		PeakVRAMMiB int `json:"peak_vram_mib"`
	} `json:"resources"`
	MeasuredSections []json.RawMessage `json:"measured_sections"`
	Flags            []struct {
		Code string `json:"code"`
	} `json:"flags"`
}

// readTierB reads the stored listening check of a take (the tier_b_raw row behind it): 200 or an error.
func (c *canary) readTierB(track string) (cTierB, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var v cTierB
	err := c.do(ctx, http.MethodGet, "/api/tracks/"+url.PathEscape(track)+"/tier-b", nil, nil, &v)
	return v, err
}

func allVerified(job cJob) bool {
	for _, a := range job.Artifacts {
		if !a.Verified {
			return false
		}
	}
	return len(job.Artifacts) > 0
}

func hasArtifact(job cJob, name string) bool {
	for _, a := range job.Artifacts {
		if a.Name == name && a.Verified {
			return true
		}
	}
	return false
}

// recordEars puts the numbers the operator calibrates MM_EARS_LOAD_S and MM_EARS_RTF_MILLI from into the report (printed as JSON). The
// worker's seconds stay json.Number: this package has no floats (money and times are integers).
func recordEars(meas map[string]any, v cTierB, audio json.Number) {
	meas["ears_seconds"] = v.Timings.TotalS
	meas["ears_timings"] = map[string]json.Number{"load_s": v.Timings.LoadS, "separate_s": v.Timings.SeparateS, "asr_s": v.Timings.ASRS,
		"transcribe_s": v.Timings.TranscribeS, "total_s": v.Timings.TotalS}
	meas["peak_vram_mib"] = v.Resources.PeakVRAMMiB
	load, okL := milliOf(v.Timings.LoadS)
	if okL {
		meas["ears_load_s"] = (load + 999) / 1000 // MM_EARS_LOAD_S
	}
	sep, okS := milliOf(v.Timings.SeparateS)
	asr, okA := milliOf(v.Timings.ASRS)
	if am, ok := milliOf(audio); ok && am > 0 && okS && okA {
		meas["ears_rtf_milli"] = (sep + asr) * 1000 / am // MM_EARS_RTF_MILLI: separation plus recognition per 1000 audio seconds
	}
}

func audioNumber(job cJob) json.Number {
	if job.AudioS == nil {
		return ""
	}
	return *job.AudioS
}

// assertTierB is the outcome of case tier-b (C341.1).
func assertTierB(c *canary, _ cProvider, job cJob, meas map[string]any) {
	if !c.check("completed", job.State == "completed" && job.Outcome != nil && *job.Outcome == "clean", "state %s", job.State) {
		return
	}
	c.check("artifacts_verified", allVerified(job), "%d artifacts", len(job.Artifacts))
	c.check("analysis_artifact", hasArtifact(job, "analysis.json"), "analysis.json among the verified artifacts")
	c.auditAudio(CanarySpec{}, job, meas)
	if job.TrackID == nil || *job.TrackID == "" {
		c.check("tier_b_raw", false, "the job has no track")
		return
	}
	v, err := c.readTierB(*job.TrackID)
	if !c.check("tier_b_raw", err == nil, "get_track_tier_b answered %v", errText(err)) {
		return
	}
	c.check("analysis_ok", v.Status == "ok" && v.State == "ok", "status %s, state %s", v.Status, v.State)
	c.check("stages_ok", v.Stages.Separate == tierb.StageOK && v.Stages.ASR == tierb.StageOK && v.Stages.Transcribe == tierb.StageOK,
		"separate %s, asr %s, transcribe %s", v.Stages.Separate, v.Stages.ASR, v.Stages.Transcribe)
	aligned := len(v.MeasuredSections) > 0
	failed := false
	for _, f := range v.Flags {
		failed = failed || f.Code == "alignment_failed"
	}
	c.check("measured_sections_or_flag", aligned || failed, "%d measured sections, alignment_failed flag %v", len(v.MeasuredSections), failed)
	totalMS, _ := milliOf(v.Timings.TotalS)
	c.check("measurements_recorded", totalMS > 0 && v.Resources.PeakVRAMMiB > 0, "ears %s s, peak VRAM %d MiB", v.Timings.TotalS.String(), v.Resources.PeakVRAMMiB)
	recordEars(meas, v, audioNumber(job))
}

// assertAnalyze is the outcome of case analyze (C341.1): the job completed on the chosen take with a document and a stored raw row.
func assertAnalyze(c *canary, track string, job cJob, meas map[string]any) {
	if !c.check("completed", job.State == "completed" && job.Outcome != nil && *job.Outcome == "clean", "state %s", job.State) {
		return
	}
	c.check("on_chosen_take", job.TrackID != nil && *job.TrackID == track, "the job's track is the one asked for")
	c.check("analysis_artifact", hasArtifact(job, "analysis.json"), "analysis.json among the verified artifacts")
	v, err := c.readTierB(track)
	if !c.check("tier_b_raw", err == nil, "get_track_tier_b answered %v", errText(err)) {
		return
	}
	c.check("analysis_ok", v.Status == "ok", "status %s", v.Status)
	recordEars(meas, v, "")
}

func errText(err error) string {
	if err == nil {
		return "200"
	}
	return err.Error()
}

// ---- dry run ---------------------------------------------------------------------------------------------------------------------

// EarsBudgetParams are the inputs of the Ears cases' worst case; zero values are the defaults and placeholders of the quote.
type EarsBudgetParams struct {
	PricePPH    money.MicroUSD // default IllustrativePricePPH
	ColdStartS  int            // default 180
	IdleS       int            // default provisionIdleS
	KillSlackS  int            // default 15
	MaxRuntimeS int            // MM_JOB_MAX_RUNTIME, default 900
	WorkersMax  int            // default 2
	RenderS     int            // the songwriting minimum the canary render is forced to, default 180
}

// EarsBudgetCase is one case's worst case: the sum of its estimate ceilings (what --max-usd is compared with).
type EarsBudgetCase struct {
	Case         CanaryCase
	Needs        []string
	Stages       []string
	CeilingMicro money.MicroUSD
}

// EarsWorstCase prices both Ears cases at the placeholder rates (MM_EARS_*, MM_ANALYZE_FETCH_S defaults) and an illustrative price, with
// the server's own quote arithmetic. It reads nothing and spends nothing; the figure the operator approves comes from a run of the same
// arithmetic at the live price (`--dry-run`), and the real estimate is checked against --max-usd again before any spend.
func EarsWorstCase(p EarsBudgetParams) []EarsBudgetCase {
	if p.PricePPH <= 0 {
		p.PricePPH = IllustrativePricePPH
	}
	p.ColdStartS, p.IdleS, p.KillSlackS = orInt(p.ColdStartS, 180), orInt(p.IdleS, provisionIdleS), orInt(p.KillSlackS, 15)
	p.MaxRuntimeS, p.WorkersMax, p.RenderS = orInt(p.MaxRuntimeS, 900), orInt(p.WorkersMax, 2), orInt(p.RenderS, 180)
	qp := spend.QuoteParams{PricePPH: p.PricePPH, RTFMilli: spend.DefaultRTFMilli, ColdStartS: p.ColdStartS, IdleS: p.IdleS,
		WorkersMax: p.WorkersMax, MaxRuntimeS: p.MaxRuntimeS, KillSlackS: p.KillSlackS}
	const earsLoadS, earsRTFMilli, earsTrRTFMilli, analyzeFetchS = 40, 150, 40, 20
	ceiling := func(t spend.TakeIn) money.MicroUSD { return spend.QuoteTakes(qp, []spend.TakeIn{t}).CeilingMicro }
	render := spend.TakeIn{AudioS: p.RenderS, EarsS: spend.EarsExpectedS(p.RenderS, earsLoadS, earsRTFMilli, earsTrRTFMilli, true)}
	plain := spend.TakeIn{AudioS: p.RenderS}
	analyze := spend.TakeIn{LoadS: analyzeFetchS, EarsS: spend.EarsExpectedS(p.RenderS, earsLoadS, earsRTFMilli, earsTrRTFMilli, false)}
	return []EarsBudgetCase{
		{Case: CaseTierB, Needs: earsNeeds(CaseTierB), Stages: []string{"render with listening check"}, CeilingMicro: ceiling(render)},
		{Case: CaseAnalyze, Needs: earsNeeds(CaseAnalyze), Stages: []string{"source render (main), unless --track", "analyze"},
			CeilingMicro: ceiling(plain) + ceiling(analyze)},
	}
}

func orInt(v, d int) int {
	if v > 0 {
		return v
	}
	return d
}

// FormatEarsWorstCase renders the dry run (golden TestCanary_EarsCasesDryRunGolden).
func FormatEarsWorstCase(p EarsBudgetParams, cs []EarsBudgetCase) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Ears canary worst case (illustrative price USD %s per hour; the real estimate is checked against --max-usd before any spend)\n",
		orMoney(p.PricePPH).USD())
	var sum money.MicroUSD
	for _, c := range cs {
		fmt.Fprintf(&b, "case %-8s ceiling USD %s  needs %s  stages: %s\n", c.Case, c.CeilingMicro.USD(), strings.Join(c.Needs, ","), strings.Join(c.Stages, "; "))
		sum += c.CeilingMicro
	}
	fmt.Fprintf(&b, "sum of both cases USD %s; default per-case --max-usd USD %s\n", sum.USD(), DefaultCanaryMaxUSD.USD())
	b.WriteString("These canaries spend real money: run them only with the operator's written approval (case list and --max-usd per case).\n")
	return b.String()
}

func orMoney(m money.MicroUSD) money.MicroUSD {
	if m <= 0 {
		return IllustrativePricePPH
	}
	return m
}
