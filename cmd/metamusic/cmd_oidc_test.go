package main

import (
	"strings"
	"testing"
)

// TestCLI_OIDCStub: the oidc command and its flags exist (C114); `probe` answers "not implemented" (exit 1) until
// M4a-C3 replaces the stub, and usage errors exit 2 without touching the network.
func TestCLI_OIDCStub(t *testing.T) {
	t.Parallel()
	env := testEnv(t, "local")
	for _, tc := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{"no subcommand", []string{"oidc"}, 2, "expected probe"},
		{"unknown subcommand", []string{"oidc", "login"}, 2, "expected probe"},
		{"missing flags", []string{"oidc", "probe"}, 2, "--server"},
		{"missing client id", []string{"oidc", "probe", "--server", "https://mm.example.com"}, 2, "--client-id"},
		{"stray argument", []string{"oidc", "probe", "--server", "https://mm.example.com", "--client-id", "x", "extra"}, 2, "unexpected argument"},
		{"stub", []string{"oidc", "probe", "--server", "https://mm.example.com", "--client-id", "metamusic-probe",
			"--client-secret-file", "/nonexistent/secret", "--redirect", "http://127.0.0.1:8765/callback"}, 1, "not implemented"},
	} {
		code, out, errs := do(t, env, "", tc.args...)
		if code != tc.code || !strings.Contains(errs, tc.want) {
			t.Errorf("%s: exit %d, want %d; stdout %q stderr %q (want %q)", tc.name, code, tc.code, out, errs, tc.want)
		}
		if strings.Contains(out+errs, "/nonexistent/secret") && tc.code == 1 {
			t.Errorf("%s: the stub echoed the secret file path", tc.name)
		}
	}
	if _, out, _ := do(t, env, "", "help"); !strings.Contains(out, "oidc probe --server URL --client-id ID") {
		t.Errorf("usage does not list oidc probe:\n%s", out)
	}
}
