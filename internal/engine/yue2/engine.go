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

// Minimum-render control. YuE2 sometimes writes its end-of-music token long before the material is sung
// (measured: 76 s of a 288 s score), and its only supported control is the semantic sampler's min_tokens, which
// suppresses that token until the step count is reached (yue2 sampling.distribution). Semantic tokens run at
// 25 per second of audio.
const (
	// minAudioShareMilli is the part of the estimated length every take is forced to reach (70 percent, in
	// thousandths below): the estimate is bars x tempo or lyric lines, so it is itself uncertain, and the
	// song is allowed to finish up to 30 percent early.
	minAudioShareMilli = 700
	// MinAudioCapSeconds keeps the forced floor well under max_tokens (9000 = 360 s): the model needs room to
	// close the song, and forcing the sampler past the material only produces rambling.
	MinAudioCapSeconds = 330
	// SemanticTokensPerSecond converts the floor to semantic min_tokens (worker side).
	SemanticTokensPerSecond = 25
)

// MinAudioSeconds is the forced minimum length of a take: min(max(minDurationS, ceil(0.7 x expectedS)), 330).
// minDurationS is the operator's songwriting.min_duration_s; 0 means none. The result is what the worker turns
// into semantic min_tokens, and what the quote must assume as the take's length.
func (Engine) MinAudioSeconds(expectedS, minDurationS int) int {
	share := (expectedS*minAudioShareMilli + 999) / 1000
	return min(max(minDurationS, share, 0), MinAudioCapSeconds)
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
