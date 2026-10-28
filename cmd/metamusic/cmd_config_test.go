package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/config"
)

var sentinelKey = strings.Repeat("ab12", 16) // 64 hex chars

const (
	sentinelEdge = "EDGE-SENTINEL-do-not-print"
	sentinelNtfy = "NTFY-TOKEN-SENTINEL"
)

// configEnv writes real secret files and returns a valid minimal proxy environment whose data
// directory does not exist.
func configEnv(t *testing.T, extra map[string]string) (map[string]string, string) {
	t.Helper()
	root := t.TempDir()
	sec := filepath.Join(root, "sec")
	if err := os.Mkdir(sec, 0o700); err != nil {
		t.Fatal(err)
	}
	for n, v := range map[string]string{"secret_key": sentinelKey, "edge_secret": sentinelEdge, "ntfy_token": sentinelNtfy} {
		if err := os.WriteFile(filepath.Join(sec, n), []byte(v+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	data := filepath.Join(root, "data")
	env := map[string]string{"MM_AUTH": "proxy", "MM_DATA_DIR": data, "MM_SECRETS_DIR": sec, "MM_BACKUP_DIR": filepath.Join(root, "backup")}
	for k, v := range extra {
		env[k] = v
	}
	return env, data
}

func TestConfigCheck(t *testing.T) {
	oidc := map[string]string{"MM_OIDC_ISSUER": "https://issuer.example.com", "MM_TRUSTED_USERS": "me", "MM_PUBLIC_URL": "https://mm.example.com"}
	with := func(kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range oidc {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	// The message serve itself gives for the same malformed variable.
	_, serveErr := config.Load(envFn(with("MM_AUTH", "proxy", "MM_OIDC_CLIENTS", "metamusic-claude")))
	if serveErr == nil {
		t.Fatal("config.Load accepted a client without =label")
	}
	tests := []struct {
		name     string
		extra    map[string]string
		unset    string
		args     []string
		code     int
		outHas   []string
		errLines []string // exact stderr lines
	}{
		{name: "valid minimal proxy", code: 0, outHas: []string{"config ok\n", "auth: proxy", "oidc: disabled", "ntfy: disabled"}},
		{name: "valid oidc", extra: with("MM_OIDC_CLIENTS", "metamusic-claude=Claude"), code: 0, outHas: []string{"oidc: enabled (1 client)"}},
		{name: "malformed oidc clients", extra: with("MM_OIDC_CLIENTS", "metamusic-claude"), code: 1, errLines: []string{serveErr.Error()}},
		{name: "auth unset", unset: "MM_AUTH", code: 1, errLines: []string{`config: MM_AUTH must be "proxy" or "local" (got "")`}},
		{name: "all errors reported", extra: map[string]string{"MM_RATE_SCALE": "0", "MM_BACKEND": "bogus", "MM_AUTH": "nope"}, code: 1, errLines: []string{
			"config: MM_RATE_SCALE must be an integer in 1..1000",
			`config: MM_BACKEND must be "none", "fake" or "runpod"`,
			`config: MM_AUTH must be "proxy" or "local" (got "nope")`}},
		{name: "usage", args: []string{"bogus"}, code: 2},
		{name: "extra arg", args: []string{"check", "x"}, code: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := configEnv(t, tc.extra)
			if tc.unset != "" {
				delete(env, tc.unset)
			}
			args := tc.args
			if args == nil {
				args = []string{"check"}
			}
			code, out, errs := do(t, env, "", append([]string{"config"}, args...)...)
			if code != tc.code {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", code, tc.code, out, errs)
			}
			for _, w := range tc.outHas {
				if !strings.Contains(out, w) {
					t.Errorf("stdout lacks %q: %q", w, out)
				}
			}
			if tc.code == 1 {
				if out != "" {
					t.Errorf("stdout on failure: %q", out)
				}
				if got := strings.Split(strings.TrimSpace(errs), "\n"); strings.Join(got, "\n") != strings.Join(tc.errLines, "\n") {
					t.Errorf("stderr lines %q, want %q", got, tc.errLines)
				}
			}
		})
	}
}

func TestConfigCheckMissingSecretsReported(t *testing.T) {
	env, _ := configEnv(t, nil)
	if err := os.Remove(filepath.Join(env["MM_SECRETS_DIR"], "edge_secret")); err != nil {
		t.Fatal(err)
	}
	code, _, errs := do(t, env, "", "config", "check")
	if code != 1 || !strings.HasPrefix(errs, "config: reading edge_secret") {
		t.Fatalf("exit %d stderr %q", code, errs)
	}
}

func TestConfigCheckPrintsNoSecrets(t *testing.T) {
	env, _ := configEnv(t, map[string]string{
		"MM_NTFY_URL":      "https://ntfy.example.com/topic",
		"MM_OIDC_ISSUER":   "https://issuer.example.com",
		"MM_TRUSTED_USERS": "me",
		"MM_PUBLIC_URL":    "https://mm.example.com",
		"MM_OIDC_CLIENTS":  "client-sentinel-id=Label",
	})
	secret := []string{sentinelKey, sentinelEdge, sentinelNtfy}
	for _, tc := range []struct {
		name  string
		extra map[string]string
	}{
		{"ok", nil},
		// Secret-bearing URLs that fail validation must not be echoed back either.
		{"bad ntfy url", map[string]string{"MM_NTFY_URL": "https://user:pw-SENTINEL@ntfy.example.com/t?token=q-SENTINEL"}},
		{"bad jwks url", map[string]string{"MM_OIDC_JWKS_URL": "https://j.example.com/k?sig=q-SENTINEL"}},
		{"bad public url", map[string]string{"MM_PUBLIC_URL": "http://pub.example.com/?k=q-SENTINEL"}},
	} {
		e := map[string]string{}
		for k, v := range env {
			e[k] = v
		}
		for k, v := range tc.extra {
			e[k] = v
		}
		_, out, errs := do(t, e, "", "config", "check")
		for _, s := range append(secret, "q-SENTINEL", "pw-SENTINEL") {
			if strings.Contains(out, s) || strings.Contains(errs, s) {
				t.Errorf("%s: output leaks %q\nstdout: %s\nstderr: %s", tc.name, s, out, errs)
			}
		}
	}
}

func TestConfigCheckDoesNotCreateDataDir(t *testing.T) {
	env, data := configEnv(t, nil)
	if code, out, errs := do(t, env, "", "config", "check"); code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, errs)
	}
	for _, p := range []string{data, env["MM_BACKUP_DIR"]} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("config check created %s (stat err %v)", p, err)
		}
	}
}

func TestHelpListsConfigCheck(t *testing.T) {
	_, out, _ := do(t, nil, "", "help")
	if !strings.Contains(out, "config check") {
		t.Error("help does not mention config check")
	}
}
