package provider

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
)

// Artifact is one allowlisted upload (C73.4).
type Artifact struct {
	Name     string
	MaxBytes int64
	// Required is the unconditional part. score.abc, the plan files and the .npy files are required only
	// when internal/provider/artifacts.observed.json lists them for the take's cot (engine.Artifacts).
	Required bool
}

const (
	kib = 1 << 10
	mib = 1 << 20
)

// ResultArtifact is the worker output uploaded last through its own capability URL (D-M3-13).
const ResultArtifact = "mm-result.json"

// Artifacts is the allowlist, in upload order of C83.2 step 8 with mm-result.json last. latent.npy is never
// uploaded (design §3.3, D-M1-1) and is not here. TestArtifacts_SchemaEnum asserts equality with the
// names and maximums of the worker input schema.
var Artifacts = []Artifact{
	{"request.json", 4 * mib, true},
	{"config.json", 4 * mib, true},
	{"result.json", 4 * mib, true},
	{"score.abc", 1 * mib, false},
	{"plan.json", 4 * mib, false},
	{"plan_manifest.json", 4 * mib, false},
	{"abc_tokens.npy", 64 * mib, false},
	{"prefix.npy", 64 * mib, false},
	{"semantic.npy", 64 * mib, false},
	{"audio.flac", 160 * mib, true},
	{ResultArtifact, 64 * kib, true},
}

// ArtifactMaxBytes is the size limit of an allowlisted artifact of the first engine (yue2); ok is false for any other
// name. Engine-aware callers use ArtifactMaxBytesFor.
func ArtifactMaxBytes(name string) (limit int64, ok bool) {
	return ArtifactMaxBytesFor(engineYuE2, name)
}

// Engine names this package knows allowlists for (the engine packages own the same strings).
const (
	engineYuE2     = "yue2"
	engineACEStep  = "acestep15"
	observedACEKey = "default" // ACE-Step has no cot: one observed variant
)

// aceArtifacts is the ACE-Step allowlist: no config.json, plans, tokens or score; audio.flac has its own limit;
// lyrics.lrc is optional. Order is the upload order, mm-result.json last.
var aceArtifacts = []Artifact{
	{"request.json", 4 * mib, true},
	{"result.json", 4 * mib, true},
	{"lyrics.lrc", 1 * mib, false},
	{"audio.flac", 192 * mib, true},
	{ResultArtifact, 64 * kib, true},
}

// ArtifactsFor returns a copy of the engine's allowlist; ok is false for an engine without one.
func ArtifactsFor(engine string) ([]Artifact, bool) {
	switch engine {
	case engineYuE2:
		return slices.Clone(Artifacts), true
	case engineACEStep:
		return slices.Clone(aceArtifacts), true
	}
	return nil, false
}

// ArtifactMaxBytesFor is the size limit of an artifact in the engine's allowlist.
func ArtifactMaxBytesFor(engine, name string) (limit int64, ok bool) {
	var list []Artifact
	switch engine {
	case engineYuE2:
		list = Artifacts
	case engineACEStep:
		list = aceArtifacts
	}
	for _, a := range list {
		if a.Name == name {
			return a.MaxBytes, true
		}
	}
	return 0, false
}

// observedVariants are the keys an engine's observed set may use: yue2 by cot, ACE-Step a single one.
var observedVariants = map[string][]string{
	engineYuE2:    {"full", "melody", "off"},
	engineACEStep: {observedACEKey},
}

//go:embed artifacts.observed.json
var observedJSON []byte

var (
	observedOnce sync.Once
	observed     map[string]map[string][]string
	observedErr  error
)

func loadObserved() map[string]map[string][]string {
	observedOnce.Do(func() { observed, observedErr = parseObserved(observedJSON) })
	if observedErr != nil {
		panic("provider: artifacts.observed.json: " + observedErr.Error()) // a committed file; caught by tests
	}
	return observed
}

// ObservedFor returns, per variant, the artifact names recorded from real GPU renders of the engine (yue2: per cot).
// While the committed file is the placeholder {} it is empty and every conditional artifact stays
// optional-but-verified (C73.4). The returned map is a copy.
func ObservedFor(engine string) map[string][]string {
	src := loadObserved()[engine]
	out := make(map[string][]string, len(src))
	for k, v := range src {
		out[k] = slices.Clone(v)
	}
	return out
}

// ObservedArtifacts is ObservedFor("yue2"): per cot.
func ObservedArtifacts() map[string][]string { return ObservedFor(engineYuE2) }

// parseObserved reads the committed file: {engine: {variant: [names]}}. Unknown engines and variants, names outside
// the engine's allowlist, and mm-result.json (never observable) are refused.
func parseObserved(raw []byte) (map[string]map[string][]string, error) {
	var m map[string]map[string][]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	for eng, variants := range m {
		allowed, ok := observedVariants[eng]
		if !ok {
			return nil, fmt.Errorf("unknown engine %q", eng)
		}
		for variant, names := range variants {
			if !slices.Contains(allowed, variant) {
				return nil, fmt.Errorf("engine %s: unknown variant %q", eng, variant)
			}
			for _, n := range names {
				if _, ok := ArtifactMaxBytesFor(eng, n); !ok || n == ResultArtifact {
					return nil, fmt.Errorf("engine %s variant %s lists %q, which is not an observable allowlisted artifact", eng, variant, n)
				}
			}
		}
	}
	return m, nil
}
