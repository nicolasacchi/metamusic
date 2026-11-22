package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const podDigest = "ghcr.io/example/mm-worker@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func bulkEnv(extra map[string]string) func(string) string {
	m := map[string]string{"MM_AUTH": "local"}
	for k, v := range extra {
		m[k] = v
	}
	return env(m)
}

func TestConfig_PodVars(t *testing.T) {
	t.Parallel()
	t.Run("defaults", func(t *testing.T) {
		c, err := Load(bulkEnv(nil))
		if err != nil {
			t.Fatal(err)
		}
		if c.PodBackend || c.PodImage != "" || c.PodGPUTypes != nil {
			t.Errorf("backend, image or types set by default: %v %q %v", c.PodBackend, c.PodImage, c.PodGPUTypes)
		}
		if c.PodMinTakes != 30 || c.PodMaxTakes != 100 || c.PodBootS != 1200 || c.PodIdleS != 120 || c.PodSlackS != 300 ||
			c.PodDiskGB != 40 || c.FallbackAfterS != 120 {
			t.Errorf("int defaults = %d %d %d %d %d %d %d", c.PodMinTakes, c.PodMaxTakes, c.PodBootS, c.PodIdleS, c.PodSlackS, c.PodDiskGB, c.FallbackAfterS)
		}
		if c.PodMaxRuntime != 6*time.Hour || c.PodDeadman != 10*time.Minute || c.PodOrphanAfter != 180*time.Second || c.PodWindowMax != 8*time.Hour {
			t.Errorf("duration defaults = %v %v %v %v", c.PodMaxRuntime, c.PodDeadman, c.PodOrphanAfter, c.PodWindowMax)
		}
	})
	t.Run("backend on", func(t *testing.T) {
		c, err := Load(bulkEnv(map[string]string{"MM_POD_BACKEND": "1", "MM_POD_IMAGE": podDigest,
			"MM_POD_GPU_TYPES": "NVIDIA GeForce RTX 4090, NVIDIA L40S", "MM_JOB_TTL": "2h"}))
		if err != nil {
			t.Fatal(err)
		}
		if !c.PodBackend || c.PodImage != podDigest || len(c.PodGPUTypes) != 2 || c.PodGPUTypes[0] != "NVIDIA GeForce RTX 4090" || c.PodGPUTypes[1] != "NVIDIA L40S" {
			t.Errorf("parsed = %v %q %q", c.PodBackend, c.PodImage, c.PodGPUTypes)
		}
	})
	on := map[string]string{"MM_POD_BACKEND": "1", "MM_POD_IMAGE": podDigest, "MM_POD_GPU_TYPES": "NVIDIA L4"}
	with := func(k, v string) map[string]string {
		m := map[string]string{}
		for a, b := range on {
			m[a] = b
		}
		m[k] = v
		return m
	}
	bad := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"backend 2", map[string]string{"MM_POD_BACKEND": "2"}, "MM_POD_BACKEND"},
		{"backend yes", map[string]string{"MM_POD_BACKEND": "yes"}, "MM_POD_BACKEND"},
		{"image without digest", with("MM_POD_IMAGE", "ghcr.io/example/mm-worker:latest"), "MM_POD_IMAGE"},
		{"image short digest", with("MM_POD_IMAGE", "ghcr.io/example/mm-worker@sha256:abc"), "MM_POD_IMAGE"},
		{"image missing with backend", map[string]string{"MM_POD_BACKEND": "1", "MM_POD_GPU_TYPES": "NVIDIA L4"}, "MM_POD_IMAGE"},
		{"types empty with backend", map[string]string{"MM_POD_BACKEND": "1", "MM_POD_IMAGE": podDigest}, "MM_POD_GPU_TYPES"},
		{"types five", with("MM_POD_GPU_TYPES", "a1,b2,c3,d4,e5"), "MM_POD_GPU_TYPES"},
		{"types duplicate", with("MM_POD_GPU_TYPES", "NVIDIA L4,NVIDIA L4"), "MM_POD_GPU_TYPES"},
		{"types bad char", with("MM_POD_GPU_TYPES", "NVIDIA;L4"), "MM_POD_GPU_TYPES"},
		{"types bad without backend", map[string]string{"MM_POD_GPU_TYPES": "x;y"}, "MM_POD_GPU_TYPES"},
		{"min 9", map[string]string{"MM_POD_MIN_TAKES": "9"}, "MM_POD_MIN_TAKES"},
		{"min 101", map[string]string{"MM_POD_MIN_TAKES": "101"}, "MM_POD_MIN_TAKES"},
		{"max 29", map[string]string{"MM_POD_MAX_TAKES": "29"}, "MM_POD_MAX_TAKES"},
		{"max 101", map[string]string{"MM_POD_MAX_TAKES": "101"}, "MM_POD_MAX_TAKES"},
		{"min above max", map[string]string{"MM_POD_MIN_TAKES": "50", "MM_POD_MAX_TAKES": "40"}, "MM_POD_MIN_TAKES"},
		{"boot 299", map[string]string{"MM_POD_BOOT_S": "299"}, "MM_POD_BOOT_S"},
		{"boot 3601", map[string]string{"MM_POD_BOOT_S": "3601"}, "MM_POD_BOOT_S"},
		{"idle 29", map[string]string{"MM_POD_IDLE_S": "29"}, "MM_POD_IDLE_S"},
		{"idle 601", map[string]string{"MM_POD_IDLE_S": "601"}, "MM_POD_IDLE_S"},
		{"slack 59", map[string]string{"MM_POD_SLACK_S": "59"}, "MM_POD_SLACK_S"},
		{"slack 1801", map[string]string{"MM_POD_SLACK_S": "1801"}, "MM_POD_SLACK_S"},
		{"runtime 59m", map[string]string{"MM_POD_MAX_RUNTIME": "59m"}, "MM_POD_MAX_RUNTIME"},
		{"runtime 13h", map[string]string{"MM_POD_MAX_RUNTIME": "13h"}, "MM_POD_MAX_RUNTIME"},
		{"runtime junk", map[string]string{"MM_POD_MAX_RUNTIME": "soon"}, "MM_POD_MAX_RUNTIME"},
		{"deadman 1m", map[string]string{"MM_POD_DEADMAN": "1m"}, "MM_POD_DEADMAN"},
		{"deadman 31m", map[string]string{"MM_POD_DEADMAN": "31m"}, "MM_POD_DEADMAN"},
		{"orphan 59s", map[string]string{"MM_POD_ORPHAN_AFTER": "59s"}, "MM_POD_ORPHAN_AFTER"},
		{"orphan 901s", map[string]string{"MM_POD_ORPHAN_AFTER": "901s"}, "MM_POD_ORPHAN_AFTER"},
		{"window 59m", map[string]string{"MM_POD_WINDOW_MAX": "59m"}, "MM_POD_WINDOW_MAX"},
		{"window 25h", map[string]string{"MM_POD_WINDOW_MAX": "25h"}, "MM_POD_WINDOW_MAX"},
		{"disk 19", map[string]string{"MM_POD_DISK_GB": "19"}, "MM_POD_DISK_GB"},
		{"disk 201", map[string]string{"MM_POD_DISK_GB": "201"}, "MM_POD_DISK_GB"},
		{"fallback 59", map[string]string{"MM_FALLBACK_AFTER_S": "59"}, "MM_FALLBACK_AFTER_S"},
		{"fallback 601", map[string]string{"MM_FALLBACK_AFTER_S": "601"}, "MM_FALLBACK_AFTER_S"},
	}
	for _, b := range bad {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			_, err := Load(bulkEnv(b.env))
			if err == nil || !strings.Contains(err.Error(), b.want) {
				t.Fatalf("err = %v, want one naming %s", err, b.want)
			}
			for _, v := range b.env {
				if len(v) > 3 && strings.Contains(err.Error(), v) {
					t.Errorf("the error echoes the value %q: %v", v, err)
				}
			}
		})
	}
	t.Run("bounds accepted", func(t *testing.T) {
		c, err := Load(bulkEnv(map[string]string{"MM_POD_MIN_TAKES": "10", "MM_POD_MAX_TAKES": "30", "MM_POD_BOOT_S": "3600", "MM_POD_IDLE_S": "30",
			"MM_POD_SLACK_S": "1800", "MM_POD_MAX_RUNTIME": "12h", "MM_POD_DEADMAN": "2m", "MM_POD_ORPHAN_AFTER": "900s", "MM_POD_WINDOW_MAX": "24h",
			"MM_POD_DISK_GB": "200", "MM_FALLBACK_AFTER_S": "600"}))
		if err != nil {
			t.Fatal(err)
		}
		if c.PodMinTakes != 10 || c.PodMaxTakes != 30 || c.PodBootS != 3600 || c.PodWindowMax != 24*time.Hour || c.FallbackAfterS != 600 {
			t.Errorf("bounds = %+v", c)
		}
	})
}

