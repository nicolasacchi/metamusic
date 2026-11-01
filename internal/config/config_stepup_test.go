package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadStepUp(t *testing.T) {
	t.Parallel()
	base := map[string]string{"MM_AUTH": "proxy", "MM_TRUSTED_USERS": "me", "MM_PUBLIC_URL": "https://mm.example.com",
		"MM_OIDC_ISSUER": "https://auth.example.com", "MM_OIDC_CLIENTS": "c=C"}
	get := func(over map[string]string) func(string) string {
		return func(k string) string {
			if v, ok := over[k]; ok {
				return v
			}
			return base[k]
		}
	}
	if c, err := Load(get(nil)); err != nil || c.StepUpClientID != "" {
		t.Fatalf("unset: %q %v", c.StepUpClientID, err)
	}
	if c, err := Load(get(map[string]string{"MM_STEPUP_OIDC_CLIENT": "metamusic-stepup"})); err != nil || c.StepUpClientID != "metamusic-stepup" {
		t.Fatalf("set: %q %v", c.StepUpClientID, err)
	}
	for name, over := range map[string]map[string]string{
		"bad id":    {"MM_STEPUP_OIDC_CLIENT": "bad id!"},
		"local":     {"MM_STEPUP_OIDC_CLIENT": "x", "MM_AUTH": "local"},
		"no issuer": {"MM_STEPUP_OIDC_CLIENT": "x", "MM_OIDC_ISSUER": "", "MM_OIDC_CLIENTS": ""},
	} {
		if _, err := Load(get(over)); err == nil || !strings.Contains(err.Error(), "MM_STEPUP_OIDC_CLIENT") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestLoadSecrets_StepUpClientSecret(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for n, v := range map[string]string{"secret_key": strings.Repeat("ab", 32), "edge_secret": "e"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if s, err := LoadSecrets(dir, "proxy"); err != nil || s.StepUpClientSecret != "" {
		t.Fatalf("absent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stepup_client_secret"), []byte("  shh \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := LoadSecrets(dir, "proxy"); err != nil || s.StepUpClientSecret != "shh" {
		t.Fatalf("present: %q %v", s.StepUpClientSecret, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stepup_client_secret"), []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSecrets(dir, "proxy"); err == nil || strings.Contains(err.Error(), "shh") {
		t.Fatalf("empty file: %v", err)
	}
}
