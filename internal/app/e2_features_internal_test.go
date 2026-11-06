package app

import (
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/engine/registry"
)

func TestApplyE2EFeatures(t *testing.T) {
	t.Parallel()
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "MM_E2E_ACESTEP_FEATURES" {
				return v
			}
			return ""
		}
	}
	for _, c := range []struct {
		name string
		cfg  config.Config
		env  string
		want bool
	}{
		{"e2e with the switch", config.Config{E2E: true, ACEStep: true, Backend: config.BackendFake}, "all", true},
		{"not an e2e server", config.Config{ACEStep: true, Backend: config.BackendFake}, "all", false},
		{"switch unset", config.Config{E2E: true, ACEStep: true, Backend: config.BackendFake}, "", false},
		{"engine off", config.Config{E2E: true, Backend: config.BackendFake}, "all", false},
	} {
		reg := registry.Default(registry.Options{ACEStep: c.cfg.ACEStep})
		if err := applyE2EFeatures(c.cfg, reg, env(c.env)); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		ae, ok := reg[acestep.Name].(acestep.Engine)
		if !ok {
			if c.cfg.ACEStep {
				t.Fatalf("%s: engine missing", c.name)
			}
			continue
		}
		caps := ae.Caps()
		if got := caps.RepaintInterval && caps.ReferenceAudio && caps.Extend && caps.Complete && caps.Stems; got != c.want {
			t.Errorf("%s: all capabilities on = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestApplyE2EFeatures_RefusesNonFakeBackend(t *testing.T) {
	t.Parallel()
	cfg := config.Config{E2E: true, ACEStep: true, Backend: config.BackendRunPod}
	reg := registry.Default(registry.Options{ACEStep: true})
	env := func(k string) string {
		if k == "MM_E2E_ACESTEP_FEATURES" {
			return "all"
		}
		return ""
	}
	if err := applyE2EFeatures(cfg, reg, env); err == nil || !strings.Contains(err.Error(), "requires fake backends") {
		t.Fatalf("err = %v", err)
	}
	if reg[acestep.Name].(acestep.Engine).Caps().RepaintInterval {
		t.Error("the registry was swapped despite the refusal")
	}
	// unset: nothing changes, whatever the backend
	if err := applyE2EFeatures(cfg, reg, func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
}
