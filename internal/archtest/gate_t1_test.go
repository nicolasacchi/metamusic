package archtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGate_T1Patterns: the public-repo gate refuses every file type T1 must never commit (soundfonts, MIDI, raw PCM, HRTF
// datasets: C215.4 and C225.7) and still allows the committed Q15 table and its NOTICE.
func TestGate_T1Patterns(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	gl := filepath.Join(root, "bin", "gitleaks")
	if _, err := os.Stat(gl); err != nil {
		p, err := exec.LookPath("gitleaks")
		if err != nil {
			t.Skip("gitleaks not found (make tools)")
		}
		gl = p
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
	gate := func() (string, error) {
		c := exec.Command("scripts/public-gate.sh", "--staged")
		c.Dir = repo
		c.Env = append(os.Environ(), "GITLEAKS="+gl, "PUBLIC_GATE_PATTERNS=Synthetic Muse 900")
		out, err := c.CombinedOutput()
		return string(out), err
	}
	plant := func(name string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, name)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, name), []byte("synthetic\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		run("add", "-f", name)
	}
	for _, name := range []string{"a.sf2", "A.SF3", "a.mid", "a.midi", "a.f32", "a.pcm24", "a.sofa", "a.hrir.zip", "a.hrirs", "dir/orchestra.sf2"} {
		plant(name)
		out, err := gate()
		if err == nil || !strings.Contains(out, "forbidden path: "+name) {
			t.Errorf("%s was not blocked: err=%v\n%s", name, err, out)
		}
		run("rm", "-q", "--cached", "-f", name)
	}
	plant("hrtf/table.q15")
	plant("hrtf/NOTICE")
	if out, err := gate(); err != nil {
		t.Errorf("table.q15 and NOTICE must be allowed: %v\n%s", err, out)
	}
}
