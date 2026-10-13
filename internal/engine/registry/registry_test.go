package registry_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engine/registry"
	"github.com/nicolasacchi/metamusic/internal/provider"
)

func TestDefault(t *testing.T) {
	t.Parallel()
	r := registry.Default()
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
