package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"
)

var (
	podImageRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:-]*@sha256:[0-9a-f]{64}$`)
	// catalog GPU type ids are display names such as "NVIDIA GeForce RTX 4090"
	podGPUTypeRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._()/+-]{1,63}$`)
)

// envSecrets are the variables that must never be in the process environment (C390.5): the pod key and the upload-fallback
// credentials are files in the secrets directory. The environment cannot be enumerated through getenv, so the names are listed.
var envSecrets = []string{
	"RUNPOD_POD_KEY",
	"R2_ENDPOINT", "R2_BUCKET", "R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY", "R2_REGION", "R2_PREFIX",
}

// loadBulk reads the bulk settings (C390.2): the pod backend and the upload fallback's switch-over time. It runs after loadM3
// (MM_JOB_TTL) and refuses secrets in the environment. Errors name the variable, never the value.
func loadBulk(c *Config, getenv func(string) string) error {
	for _, name := range envSecrets {
		if getenv(name) != "" {
			return fmt.Errorf("config: %s must not be set in the environment (secrets are files in the secrets directory)", name)
		}
	}
	switch getenv("MM_POD_BACKEND") {
	case "", "0":
	case "1":
		c.PodBackend = true
	default:
		return errors.New("config: MM_POD_BACKEND must be 0 or 1")
	}
	if v := getenv("MM_POD_IMAGE"); v != "" {
		if !podImageRE.MatchString(v) {
			return errors.New("config: MM_POD_IMAGE must be <image>@sha256:<64 hex>")
		}
		c.PodImage = v
	} else if c.PodBackend {
		return errors.New("config: MM_POD_IMAGE is required with MM_POD_BACKEND=1")
	}
	if v := getenv("MM_POD_GPU_TYPES"); v != "" || c.PodBackend {
		var types []string
		for _, t := range splitList(v) {
			if !podGPUTypeRE.MatchString(t) || slices.Contains(types, t) {
				return errors.New("config: MM_POD_GPU_TYPES entries must be distinct catalog GPU type ids")
			}
			types = append(types, t)
		}
		if len(types) < 1 || len(types) > 4 {
			return errors.New("config: MM_POD_GPU_TYPES must list 1..4 catalog GPU type ids")
		}
		c.PodGPUTypes = types
	}
	for _, f := range []struct {
		name        string
		dst         *int
		def, lo, hi int64
	}{
		{"MM_POD_MIN_TAKES", &c.PodMinTakes, 30, 10, 100},
		{"MM_POD_MAX_TAKES", &c.PodMaxTakes, 100, 30, 100},
		{"MM_POD_BOOT_S", &c.PodBootS, 1200, 300, 3600},
		{"MM_POD_IDLE_S", &c.PodIdleS, 120, 30, 600},
		{"MM_POD_SLACK_S", &c.PodSlackS, 300, 60, 1800},
		{"MM_POD_DISK_GB", &c.PodDiskGB, 40, 20, 200},
		{"MM_FALLBACK_AFTER_S", &c.FallbackAfterS, 120, 60, 600},
	} {
		n, err := intVar(getenv, f.name, f.def, f.lo, f.hi)
		if err != nil {
			return err
		}
		*f.dst = int(n)
	}
	if c.PodMinTakes > c.PodMaxTakes {
		return errors.New("config: MM_POD_MIN_TAKES must not exceed MM_POD_MAX_TAKES")
	}
	var err error
	for _, f := range []struct {
		name        string
		dst         *time.Duration
		def, lo, hi time.Duration
	}{
		{"MM_POD_MAX_RUNTIME", &c.PodMaxRuntime, 6 * time.Hour, time.Hour, 12 * time.Hour},
		{"MM_POD_DEADMAN", &c.PodDeadman, 10 * time.Minute, 2 * time.Minute, 30 * time.Minute},
		{"MM_POD_ORPHAN_AFTER", &c.PodOrphanAfter, 180 * time.Second, 60 * time.Second, 900 * time.Second},
		{"MM_POD_WINDOW_MAX", &c.PodWindowMax, 8 * time.Hour, time.Hour, 24 * time.Hour},
	} {
		if *f.dst, err = durationVar(getenv, f.name, f.def, f.lo, f.hi); err != nil {
			return err
		}
	}
	if c.PodBackend && c.JobTTL < time.Duration(c.PodBootS)*time.Second+10*time.Minute {
		return errors.New("config: MM_JOB_TTL must be at least MM_POD_BOOT_S plus 10 minutes with MM_POD_BACKEND=1 (a booting pod must not expire its first take)")
	}
	return nil
}
