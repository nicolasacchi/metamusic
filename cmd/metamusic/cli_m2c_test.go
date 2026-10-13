package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/importer"
)

// cliMP3 is a minimal valid MPEG-1 layer III stream (three 417-byte frames); n varies the bytes.
func cliMP3(n int) string {
	frame := make([]byte, 417)
	copy(frame, []byte{0xFF, 0xFB, 0x90, 0x00})
	for i := 4; i < len(frame); i++ {
		frame[i] = byte(n + i)
	}
	return string(frame) + string(frame) + string(frame)
}

func readReport(t *testing.T, path string) importer.Report {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var r importer.Report
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCLI_ReferenceFlags(t *testing.T) {
	t.Parallel()
	t.Run("parse on dry-run and the report records them", func(t *testing.T) {
		t.Parallel()
		env, inbox := importEnv(t)
		put(t, inbox, "lossy/x.mp3", cliMP3(1))
		code, out, errs := do(t, env, "", "import", "--dry-run", "--reference-unmatched", "--reference-lossy", "--json")
		if code != 0 {
			t.Fatalf("exit %d\n%s\n%s", code, out, errs)
		}
		r := readReport(t, latestReport(t, env, "-dry-run.json"))
		if !r.Flags.ReferenceUnmatched || !r.Flags.ReferenceLossy || r.Summary.TracksLossy != 1 {
			t.Errorf("flags %+v summary %+v", r.Flags, r.Summary)
		}
		code, out, _ = do(t, env, "", "import", "--dry-run")
		if code != 0 || !strings.Contains(out, "--reference-lossy") {
			t.Errorf("plain dry-run: exit %d\n%s", code, out)
		}
		r = readReport(t, latestReport(t, env, "-dry-run.json"))
		if r.Flags.ReferenceUnmatched || r.Flags.ReferenceLossy {
			t.Errorf("plain flags = %+v", r.Flags)
		}
	})
	t.Run("plan with different flags is a usage error", func(t *testing.T) {
		t.Parallel()
		env, inbox := importEnv(t)
		put(t, inbox, "lossy/x.mp3", cliMP3(1))
		if code, out, errs := do(t, env, "", "import", "--dry-run", "--reference-lossy"); code != 0 {
			t.Fatalf("dry-run exit %d\n%s\n%s", code, out, errs)
		}
		report := latestReport(t, env, "-dry-run.json")
		for _, args := range [][]string{
			{"import", "--plan", report},
			{"import", "--plan", report, "--reference-lossy", "--reference-unmatched"},
			{"import", "--plan", report, "--reference-unmatched"},
		} {
			code, _, errs := do(t, env, "", args...)
			if code != 2 || !strings.Contains(errs, "import: --plan was reviewed with different reference flags") {
				t.Errorf("%v: exit %d: %s", args, code, errs)
			}
		}
		if _, err := os.Stat(env["MM_DATA_DIR"] + "/metamusic.db"); err == nil {
			t.Error("a refused plan created the database")
		}
	})
	t.Run("usage text lists them", func(t *testing.T) {
		t.Parallel()
		code, out, _ := do(t, nil, "", "help")
		if code != 0 || !strings.Contains(out, "--reference-unmatched") || !strings.Contains(out, "--reference-lossy") || !strings.Contains(out, "--references") {
			t.Errorf("usage lacks the flags:\n%s", out)
		}
	})
	t.Run("dry-run still cannot combine with plan", func(t *testing.T) {
		t.Parallel()
		env, _ := importEnv(t)
		if code, _, _ := do(t, env, "", "import", "--dry-run", "--yes", "--reference-lossy"); code != 2 {
			t.Errorf("exit %d", code)
		}
	})
}

func TestCLI_SeedReferencesFlag(t *testing.T) {
	t.Parallel()
	env := testEnv(t, "local")
	env["MM_E2E"] = "1"
	if code, _, errs := do(t, env, "", "seed-synthetic", "--references", "--profile", "acceptance"); code != 2 {
		t.Errorf("--references with profile acceptance: exit %d %s", code, errs)
	}
}
