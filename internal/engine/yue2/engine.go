package yue2

import (
	"encoding/json"
	"errors"
	"math"

	"github.com/nicolasacchi/metamusic/internal/engine"
)

// EngineName is the registry key and recipes.engine value.
const EngineName = "yue2"

// Seconds bounds of a render (C70).
const (
	minAudioSeconds = 30
	maxAudioSeconds = 360
	sampleRate      = 48000
)

// Artifact names (C73.4); the provider package owns the sizes.
const (
	artAudio, artRequest, artConfig, artResult = "audio.flac", "request.json", "config.json", "result.json"
	artScore, artPlan, artPlanManifest         = "score.abc", "plan.json", "plan_manifest.json"
	artMMResult                                = "mm-result.json"
)

// Engine adapts this package to engine.Engine. The zero value is valid.
type Engine struct {
	// Observed lists, per cot ("full", "melody", "off"), the artifact names real GPU renders of that cot
	// wrote (internal/provider/artifacts.observed.json, recorded in M3-X3). Names listed for a cot become
	// required for it; until a cot is listed, score.abc and the plan files are optional-but-verified
	// (C73.4). nil means nothing has been observed yet.
	Observed map[string][]string
}

var _ engine.Engine = Engine{}

// Name implements engine.Engine.
func (Engine) Name() string { return EngineName }

// Caps implements engine.Engine.
func (Engine) Caps() engine.Caps {
	return engine.Caps{Text2Song: true, PlanScore: true, RenderFromScore: true, MaxSeconds: maxAudioSeconds, SampleRate: sampleRate}
}

// Validate is CheckRequest(raw, ModeYuE2, nil) filtered to errors and converted field by field.
func (Engine) Validate(request json.RawMessage) []engine.Issue {
	var out []engine.Issue
	for _, is := range CheckRequest(request, ModeYuE2, nil) {
		if is.Severity != sevError {
			continue
		}
		out = append(out, ToEngineIssue(is))
	}
	return out
}

// ToEngineIssue converts one issue without losing a field.
func ToEngineIssue(is Issue) engine.Issue {
	return engine.Issue{Code: is.Code, Field: is.Field, Severity: is.Severity, Message: is.Message,
		Line: is.Line, Section: is.Section, Fatal: is.Fatal}
}

// ExpectedAudioSeconds is ceil(seconds) of EstimateDuration over the parsed request, clamped to [30, 360]
// (design §3.3: lyric sections, or ABC bars x meter / Q:, clamped).
func (Engine) ExpectedAudioSeconds(request json.RawMessage) (int, error) {
	p, err := ParseRequest(request, ParseOptions{})
	if err != nil {
		var pe *ParseError
		if errors.As(err, &pe) {
			return 0, errors.New("yue2: request does not parse: " + pe.Code)
		}
		return 0, err
	}
	secs, _ := EstimateDuration(p.Request)
	if math.IsNaN(secs) || secs < 0 {
		secs = minAudioSeconds
	}
	return int(min(max(math.Ceil(secs), minAudioSeconds), maxAudioSeconds)), nil
}

// Artifacts is the upload allowlist for one take (C73.4). Always required: audio.flac, request.json,
// config.json, result.json and mm-result.json (uploaded last). score.abc is never expected for cot=off;
// otherwise score.abc, plan.json and plan_manifest.json are optional-but-verified-if-reported, and
// required once Observed lists them for the take's cot. The .npy token files are optional.
func (e Engine) Artifacts(request json.RawMessage) (required, optional []string) {
	cot := "full"
	var probe struct {
		Cot string `json:"cot"`
	}
	if json.Unmarshal(request, &probe) == nil && probe.Cot != "" {
		cot = probe.Cot
	}
	required = []string{artAudio, artRequest, artConfig, artResult, artMMResult}
	observed := map[string]bool{}
	for _, n := range e.Observed[cot] {
		observed[n] = true
	}
	for _, n := range []string{artScore, artPlan, artPlanManifest} {
		if n == artScore && cot == "off" {
			continue
		}
		if observed[n] {
			required = append(required, n)
		} else {
			optional = append(optional, n)
		}
	}
	for _, n := range []string{"abc_tokens.npy", "prefix.npy", "semantic.npy"} {
		if observed[n] {
			required = append(required, n)
		} else {
			optional = append(optional, n)
		}
	}
	return required, optional
}
