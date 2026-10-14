package config

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Backend names accepted by MM_BACKEND.
const (
	BackendNone   = "none"
	BackendFake   = "fake"
	BackendRunPod = "runpod"
)

// loadM3 reads the generation settings (C68, D-M3-16). Errors name the
// variable, never a value.
func loadM3(c *Config, getenv func(string) string) error {
	c.Backend = orDefault(getenv("MM_BACKEND"), BackendNone)
	switch c.Backend {
	case BackendNone, BackendFake, BackendRunPod:
	default:
		return errors.New(`config: MM_BACKEND must be "none", "fake" or "runpod"`)
	}
	c.Runner = true
	if v := getenv("MM_RUNNER"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return errors.New("config: MM_RUNNER must be a boolean")
		}
		c.Runner = b
	}
	if v := getenv("MM_FAKE_BACKEND"); v != "" {
		if c.Backend != BackendFake {
			return errors.New(`config: MM_FAKE_BACKEND is only valid with MM_BACKEND=fake`)
		}
		if v != "fast" {
			d, ok := strings.CutPrefix(v, "slow:")
			dur, err := time.ParseDuration(d)
			if !ok || err != nil || dur <= 0 || dur > time.Hour {
				return errors.New(`config: MM_FAKE_BACKEND must be "fast" or "slow:<duration>" (0 < duration <= 1h)`)
			}
		}
		c.FakeBackend = v
	}
	var err error
	if c.JobMaxRuntime, err = durationVar(getenv, "MM_JOB_MAX_RUNTIME", 15*time.Minute, 30*time.Second, 45*time.Minute); err != nil {
		return err
	}
	if c.JobTTL, err = durationVar(getenv, "MM_JOB_TTL", 2*time.Hour, 10*time.Minute, 24*time.Hour); err != nil {
		return err
	}
	if c.BillingLag, err = durationVar(getenv, "MM_BILLING_LAG", 6*time.Hour, time.Hour, 72*time.Hour); err != nil {
		return err
	}
	if c.ResidualHold, err = durationVar(getenv, "MM_RESIDUAL_HOLD", 48*time.Hour, time.Hour, 720*time.Hour); err != nil {
		return err
	}
	var n int64
	if n, err = intVar(getenv, "MM_KILL_SLACK_S", 15, 0, 120); err != nil {
		return err
	}
	c.KillSlackS = int(n)
	if n, err = intVar(getenv, "MM_COLD_START_S", 180, 30, 900); err != nil {
		return err
	}
	c.ColdStartS = int(n)
	if c.PriceMarginPPM, err = intVar(getenv, "MM_PRICE_MARGIN_PPM", 100_000, 0, 1_000_000); err != nil {
		return err
	}
	if v := getenv("MM_RUNPOD_RECORD_DIR"); v != "" {
		if err := checkRecordDir(c.DataDir, v); err != nil {
			return err
		}
		c.RunPodRecordDir = v
	}
	if c.Backend == BackendRunPod {
		if err := checkWorkerPublicURL(c.PublicURL); err != nil {
			return err
		}
	}
	return nil
}

func durationVar(getenv func(string) string, name string, def, lo, hi time.Duration) (time.Duration, error) {
	v := getenv(name)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < lo || d > hi {
		return 0, fmt.Errorf("config: %s must be a duration in %s..%s", name, lo, hi)
	}
	return d, nil
}

func intVar(getenv func(string) string, name string, def, lo, hi int64) (int64, error) {
	v := getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("config: %s must be an integer in %d..%d", name, lo, hi)
	}
	return n, nil
}

func checkRecordDir(dataDir, dir string) error {
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return errors.New("config: MM_RUNPOD_RECORD_DIR must be an existing directory")
	}
	d, err1 := filepath.Abs(dataDir)
	r, err2 := filepath.Abs(dir)
	if err1 != nil || err2 != nil {
		return errors.New("config: MM_RUNPOD_RECORD_DIR: cannot resolve path")
	}
	if rel, err := filepath.Rel(d, r); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("config: MM_RUNPOD_RECORD_DIR must lie outside MM_DATA_DIR")
	}
	return nil
}

// checkWorkerPublicURL requires https for the URL workers upload to, unless
// the host is loopback (tests). The value is never echoed.
func checkWorkerPublicURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("config: MM_PUBLIC_URL must be an absolute URL when MM_BACKEND=runpod")
	}
	if u.Scheme == "https" {
		return nil
	}
	if ip, perr := netip.ParseAddr(u.Hostname()); u.Scheme == "http" && (u.Hostname() == "localhost" || (perr == nil && ip.IsLoopback())) {
		return nil
	}
	return errors.New("config: MM_PUBLIC_URL must be https when MM_BACKEND=runpod (workers upload to it)")
}
