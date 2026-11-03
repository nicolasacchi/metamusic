package acestep

import (
	"encoding/json"
	"errors"
	"slices"

	"github.com/nicolasacchi/metamusic/internal/engine"
)

// Artifact names (C153.2); the provider package owns the sizes.
const (
	artAudio, artRequest, artResult, artMMResult = "audio.flac", "request.json", "result.json", "mm-result.json"
	artLRC                                       = "lyrics.lrc"
)

// Generation modes (lineage.Mode* values; spelled here because the engine does not import lineage).
const (
	ModeComposeNew, ModeNewSeed, ModeSameSeedEdit, ModeLyricEdit, ModeStyleEdit = "compose_new", "new_seed", "same_seed_edit", "lyric_edit", "style_edit"
	ModeFromTemplate, ModePort, ModeRepaint, ModeReference, ModeExtend          = "from_template", "port", "repaint", "reference", "extend"
	ModeComplete, ModeStems                                                     = "complete", "stems"
)

// Engine adapts this package to engine.Engine. The zero value is valid: no verified features means
// text2music only, on the default pools.
type Engine struct {
	Verified Features
	// Observed lists, per task, the artifact names real renders wrote; a name listed for a task is required for it.
	Observed map[string][]string
	// Pools are the endpoint's configured pools (MM_ACESTEP_POOLS); empty means DefaultPools.
	Pools []string
}

var (
	_ engine.Engine       = Engine{}
	_ engine.IngestPolicy = Engine{}
)

// Name implements engine.Engine.
func (Engine) Name() string { return Name }

// ReportsTruncation implements engine.IngestPolicy: ACE-Step reports no truncation.
func (Engine) ReportsTruncation() bool { return false }

// LearnsRTF implements engine.IngestPolicy: yue2's rtf_stats is never taught by ACE-Step takes.
func (Engine) LearnsRTF() bool { return false }

// Caps implements engine.Engine: the effective caps, the intersection of what the pinned ACE-Step supports and
// the verified features (D-E2-7), with the duration cap the minimum over the configured pools (D-E2-14).
func (e Engine) Caps() engine.Caps {
	f := e.Verified
	c := engine.Caps{Text2Song: true, Tempo: true, Key: true, MinSeconds: MinS, MaxSeconds: f.MaxSFor(e.Pools), SampleRate: SampleRate}
	tasks := []string{ModeComposeNew, ModeNewSeed, ModeLyricEdit, ModeStyleEdit, ModeFromTemplate, ModePort}
	if f.SeedReproducible {
		tasks = append(tasks, ModeSameSeedEdit)
	}
	if f.has(TaskRepaint) {
		c.RepaintInterval, c.RepaintMinS, c.RepaintMaxS = true, f.Repaint.MinS, f.Repaint.MaxS
		tasks = append(tasks, ModeRepaint)
	}
	if f.has(TaskExtend) {
		c.Extend, c.ExtendMinS, c.ExtendMaxS = true, f.Extend.MinS, f.Extend.MaxS
		tasks = append(tasks, ModeExtend)
	}
	if f.Reference {
		c.ReferenceAudio, c.ReferenceMaxS = true, f.ReferenceMaxS
		tasks = append(tasks, ModeReference)
	}
	if f.has(TaskComplete) && f.baseModelsLoaded() {
		c.Complete = true
		tasks = append(tasks, ModeComplete)
	}
	if f.has(TaskExtract) && f.baseModelsLoaded() {
		c.Stems, c.StemNames = true, slices.Clone(StemNames)
		tasks = append(tasks, ModeStems)
	}
	c.LRC = f.LRC
	slices.Sort(tasks)
	c.Tasks = tasks
	return c
}

