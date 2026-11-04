package spend

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

type rtfSeedFile struct {
	Version int  `json:"version"`
	Sourced bool `json:"sourced"`
	Engines map[string]map[string]map[string]struct {
		RTFMilli int `json:"rtf_milli"`
		LoadS    int `json:"load_s"`
	} `json:"engines"`
}

// TestEngineRTF_SeedFileShape: the committed seed file (unsourced placeholders until the canary measures) parses
// strictly, names only engines with a measurement table, and every value fits the engine_rtf table's CHECK ranges.
func TestEngineRTF_SeedFileShape(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("engine_rtf.seed.json")
	if err != nil {
		t.Fatal(err)
	}
	var f rtfSeedFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		t.Fatal(err)
	}
	if f.Version != 1 || f.Sourced {
		t.Errorf("version %d sourced %v: seeds are version 1 and unsourced placeholders", f.Version, f.Sourced)
	}
	if _, ok := f.Engines["yue2"]; ok {
		t.Error("yue2 keeps rtf_stats and DefaultRTFMilli; it has no seed here")
	}
	classes, n := map[string]bool{"turbo": true, "base": true}, 0
	for eng, byTask := range f.Engines {
		for task, byGPU := range byTask {
			if !classes[task] {
				t.Errorf("%s: unknown task class %q", eng, task)
			}
			if len(byGPU) == 0 {
				t.Errorf("%s/%s has no GPU class", eng, task)
			}
			for gpu, v := range byGPU {
				n++
				if gpu != GPUClassKey(gpu) {
					t.Errorf("%s/%s: %q is not a canonical GPU class key (%q)", eng, task, gpu, GPUClassKey(gpu))
				}
				if v.RTFMilli < 5 || v.RTFMilli > 5000 || v.LoadS < 0 || v.LoadS > 600 {
					t.Errorf("%s/%s/%s: %+v outside the engine_rtf CHECK ranges", eng, task, gpu, v)
				}
			}
		}
	}
	if len(f.Engines["acestep15"]["turbo"]) == 0 || n == 0 {
		t.Error("acestep15 turbo has no seeds")
	}
}
