// Package engine is the engine-neutral interface behind estimates and jobs (M3 C70). It imports no
// engine implementation (not internal/engine/yue2, not internal/engine/registry): implementations import
// this package, and only engine/registry imports both.
package engine

import "encoding/json"

// Caps describes what an engine can do and its hard limits.
type Caps struct {
	Text2Song, PlanScore, RenderFromScore, RepaintInterval, Stems, ReferenceAudio, LoRA bool
	MaxSeconds, SampleRate                                                              int
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
	// ExpectedAudioSeconds is the whole-second length estimate, clamped to [30, Caps().MaxSeconds].
	ExpectedAudioSeconds(request json.RawMessage) (int, error)
	// Artifacts is the upload allowlist for one take (C73.4): required and optional-but-verified names.
	Artifacts(request json.RawMessage) (required, optional []string)
}

// Registry maps engine names to engines.
type Registry map[string]Engine