func TestConfig_PodTTLCoversBoot(t *testing.T) {
	t.Parallel()
	on := map[string]string{"MM_POD_BACKEND": "1", "MM_POD_IMAGE": podDigest, "MM_POD_GPU_TYPES": "NVIDIA L4"}
	with := func(ttl, boot string) map[string]string {
		m := map[string]string{"MM_JOB_TTL": ttl}
		for k, v := range on {
			m[k] = v
		}
		if boot != "" {
			m["MM_POD_BOOT_S"] = boot
		}
		return m
	}
	// default boot 1200 s = 20 min, so the TTL must be at least 30 min
	if _, err := Load(bulkEnv(with("29m", ""))); err == nil || !strings.Contains(err.Error(), "MM_JOB_TTL") {
		t.Errorf("ttl 29m with boot 20m: err = %v, want one naming MM_JOB_TTL", err)
	}
	if _, err := Load(bulkEnv(with("30m", ""))); err != nil {
		t.Errorf("ttl 30m with boot 20m: %v", err)
	}
	if _, err := Load(bulkEnv(with("59m", "3000"))); err == nil {
		t.Error("ttl 59m with boot 50m must be refused")
	}
	if _, err := Load(bulkEnv(with("2h", "3000"))); err != nil {
		t.Errorf("ttl 2h with boot 50m: %v", err)
	}
	// with the backend off the rule does not apply
	if _, err := Load(bulkEnv(map[string]string{"MM_JOB_TTL": "10m"})); err != nil {
		t.Errorf("backend off: %v", err)
	}
}

