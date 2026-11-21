package runpod_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
)

// earsStub is the scripted REST server of the Ears cases: the plain stub plus get_track_tier_b. The provider shows one worker while
// the scripted job is generating and the open capabilities caps; nothing here reaches a real server.
type earsStub struct {
	*restStub
	tierB      map[string]any // the get_track_tier_b body; nil answers 409 ANALYSIS_UNAVAILABLE
	tierBCalls []string
}

func tierBDoc(over map[string]any) map[string]any {
	d := map[string]any{"state": "ok", "status": "ok",
		"stages":            map[string]any{"separate": "ok", "asr": "ok", "transcribe": "ok"},
		"timings":           map[string]any{"load_s": 31.2, "separate_s": 9.5, "asr_s": 12.0, "transcribe_s": 2.0, "total_s": 54.7},
		"resources":         map[string]any{"device": "gpu", "peak_vram_mib": 9100},
		"measured_sections": []any{map[string]any{"idx": 0, "label": "verse"}}, "flags": []any{}}
	for k, v := range over {
		d[k] = v
	}
	return d
}

func newEarsStub(t *testing.T, caps []string, tb map[string]any, jobs ...map[string]any) *earsStub {
	t.Helper()
	s := &earsStub{restStub: newRestStub(t, jobs...), tierB: tb}
	inner := s.Config.Handler
	s.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tier-b"):
			s.mu.Lock()
			s.tierBCalls = append(s.tierBCalls, r.URL.Path)
			tb := s.tierB
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if tb == nil {
				w.WriteHeader(409)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": "ANALYSIS_UNAVAILABLE", "message": "none"})
				return
			}
			_ = json.NewEncoder(w).Encode(tb)
		case r.Method == http.MethodGet && r.URL.Path == "/api/provider":
			s.mu.Lock()
			state := ""
			if s.waits > 0 {
				state, _ = s.jobs[min(s.waits-1, len(s.jobs)-1)]["state"].(string)
			}
			s.mu.Unlock()
			p := withWorkers(0)
			if state == "generating" {
				p = withWorkers(1)
			}
			p["worker"] = map[string]any{"image_digest": nil, "caps": caps, "changed_at": nil, "via": "none"}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(p)
		default:
			inner.ServeHTTP(w, r)
		}
	})
	return s
}

func earsArts(names ...string) []any {
	var out []any
	for _, n := range names {
		out = append(out, map[string]any{"name": n, "verified": true})
	}
	return out
}

func completedJob(arts []any) map[string]any {
	return job("completed", map[string]any{"outcome": "clean", "track_id": "trk-1", "audio_s": 180.5, "artifacts": arts,
		"timings": map[string]any{"execution_ms": 141000, "cold_start_s": 95}})
}

func renderSeq(arts []any) []map[string]any {
	return []map[string]any{job("queued", nil), job("starting", nil), job("generating", nil), completedJob(arts)}
}

var allCaps = []string{"v2", "ears", "analyze"}

