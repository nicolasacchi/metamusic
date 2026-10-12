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