func TestConfig_RefusesSecretsInEnv(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"RUNPOD_POD_KEY", "R2_ENDPOINT", "R2_BUCKET", "R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY", "R2_REGION", "R2_PREFIX"} {
		val := "fixture-value-" + strings.ToLower(name)
		_, err := Load(bulkEnv(map[string]string{name: val}))
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v, want an error naming the variable", name, err)
		}
		if err != nil && strings.Contains(err.Error(), val) {
			t.Errorf("%s: the error echoes the value", name)
		}
		if _, errs := LoadAll(bulkEnv(map[string]string{name: val})); len(errs) == 0 {
			t.Errorf("%s: LoadAll (config check) accepted it", name)
		}
	}
}

func TestPodPollKey_Derived(t *testing.T) {
	t.Parallel()
	a := Secrets{Key: []byte("0123456789abcdef0123456789abcdef")}
	b := Secrets{Key: []byte("fedcba9876543210fedcba9876543210")}
	k := a.PodPollKey()
	if len(k) != 32 {
		t.Fatalf("len = %d, want 32", len(k))
	}
	if string(k) != string(a.PodPollKey()) {
		t.Error("not deterministic")
	}
	if string(k) == string(b.PodPollKey()) {
		t.Error("two secrets gave one key")
	}
	for name, other := range map[string][]byte{"upload": a.UploadKey(), "confirm": a.ConfirmKey(), "meter": a.MeterKey()} {
		if string(k) == string(other) {
			t.Errorf("the poll key equals the %s key", name)
		}
	}
}

// podKeyLines builds the file at run time, so no assignment literal sits in the repo (gitleaks).
func podKeyLines(kv ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(kv); i += 2 {
		b.WriteString(kv[i] + "=" + kv[i+1] + "\n")
	}
	return b.String()
}

const podKeyName = "RUNPOD_" + "POD_KEY"

