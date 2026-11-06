package app

import (
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
		{"e2e with the switch", config.Config{E2E: true, ACEStep: true}, "all", true},
		{"not an e2e server", config.Config{ACEStep: true}, "all", false},
		{"switch unset", config.Config{E2E: true, ACEStep: true}, "", false},
		{"engine off", config.Config{E2E: true}, "all", false},
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
