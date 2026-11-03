package acestep

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"
)

//go:embed engine_params.json
var paramsJSON []byte

//go:embed lang.json
var langJSON []byte

//go:embed lyrics_tags.json
var tagsJSON []byte

// ParamSet is one pinned set of every output-affecting engine parameter (D-E2-31, C148.10). A value the pinned
// source does not expose, or that is not audited yet, is {"upstream_default": "..."}; the file-level
// "audited" flag stays false while any such placeholder says "unaudited".
type ParamSet struct {
	ACEStepCommit string                     `json:"acestep_commit"`
	DiT           map[string]json.RawMessage `json:"dit"`
	LM            map[string]json.RawMessage `json:"lm"`
	Classes       map[string]ClassParams     `json:"classes"`
}

// ClassParams are the per-class defaults.
type ClassParams struct {
	Steps         int `json:"steps"`
	GuidanceMilli int `json:"guidance_milli,omitempty"`
}

// ParamsFile is engine_params.json.
type ParamsFile struct {
	Schema   string              `json:"schema"`
	Current  string              `json:"current"`
	Audited  bool                `json:"audited"`
	Versions map[string]ParamSet `json:"versions"`
}

var (
	paramsOnce sync.Once
	params     ParamsFile
)

func paramsFile() ParamsFile {
	paramsOnce.Do(func() {
		p, err := ParseParams(paramsJSON)
		if err != nil {
			panic(err)
		}
		params = p
	})
	return params
}

var ditKeys = []string{"shift", "use_adg", "cfg_interval_start", "cfg_interval_end", "timesteps", "chunk_mask_mode", "batch_size"}
var lmKeys = []string{"backend", "temperature", "top_p", "top_k", "cfg_scale", "use_cot_metas", "use_cot_caption", "use_cot_language", "constrained_decoding", "gpu_memory_utilization"}

// ParseParams reads and checks an engine-params file.
func ParseParams(raw []byte) (ParamsFile, error) {
	var f ParamsFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return f, fmt.Errorf("acestep params: %w", err)
	}
	if f.Schema != "mm.engine-params/1" {
		return f, fmt.Errorf("acestep params: schema %q", f.Schema)
	}
	cur, ok := f.Versions[f.Current]
	if !ok {
		return f, fmt.Errorf("acestep params: current version %q is not defined", f.Current)
	}
	for v, ps := range f.Versions {
		for _, k := range ditKeys {
			if _, ok := ps.DiT[k]; !ok {
				return f, fmt.Errorf("acestep params: %s: dit.%s missing", v, k)
			}
		}
		for _, k := range lmKeys {
			if _, ok := ps.LM[k]; !ok {
				return f, fmt.Errorf("acestep params: %s: lm.%s missing", v, k)
			}
		}
		if string(ps.DiT["batch_size"]) != "1" {
			return f, fmt.Errorf("acestep params: %s: batch_size must be pinned to 1", v)
		}
		if ps.Classes[ClassTurbo].Steps != TurboSteps {
			return f, fmt.Errorf("acestep params: %s: turbo steps must be %d", v, TurboSteps)
		}
		if b := ps.Classes[ClassBase]; b.Steps != DefaultBaseSteps || b.GuidanceMilli != DefaultGuidanceMilli {
			return f, fmt.Errorf("acestep params: %s: base defaults differ from the package constants", v)
		}
	}
	if f.Audited && bytes.Contains(raw, []byte(`"unaudited"`)) {
		return f, fmt.Errorf("acestep params: audited is true but unaudited placeholders remain")
	}
	if f.Audited && cur.ACEStepCommit == "" {
		return f, fmt.Errorf("acestep params: audited without acestep_commit")
	}
	return f, nil
}

// CurrentParamsVersion is the version a new request is stamped with.
func CurrentParamsVersion() string { return paramsFile().Current }

// CurrentParams returns the current pinned parameter set.
func CurrentParams() ParamSet { return paramsFile().Versions[paramsFile().Current] }

// ParamsAudited reports whether every parameter value was read from the pinned source (E2-P0a).
func ParamsAudited() bool { return paramsFile().Audited }

var (
	langOnce sync.Once
	langs    []string
)

// languageList is the committed vocal-language list.
func languageList() []string {
	langOnce.Do(func() {
		var f struct {
			Languages []string `json:"languages"`
		}
		if err := json.Unmarshal(langJSON, &f); err != nil {
			panic(err)
		}
		langs = f.Languages
	})
	return langs
}

// Languages returns a copy of the vocal-language list.
func Languages() []string { return append([]string(nil), languageList()...) }
