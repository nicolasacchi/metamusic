package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseTTL(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]time.Duration{
		"0": 0, "never": 0, "NEVER": 0, "90d": 90 * 24 * time.Hour, "720h": 720 * time.Hour, "3650d": 3650 * 24 * time.Hour,
	} {
		if got, err := parseTTL(in); err != nil || got != want {
			t.Errorf("parseTTL(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "-1d", "0d", "3651d", "abc", "-5h", "10x"} {
		if _, err := parseTTL(in); err == nil {
			t.Errorf("parseTTL(%q) accepted", in)
		}
	}
}

func TestTokenCreateTTLAndList(t *testing.T) {
	t.Parallel()
	env := testEnv(t, "local")
	for _, args := range [][]string{
		{"--name", "dflt"}, {"--name", "forever", "--ttl", "never"}, {"--name", "short", "--ttl", "7d"},
	} {
		if code, _, errs := do(t, env, "", append([]string{"token", "create"}, args...)...); code != 0 {
			t.Fatalf("create %v: %d %s", args, code, errs)
		}
	}
	if code, _, _ := do(t, env, "", "token", "create", "--name", "bad", "--ttl", "soon"); code == 0 {
		t.Fatal("bad --ttl accepted")
	}
	code, out, errs := do(t, env, "", "token", "list")
	if code != 0 {
		t.Fatalf("list: %d %s", code, errs)
	}
	rows := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n")[1:] {
		f := strings.Fields(l)
		rows[f[1]] = l
	}
	if !strings.Contains(out, "EXPIRES") || !strings.Contains(rows["forever"], "no expiry") ||
		strings.Contains(rows["dflt"], "no expiry") || strings.Contains(rows["short"], "no expiry") {
		t.Fatalf("list:\n%s", out)
	}
}
