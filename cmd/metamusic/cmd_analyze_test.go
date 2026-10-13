package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

func TestUsage_ListsAnalyze(t *testing.T) {
	t.Parallel()
	_, out, _ := do(t, nil, "", "help")
	if !strings.Contains(out, "analyze [--track ID]... [--force] [--dry-run] [--jobs N] [--json]") {
		t.Errorf("usage lacks the analyze line:\n%s", out)
	}
}

func TestCLI_AnalyzeUsage(t *testing.T) {
	t.Parallel()
	env := testEnv(t, "local")
	for _, args := range [][]string{{"--bogus"}, {"--jobs", "0"}, {"extra"}} {
		code, out, errs := do(t, env, "", append([]string{"analyze"}, args...)...)
		if code != 2 || out != "" || errs == "" {
			t.Errorf("analyze %v = %d %q %q, want a usage error (exit 2)", args, code, out, errs)
		}
	}
	// no database yet: fatal, nothing created
	code, _, errs := do(t, env, "", "analyze")
	if code != 2 || !strings.Contains(errs, "no database") {
		t.Errorf("no database: %d %q", code, errs)
	}
	if _, err := os.Stat(filepath.Join(env["MM_DATA_DIR"], "metamusic.db")); err == nil {
		t.Error("analyze created a database")
	}
}

func auditRows(t *testing.T, env map[string]string) int {
	t.Helper()
	return dbRow(t, env, `SELECT count(*) FROM audit_log WHERE op = 'cli:analyze' AND principal_id = 'system:cli' AND principal_kind = 'system' AND transport = 'cli' AND method = 'CLI'`)
}

func TestCLI_Analyze(t *testing.T) {
	t.Parallel()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	env := seedEnv(t)
	if code, out, errs := do(t, env, "", "seed-synthetic", "--profile", "e2e"); code != 0 {
		t.Fatalf("seed: %d\n%s\n%s", code, out, errs)
	}
	tierA := func() int { return dbRow(t, env, `SELECT count(*) FROM analyses WHERE tier = 'A'`) }
	renditions := func() int { return dbRow(t, env, `SELECT count(*) FROM renditions WHERE kind = 'spectrogram'`) }
	if tierA() != 0 || auditRows(t, env) != 0 {
		t.Fatalf("a seeded library starts without Tier A (%d rows) or analyze audit rows", tierA())
	}
	tracks := dbRow(t, env, `SELECT count(*) FROM tracks`)

	// --dry-run: reports, writes nothing (no analyses, no renditions, no audit row)
	code, out, errs := do(t, env, "", "analyze", "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("dry run: %d %s", code, errs)
	}
	var rep analyzeJSON
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("dry-run --json is not JSON: %v\n%s", err, out)
	}
	if !rep.DryRun || rep.Tracks != tracks || rep.Ran["silence"] != tracks || rep.Ran["spectrogram"] != tracks || rep.Failed != 0 {
		t.Errorf("dry run report = %+v", rep)
	}
	if tierA() != 0 || renditions() != 0 || auditRows(t, env) != 0 {
		t.Errorf("a dry run wrote: %d analyses, %d renditions, %d audit rows", tierA(), renditions(), auditRows(t, env))
	}
	code, out, _ = do(t, env, "", "analyze", "--dry-run")
	if code != 0 || !strings.Contains(out, "dry run") {
		t.Errorf("human dry run = %d %q", code, out)
	}

	// one track: exactly one audit row
	id := dbString(t, env, `SELECT id FROM tracks ORDER BY recorded_at, id LIMIT 1`)
	code, out, errs = do(t, env, "", "analyze", "--track", id, "--json")
	if code != 0 {
		t.Fatalf("analyze --track: %d %s", code, errs)
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil || rep.DryRun || rep.Tracks != 1 || rep.Analysed != 1 || rep.Failed != 0 {
		t.Fatalf("report = %+v (%v)\n%s", rep, err, out)
	}
	if got := dbRow(t, env, `SELECT count(*) FROM analyses WHERE tier = 'A' AND track_id = ?`, id); got < 2 || got != tierA() {
		t.Errorf("track analyses = %d of %d", got, tierA())
	}
	if renditions() != 1 || auditRows(t, env) != 1 {
		t.Errorf("renditions %d, audit rows %d, want 1 and 1", renditions(), auditRows(t, env))
	}

	// the rest, in parallel; the track already done is skipped
	code, out, errs = do(t, env, "", "analyze", "--jobs", "3")
	if code != 0 || !strings.Contains(out, "analysed") {
		t.Fatalf("analyze: %d %q %s", code, out, errs)
	}
	if renditions() != tracks || auditRows(t, env) != 2 {
		t.Errorf("renditions %d/%d, audit rows %d, want %d and 2", renditions(), tracks, auditRows(t, env), tracks)
	}
	// nothing left: exit 0 and no audit row (nothing was written)
	before := tierA()
	code, out, _ = do(t, env, "", "analyze")
	if code != 0 || !strings.Contains(out, "0 analysed") || tierA() != before || auditRows(t, env) != 2 {
		t.Errorf("no-op run: %d %q, analyses %d -> %d, audit rows %d", code, out, before, tierA(), auditRows(t, env))
	}
	// --force re-runs with the same tool version: the rows stay unique
	if code, _, errs = do(t, env, "", "analyze", "--force", "--track", id); code != 0 || tierA() != before {
		t.Errorf("force: %d %s, analyses %d -> %d", code, errs, before, tierA())
	}
	// an unknown track fails the run (exit 1) and names it
	code, out, _ = do(t, env, "", "analyze", "--track", "no-such-track")
	if code != 1 || !strings.Contains(out, "no-such-track") {
		t.Errorf("unknown track: %d %q", code, out)
	}
}

func dbString(t *testing.T, env map[string]string, q string) string {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(env["MM_DATA_DIR"], "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var s string
	if err := st.R.QueryRow(q).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCLI_AnalyzeNamesMissingCapability(t *testing.T) {
	t.Parallel()
	testtools.Require(t, "ffmpeg")
	env := testEnv(t, "local")
	st, err := store.Open(context.Background(), filepath.Join(env["MM_DATA_DIR"], "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	// an ffmpeg that hides showspectrumpic from its filter list
	wrapper := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\nif [ \"$2\" = \"-filters\" ]; then \"" + testtools.Tools().FFmpeg + "\" \"$@\" | grep -v showspectrumpic; else exec \"" + testtools.Tools().FFmpeg + "\" \"$@\"; fi\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	env["MM_FFMPEG"] = wrapper
	code, out, errs := do(t, env, "", "analyze")
	if code != 2 || out != "" || !strings.Contains(errs, "showspectrumpic") {
		t.Errorf("analyze = %d %q %q, want exit 2 naming showspectrumpic", code, out, errs)
	}
	if auditRows(t, env) != 0 {
		t.Error("a failed start wrote an audit row")
	}
}
