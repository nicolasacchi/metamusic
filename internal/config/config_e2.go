package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

var poolIDRE = regexp.MustCompile(`^[A-Z0-9_]{2,40}$`)

// loadE2 reads the second-engine (ACE-Step) settings. It runs after loadM3: the runtime default is
// MM_JOB_MAX_RUNTIME. Errors name the variable, never a value. MM_LOAD_DEADLINE_S is a worker (endpoint
// environment) variable, not an app variable, and is not read here.
func loadE2(c *Config, getenv func(string) string) error {
	if v := getenv("MM_ACESTEP"); v != "" && v != "0" && v != "1" {
		return errors.New("config: MM_ACESTEP must be 0 or 1")
	}
	c.ACEStep = getenv("MM_ACESTEP") == "1"
	if c.ACEStep && (c.Backend == "" || c.Backend == BackendNone) {
		return errors.New("config: MM_ACESTEP=1 needs MM_BACKEND=fake or runpod")
	}
	if getenv("MM_E2E_ACESTEP_FEATURES") == "all" && c.Backend != BackendFake {
		// the e2e features swap offers unverified tasks; it may only ever meet a fake backend, never a GPU one
		return errors.New("config: MM_E2E_ACESTEP_FEATURES=all requires fake backends (MM_BACKEND=fake)")
	}
	n, err := intVar(getenv, "MM_ACESTEP_COLD_START_S", 300, 30, 900)
	if err != nil {
		return err
	}
	c.ACEStepColdStartS = int(n)
	if c.ACEStepMaxRuntime, err = durationVar(getenv, "MM_ACESTEP_MAX_RUNTIME", c.JobMaxRuntime, 30*time.Second, 45*time.Minute); err != nil {
		return err
	}
	c.ACEStepTasks = []string{"turbo"}
	if v := getenv("MM_ACESTEP_TASKS"); v != "" {
		set := map[string]bool{}
		for _, t := range strings.Split(v, ",") {
			set[strings.TrimSpace(t)] = true
		}
		switch {
		case len(set) == 1 && set["turbo"]:
		case len(set) == 2 && set["turbo"] && set["base"]:
			c.ACEStepTasks = []string{"turbo", "base"}
		default:
			return errors.New(`config: MM_ACESTEP_TASKS must be "turbo" or "turbo,base"`)
		}
	}
	c.ACEStepPools = []string{"AMPERE_24", "ADA_24"}
	if v := getenv("MM_ACESTEP_POOLS"); v != "" {
		var pools []string
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p == "" {
				continue
			}
			if !poolIDRE.MatchString(p) || slices.Contains(pools, p) {
				return fmt.Errorf("config: MM_ACESTEP_POOLS entries must be distinct RunPod pool ids matching %s", poolIDRE)
			}
			pools = append(pools, p)
		}
		if len(pools) == 0 {
			return errors.New("config: MM_ACESTEP_POOLS must list at least one pool id")
		}
		c.ACEStepPools = pools
	}
	return loadCanaryCaps(c, getenv)
}

// ACEStepCanaryCapNames is the vocabulary of MM_ACESTEP_CANARY_CAPS: the capability flags of features.verified.json that the
// canary verifies (text2music is always on). TestCanaryCapNamesMatchEngine in internal/app pins it to the engine's list.
var ACEStepCanaryCapNames = []string{"reference", "repaint", "extend", "complete", "extract", "lrc"}

// loadCanaryCaps reads MM_ACESTEP_CANARY_CAPS (empty or unset: off). The switch exists so the real-GPU canary can exercise a
// capability that is not verified yet; it is offered to canary batches only and needs MM_ACESTEP=1.
func loadCanaryCaps(c *Config, getenv func(string) string) error {
	v := getenv("MM_ACESTEP_CANARY_CAPS")
	if strings.TrimSpace(v) == "" {
		return nil
	}
	if !c.ACEStep {
		return errors.New("config: MM_ACESTEP_CANARY_CAPS needs MM_ACESTEP=1")
	}
	var caps []string
	for _, n := range strings.Split(v, ",") {
		if n = strings.TrimSpace(n); n == "" || slices.Contains(caps, n) {
			continue
		}
		if !slices.Contains(ACEStepCanaryCapNames, n) {
			return fmt.Errorf("config: MM_ACESTEP_CANARY_CAPS entries must be among %s", strings.Join(ACEStepCanaryCapNames, ","))
		}
		caps = append(caps, n)
	}
	c.ACEStepCanaryCaps = caps
	return nil
}
