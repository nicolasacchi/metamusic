package app

import (
	"errors"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/engine/registry"
)

// e2eAllFeatures is a features file with every ACE-Step capability proved. It exists for the Playwright server only: the
// committed features.verified.json proves text2music alone until the canary runs, so the Repaint and Reference actions of the
// take page would stay off in an e2e run. It is read only when MM_E2E=1 and MM_E2E_ACESTEP_FEATURES=all (synthetic dates and
// commit).
const e2eAllFeatures = `{"schema":"mm.engine-features/2","engine":"acestep15","verified_at":"2026-01-01","canary_commit":"e2e0000",
 "tasks":["text2music","repaint","extend","complete","extract"],"reference":true,"reference_reproducible":false,"seed_reproducible":true,
 "lrc":true,"lrc_fixed_s":60,"models_mode":"bundle_cached_base_baked","max_s_default":480,
 "pools":{"AMPERE_24":{"max_s":480},"ADA_24":{"max_s":480}},"repaint":{"min_s":3,"max_s":90},"extend":{"min_s":3,"max_s":90},"reference_max_s":30}`

// applyE2EFeatures swaps the all-capabilities features file into the registered ACE-Step engine of an e2e server
// (MM_E2E=1, MM_E2E_ACESTEP_FEATURES=all). Anywhere else it does nothing.
func applyE2EFeatures(cfg config.Config, reg registry.Registry, getenv func(string) string) error {
	if getenv("MM_E2E_ACESTEP_FEATURES") != "all" {
		return nil
	}
	// refuse loudly: unverified tasks must never be offered against a real GPU backend (config check says the same)
	if cfg.Backend != config.BackendFake {
		return errors.New("app: MM_E2E_ACESTEP_FEATURES=all requires fake backends")
	}
	if !cfg.E2E || !cfg.ACEStep {
		return nil
	}
	ae, ok := reg[acestep.Name].(acestep.Engine)
	if !ok {
		return nil
	}
	f, err := acestep.ParseFeatures([]byte(e2eAllFeatures))
	if err != nil {
		return fmt.Errorf("app: e2e features: %w", err)
	}
	ae.Verified = f
	reg[acestep.Name] = ae
	return nil
}