func writePodKey(t *testing.T, dir, body string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, PodKeyFile)
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func TestPodKey_ReadPerCall(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if k, ok, err := LoadPodKey(dir); err != nil || ok || k != (PodKey{}) {
		t.Fatalf("absent: %v %v %v", k, ok, err)
	}
	secret := "canary-pod-" + strings.Repeat("k", 16)
	writePodKey(t, dir, podKeyLines(podKeyName, secret), 0o600)
	k, ok, err := LoadPodKey(dir)
	if err != nil || !ok || k.Value() != secret {
		t.Fatalf("first call: %v %v %v", k, ok, err)
	}
	if err := os.Remove(filepath.Join(dir, PodKeyFile)); err != nil {
		t.Fatal(err)
	}
	if k, ok, err = LoadPodKey(dir); err != nil || ok || k.Value() != "" {
		t.Fatalf("after removal the second call must be absent: %v %v %v", k, ok, err)
	}
	// a rotated file is seen at once
	writePodKey(t, dir, podKeyLines(podKeyName, secret+"-2"), 0o400)
	if k, ok, err = LoadPodKey(dir); err != nil || !ok || k.Value() != secret+"-2" {
		t.Fatalf("rotated: %v %v %v", k, ok, err)
	}
	for _, s := range []string{fmtAll(k), k.String(), k.GoString()} {
		if strings.Contains(s, secret) {
			t.Errorf("the key leaked through formatting: %s", s)
		}
	}
	if k.String() != "PodKey{set}" {
		t.Errorf("String = %q", k.String())
	}
}

func TestPodKey_RefusesLooseMode(t *testing.T) {
	t.Parallel()
	for _, mode := range []os.FileMode{0o640, 0o604, 0o644, 0o660, 0o666, 0o607} {
		dir := t.TempDir()
		writePodKey(t, dir, podKeyLines(podKeyName, "canary-pod-"+strings.Repeat("k", 16)), mode)
		_, ok, err := LoadPodKey(dir)
		if err == nil || ok || !strings.Contains(err.Error(), PodKeyFile) || strings.Contains(err.Error(), "canary-pod") {
			t.Errorf("mode %o: ok=%v err=%v, want a refusal naming the file only", mode, ok, err)
		}
	}
	for _, mode := range []os.FileMode{0o600, 0o400} {
		dir := t.TempDir()
		writePodKey(t, dir, podKeyLines(podKeyName, "canary-pod-"+strings.Repeat("k", 16)), mode)
		if _, ok, err := LoadPodKey(dir); err != nil || !ok {
			t.Errorf("mode %o: ok=%v err=%v", mode, ok, err)
		}
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, PodKeyFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := LoadPodKey(dir); err == nil || ok {
		t.Errorf("a directory: ok=%v err=%v", ok, err)
	}
}

func TestPodKey_RefusesOtherVars(t *testing.T) {
	t.Parallel()
	v := "canary-pod-" + strings.Repeat("k", 16)
	for name, body := range map[string]string{
		"all-access api key":   podKeyLines(podKeyName, v, "RUNPOD_"+"API_KEY", v+"x"),
		"all-access admin key": podKeyLines("RUNPOD_"+"ADMIN_KEY", v),
		"serverless key":       podKeyLines(podKeyName, v, "RUNPOD_"+"JOBS_KEY", v+"x"),
		"other variable":       podKeyLines(podKeyName, v, "FOO", "bar"),
		"missing":              "# nothing\n",
		"empty":                podKeyLines(podKeyName, ""),
		"duplicated":           podKeyLines(podKeyName, v, podKeyName, v),
		"not key value":        "just words\n",
	} {
		dir := t.TempDir()
		writePodKey(t, dir, body, 0o600)
		_, ok, err := LoadPodKey(dir)
		if err == nil || ok {
			t.Errorf("%s: ok=%v err=%v, want a refusal", name, ok, err)
			continue
		}
		if strings.Contains(err.Error(), v) {
			t.Errorf("%s: the error echoes a value: %v", name, err)
		}
		if !strings.Contains(err.Error(), PodKeyFile) {
			t.Errorf("%s: the error does not name the file: %v", name, err)
		}
	}
}
