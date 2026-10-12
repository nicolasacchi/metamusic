// Package testtools is TEST-ONLY: it locates external media tools for tests.
// It must be imported only from _test.go files (see TestNoTestHelpersInBinary).
package testtools

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

func moduleRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	// <root>/internal/testtools/testtools.go
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// lookup finds <module root>/bin/<name> first, then PATH. Empty if absent.
func lookup(name string) string {
	p := filepath.Join(moduleRoot(), "bin", name)
	if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
		return p
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

// Tools returns the media tools; a tool that is not found keeps its bare name.
func Tools() mediatool.Tools {
	pick := func(n string) string {
		if p := lookup(n); p != "" {
			return p
		}
		return n
	}
	return mediatool.Tools{FFmpeg: pick("ffmpeg"), FLAC: pick("flac"), Metaflac: pick("metaflac")}
}

// Require skips the test under -short, and when a named tool is missing,
// unless MM_REQUIRE_MEDIA_TOOLS=1 (CI), in which case a missing tool fails it.
func Require(t testing.TB, names ...string) {
	t.Helper()
	if testing.Short() {
		t.Skip("media tools: skipped under -short")
	}
	for _, n := range names {
		if lookup(n) != "" {
			continue
		}
		if os.Getenv("MM_REQUIRE_MEDIA_TOOLS") == "1" {
			t.Fatalf("required media tool %q not found", n)
		}
		t.Skipf("media tool %q not found", n)
	}
}
