package acestep

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestFeatures_FileSafe(t *testing.T) {
	t.Parallel()
	good := string(featuresJSON)
	if _, err := ParseFeatures(featuresJSON); err != nil {
		t.Fatalf("embedded file: %v", err)
	}
	f := EmbeddedFeatures()
	if len(f.Tasks) != 1 || f.Tasks[0] != "text2music" || f.Reference || f.LRC || f.SeedReproducible {
		t.Errorf("the committed file must start with text2music only: %+v", f)
	}
	for name, tc := range map[string]struct{ from, to string }{
		"unknown key":         {`"engine":"acestep15"`, `"engine":"acestep15","extra":1`},
		"schema":              {`mm.engine-features/2`, `mm.engine-features/1`},
		"engine":              {`"engine":"acestep15"`, `"engine":"yue2"`},
		"date":                {`1970-01-01`, `yesterday`},
		"unknown task":        {`"tasks":["text2music"]`, `"tasks":["text2music","cover"]`},
		"no text2music":       {`"tasks":["text2music"]`, `"tasks":["repaint"]`},
		"task without canary": {`"tasks":["text2music"]`, `"tasks":["text2music","repaint"]`},
		"models mode":         {`bundle_cached_base_baked`, `nope`},
		"default above 480":   {`"max_s_default":480`, `"max_s_default":481`},
		"pool above 480":      {`"AMPERE_24":{"max_s":480}`, `"AMPERE_24":{"max_s":600}`},
		"pool above 600":      {`"AMPERE_24":{"max_s":480}`, `"AMPERE_24":{"max_s":601,"canary_commit":"c"}`},
		"pool id":             {`"AMPERE_24"`, `"bad pool"`},
		"repaint above 90":    {`"repaint":{"min_s":3,"max_s":90}`, `"repaint":{"min_s":3,"max_s":91}`},
		"extend below 3":      {`"extend":{"min_s":3,"max_s":90}`, `"extend":{"min_s":2,"max_s":90}`},
		"reference max":       {`"reference_max_s":30`, `"reference_max_s":31`},
		"lrc fixed":           {`"lrc_fixed_s":60`, `"lrc_fixed_s":-1`},
	} {
		if _, err := ParseFeatures([]byte(strings.Replace(good, tc.from, tc.to, 1))); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ParseFeatures([]byte(good + "{}")); err == nil {
		t.Error("trailing data accepted")
	}
	if _, err := ParseFeatures([]byte(`{`)); err == nil {
		t.Error("truncated accepted")
	}
	ok := strings.Replace(good, `"AMPERE_24":{"max_s":480}`, `"AMPERE_24":{"max_s":600,"canary_commit":"c"}`, 1)
	if _, err := ParseFeatures([]byte(ok)); err != nil {
		t.Errorf("a canary-proved 600 s pool: %v", err)
	}
}

var hex40 = regexp.MustCompile(`^[0-9a-f]{40}$`)

func TestWeights_Pinned(t *testing.T) {
	t.Parallel()
	for repo, sha := range Weights {
		if !hex40.MatchString(sha) {
			t.Errorf("%s: %q is not a 40 hex sha", repo, sha)
		}
	}
	if len(Weights) != 2 {
		t.Errorf("weights: %v", Weights)
	}
}

func TestWeights_ProvisionMatches(t *testing.T) {
	t.Parallel()
	env := WeightsEnv("turbo")
	if env[EnvMainRevision] != Weights[WeightsRepoMain] || env[EnvTasks] != "turbo" || len(env) != 2 {
		t.Errorf("%v", env)
	}
	env = WeightsEnv("base, turbo")
	if env[EnvBaseRevision] != Weights[WeightsRepoBase] || env[EnvTasks] != "turbo,base" || len(env) != 3 {
		t.Errorf("%v", env)
	}
	if WeightsEnv("base") != nil || WeightsEnv("") != nil {
		t.Error("invalid list must give nil")
	}
}

func TestWeightsFor_Table(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]int{"turbo": 1, "turbo,base": 2, "base,turbo": 2, " turbo , base ": 2, "base": 0, "": 0, "turbo,turbo": 0, "turbo,xl": 0} {
		if got := len(WeightsFor(in)); got != want {
			t.Errorf("WeightsFor(%q) has %d repos, want %d", in, got, want)
		}
	}
	if NormalizeTasks("base,turbo") != TasksTurboBase {
		t.Error("normalize")
	}
}

