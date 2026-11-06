package runpod_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
)

func TestCanary_E2bCaseOrder(t *testing.T) {
	t.Parallel()
	want := []runpod.CanaryCase{"extend", "complete", "extract", "base-long", "lrc"}
	if got := runpod.ACEStepCanaryCases("b"); !slices.Equal(got, want) {
		t.Fatalf("order %v", got)
	}
	for _, c := range want {
		if got, err := runpod.ParseACEStepCase(string(c)); err != nil || got != c {
			t.Errorf("%q: %v", c, err)
		}
	}
}

func TestCanary_ScriptAgainstFake_E2b(t *testing.T) {
	t.Parallel()
	stagesOf := map[runpod.CanaryCase][]string{
		runpod.CaseExtend:   {"source", "plus10", "plus90"},
		runpod.CaseComplete: {"source", "complete"},
		runpod.CaseExtract:  {"source", "extract"},
		runpod.CaseBaseLong: {"source-long", "base-long"},
		runpod.CaseLRC:      {"plain", "lrc"},
	}
	for c, want := range stagesOf {
		t.Run(string(c), func(t *testing.T) {
			t.Parallel()
			s := newAceRest(t)
			rep := runACE(t, s, c)
			if rep.Exit != runpod.ExitPass || rep.Outcome != "pass" {
				t.Fatalf("rep %+v", rep)
			}
			if got := stageLabels(rep); !slices.Equal(got, want) {
				t.Errorf("stages %v, want %v", got, want)
			}
			if rep.Proposed == nil {
				t.Error("a passing case proposes its patch")
			}
			if len(s.patches) != len(s.tracks) {
				t.Errorf("discarded %d tracks of %d", len(s.patches), len(s.tracks))
			}
			out, _ := json.Marshal(rep)
			if strings.Contains(string(out), canary("mm-token")) || strings.Contains(string(out), "mmc_") {
				t.Errorf("the report leaks: %s", out)
			}
		})
	}
}

func TestCanary_E2b_Assertions(t *testing.T) {
	t.Parallel()
	// extend must lengthen the source by exactly the extension
	s := newAceRest(t)
	s.tamper = func(item map[string]any, j map[string]any) {
		if item["mode"] == "extend" {
			j["audio_s"] = 37.0
		}
	}
	if rep := runACE(t, s, runpod.CaseExtend); rep.Exit != runpod.ExitFail || rep.Proposed != nil {
		t.Errorf("extend with a wrong length: %+v", rep)
	}
	// extract must name the stem; lrc must produce the file
	s2 := newAceRest(t)
	s2.tamper = func(item map[string]any, j map[string]any) {
		if item["mode"] == "stems" {
			j["artifacts"] = []any{map[string]any{"name": "result.json", "bytes": 1, "sha256": "x", "verified": true}}
		}
	}
	if rep := runACE(t, s2, runpod.CaseExtract); rep.Exit != runpod.ExitFail {
		t.Errorf("extract without a stem: %+v", rep)
	}
	s3 := newAceRest(t)
	s3.tamper = func(item map[string]any, j map[string]any) {
		if r, _ := item["recipe"].(map[string]any); r != nil && r["lrc"] == true {
			j["artifacts"] = []any{map[string]any{"name": "audio.flac", "bytes": 1, "sha256": "x", "verified": true}}
		}
	}
	rep := runACE(t, s3, runpod.CaseLRC)
	if rep.Exit != runpod.ExitFail || rep.Proposed != nil {
		t.Errorf("lrc without lyrics.lrc: %+v", rep)
	}
	// lrc_fixed_s is the extra execution time of the aligned render over the plain one
	rep = runACE(t, newAceRest(t), runpod.CaseLRC)
	if v, _ := rep.Measured["lrc_fixed_s"].(int64); v != 18 { // 30 s with the pass, 12 s without
		t.Errorf("lrc_fixed_s = %v", rep.Measured["lrc_fixed_s"])
	}
	if rep.Proposed.Features["lrc"] != true || rep.Proposed.Features["lrc_fixed_s"] != int64(18) {
		t.Errorf("proposed %+v", rep.Proposed.Features)
	}
	// complete and extract run on the base class; the baked base DiT proves models_mode
	rep = runACE(t, newAceRest(t), runpod.CaseComplete)
	if rep.Proposed.Features["models_mode"] != "bundle_cached_base_baked" || !slices.Equal(rep.Proposed.TasksAdd, []string{"complete"}) {
		t.Errorf("complete proposed %+v", rep.Proposed)
	}
}

func TestCanary_ProposedPatch_E2b_Golden(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	for _, c := range []runpod.CanaryCase{runpod.CaseExtend, runpod.CaseComplete, runpod.CaseExtract, runpod.CaseBaseLong, runpod.CaseLRC} {
		rep := runACE(t, newAceRest(t), c)
		if rep.Exit != runpod.ExitPass {
			t.Fatalf("%s: %+v", c, rep)
		}
		b, _ := json.MarshalIndent(map[string]any{"case": c, "proposed": rep.Proposed}, "", "  ")
		sb.Write(b)
		sb.WriteString("\n")
	}
	golden(t, "canary_proposed_patch_e2b.golden.json", sb.String())
}

func TestCanary_WorstCaseDryRun_E2b_Golden(t *testing.T) {
	t.Parallel()
	r, err := runpod.CanaryWorstCase("b", runpod.BudgetParams{})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "canary_worst_case_b.golden", r.Format())
	// base-long needs a raised MM_ACESTEP_MAX_RUNTIME: its take is priced with the unclamped timeout, above the 900 s default
	for _, c := range r.Cases {
		if c.Case == runpod.CaseBaseLong && c.Takes[1].TimeoutS <= 900 {
			t.Errorf("base-long priced with timeout %d s, expected above the default runtime", c.Takes[1].TimeoutS)
		}
	}
}
