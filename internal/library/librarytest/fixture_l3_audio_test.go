package librarytest

import (
	"os"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/testtools"
)

func TestFixtureL3Audio_Shape(t *testing.T) {
	testtools.Require(t, "ffmpeg")
	a := FixtureL3Audio(t, testtools.Tools())
	if len(a.Sources) != 48+4 {
		t.Fatalf("%d sources", len(a.Sources))
	}
	keys := map[string]bool{}
	for _, s := range a.Sources {
		st, err := os.Stat(a.Abs(s))
		if err != nil || st.Size() < 1000 || st.Mode().Perm() != 0o444 || !strings.HasSuffix(s.Rel, ".flac") {
			t.Fatalf("%s: %v", s.Name, err)
		}
		if s.Kind == "key" {
			keys[s.Name] = true
		}
	}
	if len(keys) != 48 || len(L3KeyNames()) != 24 {
		t.Errorf("%d key sources, %d key names", len(keys), len(L3KeyNames()))
	}
	// no WAV intermediates are left behind
	if m, _ := os.ReadDir(a.Dir + "/masters/l3"); len(m) != 52 {
		t.Errorf("%d files in the fixture directory", len(m))
	}
}
