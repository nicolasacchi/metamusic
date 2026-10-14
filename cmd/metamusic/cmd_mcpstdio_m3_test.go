package main

import (
	"testing"
	"time"
)

// TestMCPStdio_NeverRunsRunner pins that mcp-stdio builds no runner: even with a backend selected and the
// runner enabled in the environment, it takes no lease, and tools that nudge the runner (Wake) work.
func TestMCPStdio_NeverRunsRunner(t *testing.T) {
	t.Parallel()
	env := m3Env(t, nil)
	env["MM_RUNNER"] = "true"
	code, tokOut, errs := do(t, env, "", "token", "create", "--name", "a", "--scopes", "read,write,generate")
	if code != 0 {
		t.Fatalf("token: %d %s", code, errs)
	}
	env["MM_TOKEN"] = trimNL(tokOut)
	sess, _ := stdioSession(t, env)
	res := callTool(t, sess, "whoami", nil)
	if res.IsError {
		t.Fatalf("whoami: %s", resultText(res))
	}
	time.Sleep(400 * time.Millisecond) // a runner would have taken the lease at start
	if leaseHeld(t, env) {
		t.Error("mcp-stdio took the runner lease")
	}
}

func trimNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
