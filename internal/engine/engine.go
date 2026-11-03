// Package engine is the engine-neutral interface behind estimates and jobs (M3 C70). It imports no
// engine implementation (not internal/engine/yue2, not internal/engine/registry): implementations import
// this package, and only engine/registry imports both.
package engine

import "encoding/json"

// Caps describes what an engine can do and its hard limits (Caps v2, C148.1). It is additive over M3: the zero
// value of every field added in v2 means "no" or "not applicable". Engine.Caps returns the effective caps.
type Caps struct {
	Text2Song, PlanScore, RenderFromScore, RepaintInterval, Stems, ReferenceAudio, LoRA bool // M3, unchanged
	Extend                                                                              bool // continue a take past its end
	Complete                                                                            bool // add named instrument tracks to a take
	LRC                                                                                 bool // returns timestamped lyrics
	Tempo, Key                                                                          bool // accepts bpm / keyscale (+ time signature with Tempo)
	// MinSeconds, MaxSeconds and SampleRate bound a render; MinSeconds 0 means no stated minimum.
	MinSeconds, MaxSeconds, SampleRate int
	// Interval limits in whole seconds; 0 = not applicable.
	RepaintMinS, RepaintMaxS, ExtendMinS, ExtendMaxS, ReferenceMaxS int
	StemNames                                                       []string // sorted; empty unless Stems
	Tasks                                                           []string // the generation modes the engine offers, sorted
}

// Issue is an engine-neutral validation finding. It mirrors the field set of yue2.Issue one for one
// (TestIssueConversion_Lossless pins the conversion).
type Issue struct {
	Code, Field, Severity, Message string // Severity "error" | "warning"
	Line                           int
	Section                        string
	Fatal                          bool
}

// Engine is one render engine.
type Engine interface {
	// Name is the registry key and recipes.engine value, for example "yue2".
	Name() string
	Caps() Caps
	// Validate returns the errors of exactly what the engine's own batch tooling rejects (ModeYuE2).
	Validate(request json.RawMessage) []Issue
	// ExpectedAudioSeconds is the whole-second length estimate: clamped to [MinSeconds, MaxSeconds] by engines
	// that estimate (yue2); an engine with explicit durations validates instead and returns an error outside
	// the range.
	ExpectedAudioSeconds(request json.RawMessage) (int, error)
	// Artifacts is the upload allowlist for one take (C73.4): required and optional-but-verified names.
	Artifacts(request json.RawMessage) (required, optional []string)
}

// Registry maps engine names to engines.
type Registry map[string]Engine

// MinAudioPlanner is implemented by engines whose renders can end well before the material does and that have
// a supported control for it. MinAudioSeconds is the forced minimum length in seconds of a take expected to
// last expectedS, given the operator's minimum song length minDurationS (0 = none). The planner records it per
// take and quotes the take no shorter; the worker enforces it.
type MinAudioPlanner interface {
	MinAudioSeconds(expectedS, minDurationS int) int
}
