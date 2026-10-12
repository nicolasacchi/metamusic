package config

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{"MM_AUTH": "local"}))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Addr: "127.0.0.1:8080", PublicURL: "http://127.0.0.1:8080", AuthMode: "local",
		DataDir: "./data", SecretsDir: "data/secrets",
		FFmpeg: "ffmpeg", FLAC: "flac", Metaflac: "metaflac", Inbox: "data/inbox",
		BackupDir: "backup", BackupAt: "03:30", RateScale: 1,
	}
	if diff := cmp.Diff(want, c, cmp.Comparer(func(a, b netip.Prefix) bool { return a == b })); diff != "" {
		t.Fatal(diff)
	}
}

func TestLoadAuthMode(t *testing.T) {
	for _, tc := range []struct {
		v  string
		ok bool
	}{{"", false}, {"bogus", false}, {"PROXY", false}, {"proxy", true}, {"local", true}} {
		_, err := Load(env(map[string]string{"MM_AUTH": tc.v}))
		if (err == nil) != tc.ok {
			t.Errorf("MM_AUTH=%q: err=%v, want ok=%v", tc.v, err, tc.ok)
		}
	}
}

func TestLoadOverridesAndLists(t *testing.T) {
	c, err := Load(env(map[string]string{
		"MM_AUTH": "proxy", "MM_ADDR": "0.0.0.0:9", "MM_PUBLIC_URL": "https://mm.example.com",
		"MM_DATA_DIR": "/d", "MM_TRUSTED_PROXY": "10.0.0.0/8, 192.168.1.5 ,::1",
		"MM_TRUSTED_USERS": "alice, bob,", "MM_E2E": "1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	wantP := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.168.1.5/32"), netip.MustParsePrefix("::1/128")}
	if diff := cmp.Diff(wantP, c.TrustedProxies, cmp.Comparer(func(a, b netip.Prefix) bool { return a == b })); diff != "" {
		t.Error(diff)
	}
	if diff := cmp.Diff([]string{"alice", "bob"}, c.TrustedUsers); diff != "" {
		t.Error(diff)
	}
	if c.Addr != "0.0.0.0:9" || c.PublicURL != "https://mm.example.com" || c.DataDir != "/d" ||
		c.SecretsDir != "/d/secrets" || !c.E2E {
		t.Errorf("unexpected: %+v", c)
	}
}

func TestLoadInvalidProxy(t *testing.T) {
	for _, v := range []string{"10.0.0.0/33", "nonsense", "1.2.3.4,x"} {
		if _, err := Load(env(map[string]string{"MM_AUTH": "proxy", "MM_TRUSTED_PROXY": v})); err == nil {
			t.Errorf("%q: expected error", v)
		}
	}
}

func TestLoadSecrets(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadSecrets(dir, "local"); err == nil || !strings.Contains(err.Error(), "secret_key") {
		t.Fatalf("want error naming secret_key, got %v", err)
	}
	hexKey := strings.Repeat("ab", 32)
	if err := os.WriteFile(filepath.Join(dir, "secret_key"), []byte(hexKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSecrets(dir, "local")
	if err != nil || len(s.Key) != 32 {
		t.Fatalf("hex key: %v len=%d", err, len(s.Key))
	}
	raw := strings.Repeat("\x01", 32)
	os.WriteFile(filepath.Join(dir, "secret_key"), []byte(raw), 0o600)
	if s, err = LoadSecrets(dir, "local"); err != nil || len(s.Key) != 32 {
		t.Fatalf("raw key: %v", err)
	}
	// proxy requires edge_secret; error names file, never contents
	_, err = LoadSecrets(dir, "proxy")
	if err == nil || !strings.Contains(err.Error(), "edge_secret") {
		t.Fatalf("want edge_secret error, got %v", err)
	}
	os.WriteFile(filepath.Join(dir, "edge_secret"), []byte("deadbeef\n"), 0o600)
	if s, err = LoadSecrets(dir, "proxy"); err != nil || s.EdgeSecret != "deadbeef" {
		t.Fatalf("edge: %v %q", err, s.EdgeSecret)
	}
	// bad key length: error must not leak bytes
	os.WriteFile(filepath.Join(dir, "secret_key"), []byte("SUPERSECRETSHORT"), 0o600)
	_, err = LoadSecrets(dir, "local")
	if err == nil || strings.Contains(err.Error(), "SUPERSECRET") || !strings.Contains(err.Error(), "secret_key") {
		t.Fatalf("bad key err: %v", err)
	}
}

func TestConfig_MediaTools(t *testing.T) {
	c, err := Load(env(map[string]string{"MM_AUTH": "local", "MM_DATA_DIR": "/d"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.FFmpeg != "ffmpeg" || c.FLAC != "flac" || c.Metaflac != "metaflac" || c.Inbox != "/d/inbox" {
		t.Fatalf("defaults: %+v", c)
	}
	c, err = Load(env(map[string]string{
		"MM_AUTH": "local", "MM_DATA_DIR": "/d", "MM_FFMPEG": "/x/ff", "MM_FLAC": "/x/fl",
		"MM_METAFLAC": "/x/mf", "MM_INBOX": "/in",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.FFmpeg != "/x/ff" || c.FLAC != "/x/fl" || c.Metaflac != "/x/mf" || c.Inbox != "/in" {
		t.Fatalf("overrides: %+v", c)
	}
}

func TestConfig_BackupAndNtfy(t *testing.T) {
	base := map[string]string{"MM_AUTH": "local", "MM_DATA_DIR": "/var/lib/mm/data"}
	with := func(kv ...string) func(string) string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return env(m)
	}
	c, err := Load(with())
	if err != nil {
		t.Fatal(err)
	}
	if c.BackupDir != "/var/lib/mm/backup" || c.BackupAt != "03:30" || c.RateScale != 1 || c.NtfyURL != "" {
		t.Fatalf("defaults = %+v", c)
	}
	bad := []struct{ name, key, val, want string }{
		{"backup at short", "MM_BACKUP_AT", "3:30", "MM_BACKUP_AT"},
		{"backup at hour", "MM_BACKUP_AT", "24:00", "MM_BACKUP_AT"},
		{"backup inside data", "MM_BACKUP_DIR", "/var/lib/mm/data/b", "MM_BACKUP_DIR"},
		{"backup equals data", "MM_BACKUP_DIR", "/var/lib/mm/data", "MM_BACKUP_DIR"},
		{"backup contains data", "MM_BACKUP_DIR", "/var/lib/mm", "MM_BACKUP_DIR"},
		{"ntfy http remote", "MM_NTFY_URL", "http://ntfy.example.com/t", "MM_NTFY_URL"},
		{"ntfy query", "MM_NTFY_URL", "https://ntfy.example.com/t?x=1", "MM_NTFY_URL"},
		{"ntfy fragment", "MM_NTFY_URL", "https://ntfy.example.com/t#x", "MM_NTFY_URL"},
		{"ntfy relative", "MM_NTFY_URL", "ntfy.example.com/t", "MM_NTFY_URL"},
		{"ntfy userinfo", "MM_NTFY_URL", "https://user:pass@ntfy.example.com/t", "MM_NTFY_URL"},
		{"scale zero", "MM_RATE_SCALE", "0", "MM_RATE_SCALE"},
		{"scale big", "MM_RATE_SCALE", "1001", "MM_RATE_SCALE"},
		{"scale text", "MM_RATE_SCALE", "x", "MM_RATE_SCALE"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(with(tc.key, tc.val))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want mention of %s", err, tc.want)
			}
			if strings.Contains(err.Error(), "pass") {
				t.Fatalf("error leaks the credential: %v", err)
			}
		})
	}
	c, err = Load(with("MM_BACKUP_AT", "off", "MM_NTFY_URL", "http://127.0.0.1:8090/t", "MM_RATE_SCALE", "100", "MM_BACKUP_DIR", "/srv/bk"))
	if err != nil {
		t.Fatal(err)
	}
	if c.BackupAt != "off" || c.NtfyURL != "http://127.0.0.1:8090/t" || c.RateScale != 100 || c.BackupDir != "/srv/bk" {
		t.Fatalf("overrides = %+v", c)
	}
	if _, err := Load(with("MM_NTFY_URL", "https://ntfy.example.com/t")); err != nil {
		t.Fatal(err)
	}
}

func TestConfig_LoadOffline(t *testing.T) {
	c, err := LoadOffline(env(map[string]string{"MM_DATA_DIR": "/d"}))
	if err != nil || c.AuthMode != "" {
		t.Fatalf("offline = %+v, %v", c, err)
	}
	if _, err := Load(env(map[string]string{"MM_DATA_DIR": "/d"})); err == nil {
		t.Fatal("Load must still refuse a missing MM_AUTH")
	}
	for _, kv := range [][2]string{{"MM_BACKUP_AT", "bogus"}, {"MM_RATE_SCALE", "0"}, {"MM_NTFY_URL", "http://ntfy.example.com/t"}, {"MM_BACKUP_DIR", "/d/x"}, {"MM_TRUSTED_PROXY", "nope"}} {
		if _, err := LoadOffline(env(map[string]string{"MM_DATA_DIR": "/d", kv[0]: kv[1]})); err == nil {
			t.Fatalf("%s not checked offline", kv[0])
		}
	}
}

func TestLoadSecrets_NtfyTokenOptional(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secret_key"), []byte(strings.Repeat("ab", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSecrets(dir, "local")
	if err != nil || s.NtfyToken != "" {
		t.Fatalf("missing ntfy_token: %+v, %v", s, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ntfy_token"), []byte("tk_canary\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err = LoadSecrets(dir, "local"); err != nil || s.NtfyToken != "tk_canary" {
		t.Fatalf("present ntfy_token: %+v, %v", s, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ntfy_token"), []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadSecrets(dir, "local"); err == nil || !strings.Contains(err.Error(), "ntfy_token") {
		t.Fatalf("empty ntfy_token err = %v", err)
	}
}