func runEars(t *testing.T, s *earsStub, c runpod.CanaryCase, mod func(*runpod.CanaryOptions)) *runpod.CanaryReport {
	t.Helper()
	o := canaryOpts(s.restStub, c)
	if mod != nil {
		mod(&o)
	}
	rep, err := runpod.RunCanary(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func noSpend(t *testing.T, s *earsStub) {
	t.Helper()
	if s.count("POST /api/generations/estimate") != 0 || s.count("POST /api/generations") != 0 {
		t.Error("the canary went past the closed gate")
	}
}

func TestCanary_TierBCase(t *testing.T) {
	t.Parallel()
	good := func() *earsStub {
		return newEarsStub(t, allCaps, tierBDoc(nil), renderSeq(earsArts("audio.flac", "analysis.json"))...)
	}
	t.Run("pass", func(t *testing.T) {
		t.Parallel()
		s := good()
		rep := runEars(t, s, runpod.CaseTierB, nil)
		if rep.Exit != runpod.ExitPass {
			t.Fatalf("rep %+v", rep)
		}
		for _, name := range []string{"completed", "analysis_artifact", "tier_b_raw", "analysis_ok", "stages_ok", "measured_sections_or_flag", "measurements_recorded"} {
			if c, ok := checkOf(rep, name); !ok || !c.OK {
				t.Errorf("check %s = %+v", name, c)
			}
		}
		var body struct {
			Engine  string
			Options map[string]any
			Items   []struct{ Mode, Title string }
		}
		if err := json.Unmarshal(s.estimate, &body); err != nil || body.Options["tier_b"] != true || len(body.Items) != 1 || body.Items[0].Mode != "compose_new" {
			t.Errorf("estimate body %.300s", s.estimate)
		}
		if !strings.Contains(string(s.estimate), "mm-canary-20261014-tier-b-1") || !strings.Contains(string(s.estimate), "[chorus]") {
			t.Errorf("recipe not sent: %.300s", s.estimate)
		}
		if rep.Measured["ears_seconds"] != json.Number("54.7") || rep.Measured["peak_vram_mib"] != 9100 || rep.Measured["ears_timings"] == nil {
			t.Errorf("measured %+v", rep.Measured)
		}
		if rep.Measured["ears_rtf_milli"] != int64(119) || rep.Measured["ears_load_s"] != int64(32) {
			t.Errorf("calibration numbers %+v", rep.Measured)
		}
		if e, ok := rep.Measured["audio_expected_s"]; !ok || e == nil || e == 0 {
			t.Errorf("audio_expected_s missing: %+v", rep.Measured)
		}
		if len(s.patches) != 1 {
			t.Errorf("the canary take was not discarded: %v", s.patches)
		}
	})
	for name, caps := range map[string][]string{"nothing open": nil, "ears closed": {"v2"}} {
		t.Run("refused: "+name, func(t *testing.T) {
			t.Parallel()
			s := newEarsStub(t, caps, tierBDoc(nil), renderSeq(nil)...)
			rep := runEars(t, s, runpod.CaseTierB, nil)
			if rep.Exit != runpod.ExitRefused || !strings.Contains(rep.Reason, "contract open --caps v2,ears") || !strings.Contains(rep.Reason, "written approval") {
				t.Fatalf("rep %+v", rep)
			}
			noSpend(t, s)
		})
	}
	t.Run("a ceiling over --max-usd is refused before create", func(t *testing.T) {
		t.Parallel()
		s := good()
		s.ceiling = 800_000
		rep := runEars(t, s, runpod.CaseTierB, func(o *runpod.CanaryOptions) { o.MaxUSD = runpod.DefaultCanaryMaxUSD })
		if rep.Exit != runpod.ExitRefused || s.count("POST /api/generations") != 0 {
			t.Fatalf("rep %+v", rep)
		}
	})
	fails := map[string]struct {
		tb    map[string]any
		arts  []any
		check string
	}{
		"document missing":     {tierBDoc(nil), earsArts("audio.flac"), "analysis_artifact"},
		"status error":         {tierBDoc(map[string]any{"status": "error"}), earsArts("analysis.json"), "analysis_ok"},
		"a stage skipped":      {tierBDoc(map[string]any{"stages": map[string]any{"separate": "ok", "asr": "ok", "transcribe": "skipped"}}), earsArts("analysis.json"), "stages_ok"},
		"no measured sections": {tierBDoc(map[string]any{"measured_sections": nil}), earsArts("analysis.json"), "measured_sections_or_flag"},
		"no vram":              {tierBDoc(map[string]any{"resources": map[string]any{"peak_vram_mib": 0}}), earsArts("analysis.json"), "measurements_recorded"},
		"no raw row":           {nil, earsArts("analysis.json"), "tier_b_raw"},
	}
	for name, f := range fails {
		t.Run("fail: "+name, func(t *testing.T) {
			t.Parallel()
			rep := runEars(t, newEarsStub(t, allCaps, f.tb, renderSeq(f.arts)...), runpod.CaseTierB, nil)
			if c, _ := checkOf(rep, f.check); rep.Exit != runpod.ExitFail || c.OK {
				t.Fatalf("rep %+v check %+v", rep, c)
			}
		})
	}
	t.Run("alignment_failed stands in for measured sections", func(t *testing.T) {
		t.Parallel()
		tb := tierBDoc(map[string]any{"measured_sections": nil, "flags": []any{map[string]any{"code": "alignment_failed"}}})
		rep := runEars(t, newEarsStub(t, allCaps, tb, renderSeq(earsArts("analysis.json"))...), runpod.CaseTierB, nil)
		if rep.Exit != runpod.ExitPass {
			t.Fatalf("rep %+v", rep)
		}
	})
}

// analyzeSeq is the source render of case main followed by the analyze job of the same run.
func analyzeSeq(arts []any, track string) []map[string]any {
	main := renderSeq(earsArts("audio.flac"))
	an := completedJob(arts)
	an["track_id"] = track
	return append(main, job("generating", nil), an)
}

func TestCanary_AnalyzeCase(t *testing.T) {
	t.Parallel()
	t.Run("pass on --track keeps the operator's take", func(t *testing.T) {
		t.Parallel()
		s := newEarsStub(t, allCaps, tierBDoc(nil), []map[string]any{job("generating", nil), func() map[string]any {
			j := completedJob(earsArts("analysis.json"))
			j["track_id"] = "trk-9"
			return j
		}()}...)
		rep := runEars(t, s, runpod.CaseAnalyze, func(o *runpod.CanaryOptions) { o.SourceTrack = "trk-9" })
		if rep.Exit != runpod.ExitPass {
			t.Fatalf("rep %+v", rep)
		}
		for _, name := range []string{"completed", "on_chosen_take", "analysis_artifact", "tier_b_raw", "analysis_ok"} {
			if c, ok := checkOf(rep, name); !ok || !c.OK {
				t.Errorf("check %s = %+v", name, c)
			}
		}
		var body struct {
			Engine  string
			Options map[string]any
			Items   []struct {
				Mode     string
				TrackIDs []string `json:"track_ids"`
			}
		}
		if err := json.Unmarshal(s.estimate, &body); err != nil || body.Engine != "yue2" || len(body.Items) != 1 || body.Items[0].Mode != "analyze" ||
			len(body.Items[0].TrackIDs) != 1 || body.Items[0].TrackIDs[0] != "trk-9" || body.Options != nil {
			t.Errorf("estimate body %.300s", s.estimate)
		}
		if len(s.patches) != 0 || s.count("POST /api/generations") != 1 {
			t.Errorf("patches %v generations %d", s.patches, s.count("POST /api/generations"))
		}
	})
	t.Run("pass after a main render of the same run", func(t *testing.T) {
		t.Parallel()
		s := newEarsStub(t, allCaps, tierBDoc(nil), analyzeSeq(earsArts("analysis.json"), "trk-1")...)
		s.ceiling = 200_000
		rep := runEars(t, s, runpod.CaseAnalyze, nil)
		if rep.Exit != runpod.ExitPass {
			t.Fatalf("rep %+v", rep)
		}
		if c, ok := checkOf(rep, "source.completed"); !ok || !c.OK {
			t.Errorf("the source render's checks are missing: %+v", rep.Checks)
		}
		if s.count("POST /api/generations") != 2 || len(s.patches) != 1 || !strings.Contains(s.patches[0], "trk-1") {
			t.Errorf("generations %d patches %v (the source take is discarded after the analysis)", s.count("POST /api/generations"), s.patches)
		}
		if !strings.Contains(string(s.estimate), `"analyze"`) {
			t.Errorf("last estimate is not the analyze item: %.200s", s.estimate)
		}
	})
	t.Run("the sum of both ceilings is bounded by --max-usd", func(t *testing.T) {
		t.Parallel()
		s := newEarsStub(t, allCaps, tierBDoc(nil), analyzeSeq(earsArts("analysis.json"), "trk-1")...)
		s.ceiling = 300_000 // each estimate fits 500 000 alone, both do not
		rep := runEars(t, s, runpod.CaseAnalyze, func(o *runpod.CanaryOptions) { o.MaxUSD = runpod.DefaultCanaryMaxUSD })
		if rep.Exit != runpod.ExitRefused || s.count("POST /api/generations") != 1 {
			t.Fatalf("rep %+v generations %d", rep, s.count("POST /api/generations"))
		}
		if len(s.patches) != 1 || !strings.Contains(s.patches[0], "trk-1") {
			t.Errorf("the kept source take was not discarded: %v", s.patches)
		}
		if rep.Ceiling != 600_000 || rep.Measured["source_batch_id"] == nil {
			t.Errorf("ceiling %d measured %v", rep.Ceiling, rep.Measured)
		}
	})
	t.Run("a main render that used up the budget stops", func(t *testing.T) {
		t.Parallel()
		s := newEarsStub(t, allCaps, tierBDoc(nil), analyzeSeq(earsArts("analysis.json"), "trk-1")...)
		s.ceiling = 500_000
		rep := runEars(t, s, runpod.CaseAnalyze, func(o *runpod.CanaryOptions) { o.MaxUSD = runpod.DefaultCanaryMaxUSD })
		if rep.Exit != runpod.ExitRefused || s.count("POST /api/generations") != 1 {
			t.Fatalf("rep %+v", rep)
		}
		if len(s.patches) != 1 || !strings.Contains(s.patches[0], "trk-1") {
			t.Errorf("the kept source take was not discarded: %v", s.patches)
		}
	})
	for name, caps := range map[string][]string{"nothing open": nil, "analyze closed": {"v2", "ears"}} {
		t.Run("refused: "+name, func(t *testing.T) {
			t.Parallel()
			s := newEarsStub(t, caps, tierBDoc(nil), analyzeSeq(nil, "trk-1")...)
			rep := runEars(t, s, runpod.CaseAnalyze, nil)
			if rep.Exit != runpod.ExitRefused || !strings.Contains(rep.Reason, "contract open --caps v2,ears,analyze") {
				t.Fatalf("rep %+v", rep)
			}
			noSpend(t, s)
		})
	}
	trackJob := func(arts []any, track string) []map[string]any {
		j := completedJob(arts)
		j["track_id"] = track
		return []map[string]any{job("generating", nil), j}
	}
	fails := map[string]struct {
		tb    map[string]any
		jobs  []map[string]any
		check string
	}{
		"no document":       {tierBDoc(nil), trackJob(earsArts("audio.flac"), "trk-9"), "analysis_artifact"},
		"other take":        {tierBDoc(nil), trackJob(earsArts("analysis.json"), "trk-2"), "on_chosen_take"},
		"no raw row":        {nil, trackJob(earsArts("analysis.json"), "trk-9"), "tier_b_raw"},
		"document in error": {tierBDoc(map[string]any{"status": "error"}), trackJob(earsArts("analysis.json"), "trk-9"), "analysis_ok"},
	}
	for name, f := range fails {
		t.Run("fail: "+name, func(t *testing.T) {
			t.Parallel()
			rep := runEars(t, newEarsStub(t, allCaps, f.tb, f.jobs...), runpod.CaseAnalyze, func(o *runpod.CanaryOptions) { o.SourceTrack = "trk-9" })
			if c, _ := checkOf(rep, f.check); rep.Exit != runpod.ExitFail || c.OK {
				t.Fatalf("rep %+v check %+v", rep, c)
			}
		})
	}
	t.Run("a failed source render stops the case", func(t *testing.T) {
		t.Parallel()
		s := newEarsStub(t, allCaps, tierBDoc(nil), job("generating", nil), job("failed", map[string]any{"error_code": "OOM"}))
		rep := runEars(t, s, runpod.CaseAnalyze, nil)
		if rep.Exit != runpod.ExitFail || !strings.Contains(rep.Reason, "source render") || s.count("POST /api/generations") != 1 {
			t.Fatalf("rep %+v", rep)
		}
	})
}

func TestCanary_EarsCasesAreListed(t *testing.T) {
	t.Parallel()
	for _, c := range []runpod.CanaryCase{runpod.CaseTierB, runpod.CaseAnalyze} {
		if got, err := runpod.ParseCanaryCase(string(c)); err != nil || got != c {
			t.Errorf("ParseCanaryCase(%q) = %q %v", c, got, err)
		}
	}
	_, err := runpod.ParseCanaryCase("bogus")
	if err == nil || !strings.Contains(err.Error(), "tier-b") || !strings.HasSuffix(err.Error(), "or analyze)") {
		t.Errorf("error text %v", err)
	}
}

func TestCanary_EarsCasesDryRunGolden(t *testing.T) {
	t.Parallel()
	p := runpod.EarsBudgetParams{}
	cs := runpod.EarsWorstCase(p)
	golden(t, "canary_worst_case_ears.golden", runpod.FormatEarsWorstCase(p, cs))
	for _, c := range cs {
		if c.CeilingMicro <= 0 || money.MicroUSD(c.CeilingMicro) > 3*runpod.DefaultCanaryMaxUSD {
			t.Errorf("%s ceiling %d is implausible", c.Case, c.CeilingMicro)
		}
	}
	// a dearer price only raises the figure
	dear := runpod.EarsWorstCase(runpod.EarsBudgetParams{PricePPH: 2 * runpod.IllustrativePricePPH})
	if dear[0].CeilingMicro <= cs[0].CeilingMicro {
		t.Errorf("tier-b ceiling did not rise with the price: %d <= %d", dear[0].CeilingMicro, cs[0].CeilingMicro)
	}
}

// the sung recipe of tier-b parses as a YuE2 request with lyrics, a score and no instrumental style.
func TestCanary_EarsRecipeParses(t *testing.T) {
	t.Parallel()
	for _, c := range []runpod.CanaryCase{runpod.CaseTierB, runpod.CaseAnalyze} {
		s := mustSpec(c)
		p, err := yue2.ParseRequest(s.Recipe, yue2.ParseOptions{})
		if err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		rq := p.Request
		if rq.ABC == nil || !strings.Contains(rq.Lyrics, "[chorus]") || strings.Contains(rq.Style, "instrumental") || rq.ID != s.RequestID {
			t.Errorf("%s: request %+v", c, rq)
		}
		if len(s.NeedCaps) < 2 || s.NeedCaps[0] != "v2" || s.NeedCaps[1] != "ears" {
			t.Errorf("%s: needs %v", c, s.NeedCaps)
		}
	}
}
