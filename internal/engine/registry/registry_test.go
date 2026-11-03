package registry_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/engine/registry"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

func TestDefault(t *testing.T) {
	t.Parallel()
	r := registry.Default(registry.Options{})
	if len(r) != 1 || r["yue2"] == nil || r["yue2"].Name() != "yue2" {
		t.Fatalf("Default = %v", r)
	}
	// the observed artifact sets come from the committed provider file: a placeholder today, so score.abc is optional
	req, opt := r["yue2"].Artifacts(json.RawMessage(`{"cot":"full"}`))
	if len(provider.ObservedArtifacts()) == 0 && (slices.Contains(req, "score.abc") || !slices.Contains(opt, "score.abc")) {
		t.Errorf("placeholder observed file: required %v optional %v", req, opt)
	}
	// every artifact the engine can name is allowlisted by the provider
	for _, n := range append(slices.Clone(req), opt...) {
		if _, ok := provider.ArtifactMaxBytes(n); !ok {
			t.Errorf("engine names %q, which provider does not allowlist", n)
		}
	}
}

func TestRegistry_Names(t *testing.T) {
	t.Parallel()
	for _, o := range []registry.Options{{}, {ACEStep: true}} {
		r := registry.Default(o)
		if r["yue2"] == nil {
			t.Fatalf("Default(%+v) has no yue2", o)
		}
		for name, e := range r {
			if e.Name() != name {
				t.Errorf("registered %q under %q", e.Name(), name)
			}
		}
	}
}

// TestCaps_Yue2Unchanged pins yue2's capabilities: M3's value plus the two Caps v2 fields yue2 sets.
func TestCaps_Yue2Unchanged(t *testing.T) {
	t.Parallel()
	got, err := json.Marshal(registry.Default(registry.Options{})["yue2"].Caps())
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"Text2Song":true,"PlanScore":true,"RenderFromScore":true,"RepaintInterval":false,"Stems":false,"ReferenceAudio":false,"LoRA":false,` +
		`"Extend":false,"Complete":false,"LRC":false,"Tempo":false,"Key":false,"MinSeconds":30,"MaxSeconds":360,"SampleRate":48000,` +
		`"RepaintMinS":0,"RepaintMaxS":0,"ExtendMinS":0,"ExtendMaxS":0,"ReferenceMaxS":0,"StemNames":null,` +
		`"Tasks":["compose_new","from_draft","from_template","lyric_edit","new_seed","same_seed_edit","style_edit"]}`
	if string(got) != want {
		t.Errorf("yue2 caps = %s", got)
	}
}

// TestMinAudio_YuE2Only: the forced-minimum floor and the yue2-shaped ingest facts stay with yue2.
func TestIngestPolicy_Yue2Only(t *testing.T) {
	t.Parallel()
	y := registry.Default(registry.Options{})["yue2"]
	if _, ok := y.(engine.MinAudioPlanner); !ok || !engine.ReportsTruncation(y) || !engine.LearnsRTF(y) {
		t.Error("yue2 must plan min audio, report truncation and learn RTF")
	}
	var bare engine.Engine = bareEngine{}
	if engine.ReportsTruncation(bare) || engine.LearnsRTF(bare) {
		t.Error("an engine without an IngestPolicy must neither require truncation nor learn RTF")
	}
}

type bareEngine struct{ engine.Engine }
