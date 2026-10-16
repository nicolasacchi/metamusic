package archtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const privateMarker = "<!-- PRIVATE APPENDIX -->"

// publicCopy runs scripts/l1-public-copy.sh on a synthetic draft and returns the copy, its output and the exit error.
func publicCopy(t *testing.T, draft string) (copied, output string, err error) {
	t.Helper()
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "draft.md"), filepath.Join(dir, "copy.md")
	if err := os.WriteFile(src, []byte(draft), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(filepath.Join(repoRoot(t), "scripts", "l1-public-copy.sh"), src, dst).CombinedOutput()
	b, _ := os.ReadFile(dst)
	return string(b), string(out), err
}

const syntheticAppendix = "\n## Appendix P (PRIVATE)\n### P.4 Gate patterns\nSynthetic Muse 900 by Synthetic Artist 900\n"

func TestPublicCopy_StopsAtMarker(t *testing.T) {
	t.Parallel()
	draft := "# Plan\n\npublic text\n" + privateMarker + "\n" + syntheticAppendix
	got, out, err := publicCopy(t, draft)
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	if got != "# Plan\n\npublic text\n" {
		t.Fatalf("copy = %q", got)
	}
	for _, bad := range []string{privateMarker, "## Appendix P", "Synthetic Muse 900", "Synthetic Artist 900"} {
		if strings.Contains(got, bad) {
			t.Errorf("copy contains %q", bad)
		}
	}
}

func TestPublicCopy_RefusesBadMarkers(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"missing": "# Plan\n" + syntheticAppendix,
		"twice":   "a\n" + privateMarker + "\nb\n" + privateMarker + "\n" + syntheticAppendix,
	}
	for name, draft := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, _, err := publicCopy(t, draft)
			if err == nil {
				t.Fatalf("script accepted the draft; copy = %q", got)
			}
		})
	}
}

// loadPrivatePatterns returns the operator's private gate patterns (P.4), or nil when none are configured here.
func loadPrivatePatterns(t *testing.T) []string {
	t.Helper()
	root := repoRoot(t)
	paths := []string{filepath.Join(root, ".public-gate-patterns")}
	if out, err := exec.Command("git", "-C", root, "rev-parse", "--path-format=absolute", "--git-common-dir").Output(); err == nil {
		paths = append(paths, filepath.Join(strings.TrimSpace(string(out)), "..", ".public-gate-patterns"))
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var pats []string
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
				pats = append(pats, l)
			}
		}
		return pats
	}
	return nil
}

// TestPublicCopy_CommittedCopyIsClean checks the tracked copy of the plan: no marker line, no appendix, no private pattern.
// The real P.4 patterns are private and absent on CI; the pattern check runs only where .public-gate-patterns exists
// (the build host), and `make gate` (pre-commit, pre-push, CI) enforces the same patterns on every commit.
func TestPublicCopy_CommittedCopyIsClean(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "plans", "2026-10-15-l1-library-provenance.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, l := range strings.Split(text, "\n") {
		if l == privateMarker {
			t.Fatal("the committed copy holds the private-appendix marker line")
		}
		if strings.HasPrefix(l, "## Appendix P") {
			t.Fatal("the committed copy holds the Appendix P heading")
		}
	}
	low := strings.ToLower(text)
	for _, p := range loadPrivatePatterns(t) {
		if strings.Contains(low, strings.ToLower(p)) {
			t.Fatal("the committed copy matches a private gate pattern")
		}
	}
}

// TestPublicCopy_GateFailsOnLeak proves the gate rejects a staged copy holding a private pattern (synthetic pattern).
func TestPublicCopy_GateFailsOnLeak(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	gl := filepath.Join(root, "bin", "gitleaks")
	if _, err := os.Stat(gl); err != nil {
		if p, err := exec.LookPath("gitleaks"); err == nil {
			gl = p
		} else {
			t.Skip("gitleaks not found (make tools)")
		}
	}
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", ".")
	run("config", "user.email", "t@example.invalid")
	run("config", "user.name", "t")
	run("config", "commit.gpgsign", "false")
	for _, f := range []string{"scripts/public-gate.sh", "scripts/metrics-row.jq", ".gitleaks.toml"} {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, f)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, f), b, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	gate := func(extra string) error {
		c := exec.Command("scripts/public-gate.sh", "--staged")
		c.Dir = repo
		c.Env = append(os.Environ(), "GITLEAKS="+gl, "PUBLIC_GATE_PATTERNS=Synthetic Muse 900")
		out, err := c.CombinedOutput()
		if err != nil && extra == "" {
			t.Logf("gate output: %s", out)
		}
		return err
	}
	if err := os.WriteFile(filepath.Join(repo, "plan.md"), []byte("public text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "plan.md")
	if err := gate(""); err != nil {
		t.Fatalf("gate rejected a clean copy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "plan.md"), []byte("public text\nSynthetic Muse 900\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "plan.md")
	if err := gate("leak"); err == nil {
		t.Fatal("gate accepted a copy holding a private pattern")
	}
}
