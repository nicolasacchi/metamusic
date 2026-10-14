package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfig_M3(t *testing.T) {
	t.Parallel()
	c, err := Load(env(map[string]string{"MM_AUTH": "local"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Backend != "none" || !c.Runner || c.FakeBackend != "" || c.JobMaxRuntime != 15*time.Minute ||
		c.KillSlackS != 15 || c.JobTTL != 2*time.Hour || c.ColdStartS != 180 || c.PriceMarginPPM != 100000 ||
		c.BillingLag != 6*time.Hour || c.ResidualHold != 48*time.Hour || c.RunPodRecordDir != "" {
		t.Fatalf("defaults: %+v", c)
	}

	data := t.TempDir()
	inside := filepath.Join(data, "rec")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	type tc struct {
		name    string
		env     map[string]string
		wantErr string // "" ⇒ ok; otherwise the variable the error must name
	}
	cases := []tc{
		{"backend fake", map[string]string{"MM_BACKEND": "fake"}, ""},
		{"backend bad", map[string]string{"MM_BACKEND": "x"}, "MM_BACKEND"},
		{"fake slow", map[string]string{"MM_BACKEND": "fake", "MM_FAKE_BACKEND": "slow:2s"}, ""},
		{"fake fast", map[string]string{"MM_BACKEND": "fake", "MM_FAKE_BACKEND": "fast"}, ""},
		{"fake slow bad", map[string]string{"MM_BACKEND": "fake", "MM_FAKE_BACKEND": "slow:x"}, "MM_FAKE_BACKEND"},
		{"fake bad mode", map[string]string{"MM_BACKEND": "fake", "MM_FAKE_BACKEND": "turbo"}, "MM_FAKE_BACKEND"},
		{"fake with runpod", map[string]string{"MM_BACKEND": "runpod", "MM_FAKE_BACKEND": "slow:2s", "MM_PUBLIC_URL": "https://mm.example.com"}, "MM_FAKE_BACKEND"},
		{"fake with none", map[string]string{"MM_FAKE_BACKEND": "fast"}, "MM_FAKE_BACKEND"},
		{"runtime 50m", map[string]string{"MM_JOB_MAX_RUNTIME": "50m"}, "MM_JOB_MAX_RUNTIME"},
		{"runtime 30m", map[string]string{"MM_JOB_MAX_RUNTIME": "30m"}, ""},
		{"runtime 30s", map[string]string{"MM_JOB_MAX_RUNTIME": "30s"}, ""},
		{"runtime 20s", map[string]string{"MM_JOB_MAX_RUNTIME": "20s"}, "MM_JOB_MAX_RUNTIME"},
		{"runtime junk", map[string]string{"MM_JOB_MAX_RUNTIME": "soon"}, "MM_JOB_MAX_RUNTIME"},
		{"slack 121", map[string]string{"MM_KILL_SLACK_S": "121"}, "MM_KILL_SLACK_S"},
		{"slack 0", map[string]string{"MM_KILL_SLACK_S": "0"}, ""},
		{"margin -1", map[string]string{"MM_PRICE_MARGIN_PPM": "-1"}, "MM_PRICE_MARGIN_PPM"},
		{"margin 1e6", map[string]string{"MM_PRICE_MARGIN_PPM": "1000000"}, ""},
		{"margin 1e6+1", map[string]string{"MM_PRICE_MARGIN_PPM": "1000001"}, "MM_PRICE_MARGIN_PPM"},
		{"lag 30m", map[string]string{"MM_BILLING_LAG": "30m"}, "MM_BILLING_LAG"},
		{"lag 72h", map[string]string{"MM_BILLING_LAG": "72h"}, ""},
		{"ttl 5m", map[string]string{"MM_JOB_TTL": "5m"}, "MM_JOB_TTL"},
		{"ttl 24h", map[string]string{"MM_JOB_TTL": "24h"}, ""},
		{"cold 10", map[string]string{"MM_COLD_START_S": "10"}, "MM_COLD_START_S"},
		{"cold 900", map[string]string{"MM_COLD_START_S": "900"}, ""},
		{"hold 30m", map[string]string{"MM_RESIDUAL_HOLD": "30m"}, "MM_RESIDUAL_HOLD"},
		{"hold 720h", map[string]string{"MM_RESIDUAL_HOLD": "720h"}, ""},
		{"runner junk", map[string]string{"MM_RUNNER": "maybe"}, "MM_RUNNER"},
		{"runner off", map[string]string{"MM_RUNNER": "false"}, ""},
		{"runpod http public", map[string]string{"MM_BACKEND": "runpod", "MM_PUBLIC_URL": "http://mm.example.com"}, "MM_PUBLIC_URL"},
		{"runpod https public", map[string]string{"MM_BACKEND": "runpod", "MM_PUBLIC_URL": "https://mm.example.com"}, ""},
		{"runpod loopback", map[string]string{"MM_BACKEND": "runpod", "MM_PUBLIC_URL": "http://127.0.0.1:8080"}, ""},
		{"record dir missing", map[string]string{"MM_RUNPOD_RECORD_DIR": "/nonexistent/dir/x"}, "MM_RUNPOD_RECORD_DIR"},
		{"record dir ok", map[string]string{"MM_RUNPOD_RECORD_DIR": t.TempDir(), "MM_DATA_DIR": "/srv/elsewhere"}, ""},
		{"record dir inside data", map[string]string{"MM_RUNPOD_RECORD_DIR": inside, "MM_DATA_DIR": data, "MM_BACKUP_DIR": t.TempDir()}, "MM_RUNPOD_RECORD_DIR"},
	}
	for _, tc := range cases {
		for _, offline := range []bool{false, true} {
			e := map[string]string{"MM_AUTH": "local"}
			for k, v := range tc.env {
				e[k] = v
			}
			var err error
			if offline {
				_, err = LoadOffline(env(e))
			} else {
				_, err = Load(env(e))
			}
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("%s (offline=%v): unexpected error %v", tc.name, offline, err)
			case tc.wantErr != "" && err == nil:
				t.Errorf("%s (offline=%v): want error naming %s", tc.name, offline, tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Errorf("%s (offline=%v): error %q does not name %s", tc.name, offline, err, tc.wantErr)
			}
		}
	}
}

func TestConfig_M3ErrorsNeverEchoValues(t *testing.T) {
	t.Parallel()
	_, err := Load(env(map[string]string{"MM_AUTH": "local", "MM_BACKEND": "runpod", "MM_PUBLIC_URL": "http://user:secretpass@mm.example.com"}))
	if err == nil || strings.Contains(err.Error(), "secretpass") {
		t.Fatalf("err = %v", err)
	}
}
