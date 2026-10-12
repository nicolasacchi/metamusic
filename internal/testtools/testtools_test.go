package testtools

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestTesttools_Require(t *testing.T) {
	if os.Getenv("MM_TESTTOOLS_CHILD") == "1" {
		Require(t, "definitely-not-a-tool-xyz")
		return
	}
	if testing.Short() {
		t.Skip("subprocess check skipped under -short")
	}
	run := func(env string, args ...string) (string, error) {
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestTesttools_Require$", "-test.v"}, args...)...)
		cmd.Env = append(os.Environ(), "MM_TESTTOOLS_CHILD=1", env)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	// missing tool, not required => skip (exit 0)
	out, err := run("MM_REQUIRE_MEDIA_TOOLS=0")
	if err != nil || !strings.Contains(out, "SKIP") {
		t.Fatalf("want skip: err=%v out=%s", err, out)
	}
	// missing tool, required => fail
	out, err = run("MM_REQUIRE_MEDIA_TOOLS=1")
	if err == nil || !strings.Contains(out, "FAIL") {
		t.Fatalf("want fail: err=%v out=%s", err, out)
	}
	// -short always skips, even when required
	out, err = run("MM_REQUIRE_MEDIA_TOOLS=1", "-test.short")
	if err != nil || !strings.Contains(out, "SKIP") {
		t.Fatalf("want short skip: err=%v out=%s", err, out)
	}
}

func TestTools_Defaults(t *testing.T) {
	tl := Tools()
	if tl.FFmpeg == "" || tl.FLAC == "" || tl.Metaflac == "" {
		t.Fatalf("%+v", tl)
	}
}