func TestEngineParams_FileSafe(t *testing.T) {
	t.Parallel()
	if _, err := ParseParams(paramsJSON); err != nil {
		t.Fatal(err)
	}
	good := string(paramsJSON)
	for name, tc := range map[string]struct{ from, to string }{
		"schema":        {`mm.engine-params/1`, `x`},
		"current":       {`"current":"ace1"`, `"current":"ace9"`},
		"dit key":       {`"shift":`, `"shifty":`},
		"lm key":        {`"top_k":`, `"top_kk":`},
		"batch size":    {`"batch_size":1`, `"batch_size":2`},
		"turbo steps":   {`"turbo":{"steps":8}`, `"turbo":{"steps":9}`},
		"base defaults": {`"steps":50`, `"steps":40`},
		"no commit":     {`"acestep_commit":"` + pinnedACEStepCommit + `"`, `"acestep_commit":""`},
		"placeholder":   {`"use_adg":false`, `"use_adg":"unaudited"`},
		"unknown key":   {`"audited":true`, `"audited":true,"zzz":1`},
	} {
		if _, err := ParseParams([]byte(strings.Replace(good, tc.from, tc.to, 1))); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ParseParams([]byte(`{`)); err == nil {
		t.Error("truncated accepted")
	}
	if !ParamsAudited() || CurrentParamsVersion() != "ace1" || CurrentParams().Classes[ClassTurbo].Steps != TurboSteps {
		t.Error("params accessors")
	}
	// an unaudited file stays valid while it carries a placeholder
	draft := strings.Replace(strings.Replace(good, `"audited":true`, `"audited":false`, 1), `"use_adg":false`, `"use_adg":{"upstream_default":"unaudited"}`, 1)
	if _, err := ParseParams([]byte(draft)); err != nil {
		t.Errorf("unaudited draft: %v", err)
	}
}

// pinnedACEStepCommit is the ACE-Step release the audited values were read from (E2-P0a: tag v0.1.8).
const pinnedACEStepCommit = "dce621408bee8c31b4fcf4811682eb9359e1bc94"

func TestEngineParams_AuditedValues(t *testing.T) {
	t.Parallel()
	ps := CurrentParams()
	if ps.ACEStepCommit != pinnedACEStepCommit {
		t.Errorf("acestep_commit = %q", ps.ACEStepCommit)
	}
	// Values read from the pinned source (inference.py GenerationParams, llm_inference.py, gpu_config.py): a change is
	// a reviewed edit that bumps engine_params_version.
	for _, tc := range []struct{ got, want string }{
		{string(ps.DiT["shift"]), "3.0"},
		{string(ps.DiT["use_adg"]), "false"},
		{string(ps.DiT["cfg_interval_start"]), "0.0"},
		{string(ps.DiT["cfg_interval_end"]), "1.0"},
		{string(ps.DiT["timesteps"]), "null"},
		{string(ps.DiT["batch_size"]), "1"},
		{string(ps.DiT["dcw_enabled"]), `{"turbo":true,"base":false}`},
		{string(ps.DiT["chunk_mask_mode"]), `{"default":"auto","repaint":"explicit"}`},
		{string(ps.LM["backend"]), `"vllm"`},
		{string(ps.LM["model"]), `"acestep-5Hz-lm-1.7B"`},
		{string(ps.LM["temperature"]), "0.85"},
		{string(ps.LM["top_p"]), "0.9"},
		{string(ps.LM["top_k"]), "0"},
		{string(ps.LM["cfg_scale"]), "2.0"},
		{string(ps.LM["constrained_decoding"]), "true"},
	} {
		if tc.got != tc.want {
			t.Errorf("got %s, want %s", tc.got, tc.want)
		}
	}
}

func TestLanguages(t *testing.T) {
	t.Parallel()
	l := Languages()
	for _, want := range []string{"it", "en", "es", "fr", "de", "pt", "ja", "zh", "ko", "unknown"} {
		found := false
		for _, x := range l {
			found = found || x == want
		}
		if !found {
			t.Errorf("language %s missing", want)
		}
	}
	if len(l) != 51 || l[len(l)-1] != "unknown" || !slices.IsSorted(l[:len(l)-1]) {
		t.Errorf("language list is not the pinned ACE-Step list: %d entries", len(l))
	}
	for _, want := range []string{"yue", "sw", "ur", "az"} {
		if !slices.Contains(l, want) {
			t.Errorf("pinned language %s missing", want)
		}
	}
	l[0] = "mutated"
	if Languages()[0] == "mutated" {
		t.Error("Languages must return a copy")
	}
}