// Check validates r against the effective caps: task and option capabilities, the duration cap (never a
// silent clamp), tightened segment bounds and the pinned parameters version.
func Check(r Request, c engine.Caps) []engine.Issue {
	var is []engine.Issue
	add := func(i engine.Issue) { is = append(is, i) }
	if r.ParamsVersion != CurrentParamsVersion() {
		add(issue("ENGINE_PARAMS_STALE", "engine_params_version", "engine_params_version is not the pinned version"))
	}
	switch {
	case r.Task == TaskRepaint && !c.RepaintInterval:
		add(capability("task", "repaint"))
	case r.Task == TaskExtend && !c.Extend:
		add(capability("task", "extend"))
	case r.Task == TaskComplete && !c.Complete:
		add(capability("task", "complete"))
	case r.Task == TaskExtract && !c.Stems:
		add(capability("task", "stems"))
	}
	if r.Ref != nil {
		if !c.ReferenceAudio {
			add(capability("ref", "reference"))
		} else if c.ReferenceMaxS > 0 && r.Ref.ToDS-r.Ref.FromDS > 10*c.ReferenceMaxS {
			add(issue("reference_window", "ref", "reference window above %d s", c.ReferenceMaxS))
		}
	}
	if r.LRC && !c.LRC {
		add(capability("lrc", "lrc"))
	}
	if r.BPM != nil && !c.Tempo || r.TimeSignature != "" && !c.Tempo {
		add(capability("bpm", "tempo"))
	}
	if r.KeyScale != "" && !c.Key {
		add(capability("keyscale", "key"))
	}
	if c.MaxSeconds > 0 && ExpectedSeconds(r) > c.MaxSeconds {
		add(capability("duration_ds", "max_seconds"))
	}
	if r.Task == TaskRepaint && r.RepaintFromDS != nil && r.RepaintToDS != nil {
		n := *r.RepaintToDS - *r.RepaintFromDS
		if c.RepaintMinS > 0 && n < 10*c.RepaintMinS {
			add(issue("range_too_short", "repaint_to_ds", "range shorter than %d s", c.RepaintMinS))
		}
		if c.RepaintMaxS > 0 && n > 10*c.RepaintMaxS {
			add(issue("range_too_long", "repaint_to_ds", "range longer than %d s", c.RepaintMaxS))
		}
	}
	if r.Task == TaskExtend && r.ExtendDS != nil {
		if c.ExtendMinS > 0 && *r.ExtendDS < 10*c.ExtendMinS {
			add(issue("extend_too_short", "extend_ds", "extend shorter than %d s", c.ExtendMinS))
		}
		if c.ExtendMaxS > 0 && *r.ExtendDS > 10*c.ExtendMaxS {
			add(issue("extend_too_long", "extend_ds", "extend longer than %d s", c.ExtendMaxS))
		}
	}
	return is
}

// Validate implements engine.Engine: the errors of Parse, else those of Check against the effective caps.
func (e Engine) Validate(request json.RawMessage) []engine.Issue {
	r, _, is := Parse(request)
	var out []engine.Issue
	for _, i := range is {
		if i.Severity == "error" {
			out = append(out, i)
		}
	}
	if len(out) > 0 {
		return out
	}
	return Check(r, e.Caps())
}

// ExpectedSeconds is the exact rendered length in whole seconds (C148.6): text2music ceil(duration/10),
// repaint, complete and extract the source length, extend source plus extension. It is never clamped.
func ExpectedSeconds(r Request) int {
	ds := r.DurationDS
	if r.Src != nil {
		ds = r.Src.DurationDS
		if r.Task == TaskExtend && r.ExtendDS != nil {
			ds += *r.ExtendDS
		}
	}
	return (ds + 9) / 10
}

// ExpectedAudioSeconds implements engine.Engine: exact, and an error (never a clamp) when the request is
// invalid or outside the effective caps.
func (e Engine) ExpectedAudioSeconds(request json.RawMessage) (int, error) {
	if is := e.Validate(request); len(is) > 0 {
		return 0, errors.New("acestep: " + is[0].Code + ": " + is[0].Message)
	}
	r, _, _ := Parse(request)
	return ExpectedSeconds(r), nil
}

// Artifacts implements engine.Engine: audio.flac, request.json, result.json and mm-result.json are always
// required; lyrics.lrc is optional-but-verified when the request asks for it. A name Observed lists for the
// request's task becomes required.
func (e Engine) Artifacts(request json.RawMessage) (required, optional []string) {
	required = []string{artAudio, artRequest, artResult, artMMResult}
	r, _, _ := Parse(request)
	if r.LRC {
		optional = append(optional, artLRC)
	}
	for _, n := range e.Observed[r.Task] {
		if i := slices.Index(optional, n); i >= 0 {
			optional = slices.Delete(optional, i, i+1)
		}
		if !slices.Contains(required, n) {
			required = append(required, n)
		}
	}
	return required, optional
}
