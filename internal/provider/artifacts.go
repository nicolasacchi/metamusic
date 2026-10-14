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

// ArtifactMaxBytes is the size limit of an allowlisted artifact; ok is false for any other name.
func ArtifactMaxBytes(name string) (limit int64, ok bool) {
	for _, a := range Artifacts {
		if a.Name == name {
			return a.MaxBytes, true
		}
	}
	return 0, false
}

//go:embed artifacts.observed.json
var observedJSON []byte

var (
	observedOnce sync.Once
	observed     map[string][]string
	observedErr  error
)

// ObservedArtifacts returns, per cot, the artifact names recorded from real GPU renders (M3-X3). While the
// committed file is the placeholder {} it is empty and every conditional artifact stays
// optional-but-verified (C73.4). The returned map is a copy.
func ObservedArtifacts() map[string][]string {
	observedOnce.Do(func() { observed, observedErr = parseObserved(observedJSON) })
	if observedErr != nil {
		panic("provider: artifacts.observed.json: " + observedErr.Error()) // a committed file; caught by tests
	}
	out := make(map[string][]string, len(observed))
	for k, v := range observed {
		out[k] = slices.Clone(v)
	}
	return out
}

func parseObserved(raw []byte) (map[string][]string, error) {
	var m map[string][]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	for cot, names := range m {
		switch cot {
		case "full", "melody", "off":
		default:
			return nil, fmt.Errorf("unknown cot %q", cot)
		}
		for _, n := range names {
			if _, ok := ArtifactMaxBytes(n); !ok || n == ResultArtifact {
				return nil, fmt.Errorf("cot %s lists %q, which is not an observable allowlisted artifact", cot, n)
			}
		}
	}
	return m, nil
}
