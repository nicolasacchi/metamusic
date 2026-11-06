package ops

import (
	"context"
	"flag"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
)

var updateD5 = flag.Bool("update-d5", false, "rewrite testdata/d5_golden.txt (after a deliberate dsp_version bump only)")

const d5GoldenPath = "testdata/d5_golden.txt"

func d5Hashes(t *testing.T) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, kind := range Kinds() {
		out, _, err := Run(context.Background(), d5Job(t, kind))
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		got[kind] = d5Hash(t, out)
	}
	return got
}

// TestD5_Golden pins pcm_sha256 of every kind rendered through the dispatcher (operator plus safety), on a fixed synthetic job,
// bit for bit (linux/amd64, GOAMD64=v1). Update only with a dsp_version bump.
func TestD5_Golden(t *testing.T) {
	d4SkipHeavy(t)
	dsptest.RequireGolden(t)
	got := d5Hashes(t)
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if *updateD5 {
		var sb strings.Builder
		sb.WriteString("# pcm_sha256 of the 24-bit render of each kind through ops.Run (operator plus safety) on a fixed synthetic job, seed 7. linux/amd64, GOAMD64=v1.\n")
		for _, k := range keys {
			sb.WriteString(k + " " + got[k] + "\n")
		}
		if err := os.WriteFile(d5GoldenPath, []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(d5GoldenPath)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0][0] != '#' {
			want[f[0]] = f[1]
		}
	}
	if len(want) != len(got) {
		t.Errorf("golden has %d rows, the kinds are %d", len(want), len(got))
	}
	for _, k := range keys {
		if got[k] != want[k] {
			t.Errorf("%s: pcm_sha256 %s, golden %s", k, got[k], want[k])
		}
	}
}

// TestD5_GoldenFMAOff re-executes the golden with the run-time FMA paths off and requires the same bits.
func TestD5_GoldenFMAOff(t *testing.T) {
	d4SkipHeavy(t)
	dsptest.RequireGolden(t)
	dsptest.ReexecFMAOff(t, `^TestD5_Golden$`)
}
