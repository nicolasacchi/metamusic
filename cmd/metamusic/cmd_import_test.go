package main

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/media/flacmeta/flactest"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/synth"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

func timeAgo() time.Time { return time.Now().Add(-time.Hour) }

func timeNowRFC() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

// importEnv is a CLI environment with an inbox directory.
func importEnv(t *testing.T) (env map[string]string, inbox string) {
	t.Helper()
	env = testEnv(t, "local")
	inbox = filepath.Join(t.TempDir(), "inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}
	env["MM_INBOX"] = inbox
	return env, inbox
}

func put(t *testing.T, inbox, rel, content string) {
	t.Helper()
	p := filepath.Join(inbox, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	old := timeAgo()
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
}

func dbRow(t *testing.T, env map[string]string, q string, args ...any) int {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(env["MM_DATA_DIR"], "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var n int
	if err := st.R.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func latestReport(t *testing.T, env map[string]string, suffix string) string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(env["MM_DATA_DIR"], "import", "reports", "*"+suffix))
	if len(m) == 0 {
		t.Fatalf("no report *%s", suffix)
	}
	return m[len(m)-1]
}

func TestCLI_ImportExitCodes(t *testing.T) {
	t.Parallel()
	t.Run("warnings only exit 0", func(t *testing.T) {
		env, inbox := importEnv(t)
		put(t, inbox, "r.jsonl", strings.NewReplacer("%s", "a").Replace(`{"cot": "full", "id": "cli-a", "lyrics": "[Verse 1]\nla\n[Chorus]\nsing", "seed": 1, "style": "pop"}`+"\n"))
		put(t, inbox, "orphan.mp3", "ID3 orphan")
		code, out, errs := do(t, env, "", "import", "--dry-run")
		if code != 0 {
			t.Fatalf("exit %d\n%s\n%s", code, out, errs)
		}
		if !strings.Contains(out, "orphan_mp3") && !strings.Contains(out, "warnings") {
			t.Errorf("summary lacks warnings:\n%s", out)
		}
		if !strings.Contains(out, "--plan") {
			t.Errorf("no next-command hint:\n%s", out)
		}
	})
	t.Run("invalid line exit 1", func(t *testing.T) {
		env, inbox := importEnv(t)
		put(t, inbox, "r.jsonl", "{not json\n")
		code, out, _ := do(t, env, "", "import", "--dry-run")
		if code != 1 || !strings.Contains(out, "not_object") {
			t.Fatalf("exit %d\n%s", code, out)
		}
	})
	t.Run("bad flag exit 2", func(t *testing.T) {
		env, _ := importEnv(t)
		if code, _, errs := do(t, env, "", "import", "--bogus"); code != 2 {
			t.Fatalf("exit %d %s", code, errs)
		}
		if code, _, _ := do(t, env, "", "import", "--dry-run", "--min-age", "soon"); code != 2 {
			t.Fatalf("bad duration exit %d", code)
		}
	})
	t.Run("missing inbox is fatal", func(t *testing.T) {
		env, _ := importEnv(t)
		env["MM_INBOX"] = filepath.Join(t.TempDir(), "nowhere")
		if code, _, _ := do(t, env, "", "import", "--dry-run"); code != 2 {
			t.Fatalf("exit %d", code)
		}
	})
	t.Run("apply needs plan or yes", func(t *testing.T) {
		env, inbox := importEnv(t)
		put(t, inbox, "r.jsonl", "{}\n")
		code, _, errs := do(t, env, "", "import")
		if code != 2 || !strings.Contains(errs, "--dry-run") {
			t.Fatalf("exit %d: %s", code, errs)
		}
		if _, err := os.Stat(filepath.Join(env["MM_DATA_DIR"], "metamusic.db")); err == nil {
			t.Error("a refused apply created the database")
		}
	})
	t.Run("plan changed exit 3", func(t *testing.T) {
		testtools.Require(t, "ffmpeg", "flac", "metaflac")
		env, inbox := importEnv(t)
		put(t, inbox, "r.jsonl", `{"cot": "full", "id": "cli-a", "lyrics": "[Verse 1]\nla\n[Chorus]\nsing", "seed": 1, "style": "pop"}`+"\n")
		if code, out, errs := do(t, env, "", "import", "--dry-run"); code != 0 {
			t.Fatalf("dry-run %d %s %s", code, out, errs)
		}
		report := latestReport(t, env, "-dry-run.json")
		put(t, inbox, "r2.jsonl", `{"cot": "full", "id": "cli-b", "lyrics": "[Verse 1]\nla\n[Chorus]\nsing", "seed": 2, "style": "pop"}`+"\n")
		code, out, errs := do(t, env, "", "import", "--plan", report)
		if code != 3 || !strings.Contains(errs+out, "PLAN_CHANGED") {
			t.Fatalf("exit %d\n%s\n%s", code, out, errs)
		}
		if n := dbRow(t, env, `SELECT count(*) FROM recipes`); n != 0 {
			t.Errorf("a refused apply wrote %d recipes", n)
		}
		// --plan --yes applies the current plan
		code, out, errs = do(t, env, "", "import", "--plan", report, "--yes")
		if code != 0 {
			t.Fatalf("--plan --yes: exit %d\n%s\n%s", code, out, errs)
		}
		if n := dbRow(t, env, `SELECT count(*) FROM recipes`); n != 2 {
			t.Errorf("recipes = %d", n)
		}
	})
	t.Run("import in progress exit 4", func(t *testing.T) {
		testtools.Require(t, "ffmpeg", "flac", "metaflac")
		env, inbox := importEnv(t)
		put(t, inbox, "r.jsonl", `{"cot": "full", "id": "cli-a", "lyrics": "[Verse 1]\nla\n[Chorus]\nsing", "seed": 1, "style": "pop"}`+"\n")
		st, err := store.Open(context.Background(), filepath.Join(env["MM_DATA_DIR"], "metamusic.db"))
		if err != nil {
			t.Fatal(err)
		}
		now := timeNowRFC()
		if _, err := st.W.Exec(`INSERT INTO import_runs(id, started_at, heartbeat_at, state, principal, inbox_label, plan_digest) VALUES ('01a00000-0000-7000-8000-000000000001', ?, ?, 'running', 'cli', 'inbox', 'x')`, now, now); err != nil {
			t.Fatal(err)
		}
		st.Close()
		code, out, errs := do(t, env, "", "import", "--yes")
		if code != 4 || !strings.Contains(out+errs, "IMPORT_IN_PROGRESS") {
			t.Fatalf("exit %d\n%s\n%s", code, out, errs)
		}
	})
	t.Run("failed ingest exit 1", func(t *testing.T) {
		testtools.Require(t, "ffmpeg", "flac", "metaflac")
		env, inbox := importEnv(t)
		put(t, inbox, "r.jsonl", `{"cot": "full", "id": "cli-a", "lyrics": "[Verse 1]\nla\n[Chorus]\nsing", "seed": 1, "style": "pop"}`+"\n")
		// a header-valid FLAC whose frames are garbage: `flac --test` fails
		p := filepath.Join(inbox, "cli_a.flac")
		flactest.WriteFile(t, p, flactest.Spec{SampleRate: 48000, Channels: 2, BitsPerSample: 24, TotalSamples: 48000, Vendor: "t", Frames: []byte("not audio")})
		old := timeAgo()
		_ = os.Chtimes(p, old, old)
		code, out, errs := do(t, env, "", "import", "--yes")
		if code != 1 || !strings.Contains(out, "INVALID_AUDIO") && !strings.Contains(out, "errors") {
			t.Fatalf("exit %d\n%s\n%s", code, out, errs)
		}
		if n := dbRow(t, env, `SELECT count(*) FROM tracks`); n != 0 {
			t.Errorf("tracks = %d", n)
		}
		if n := dbRow(t, env, `SELECT count(*) FROM recipe_drafts WHERE status='backlog'`); n != 1 {
			t.Errorf("the failed take must leave its recipe as a backlog draft, got %d", n)
		}
	})
}

func TestCLI_ImportJSONOutput(t *testing.T) {
	env, inbox := importEnv(t)
	put(t, inbox, "r.jsonl", `{"cot": "full", "id": "cli-a", "lyrics": "[Verse 1]\nla\n[Chorus]\nsing", "seed": 1, "style": "pop"}`+"\n")
	code, out, errs := do(t, env, "", "import", "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("exit %d %s", code, errs)
	}
	var v struct {
		ReportID   string         `json:"report_id"`
		DryRun     bool           `json:"dry_run"`
		PlanDigest string         `json:"plan_digest"`
		Report     string         `json:"report"`
		Summary    map[string]any `json:"summary"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, out)
	}
	if v.ReportID == "" || !v.DryRun || len(v.PlanDigest) != 64 || v.Summary["recipes_unique"] != float64(1) || !strings.HasPrefix(v.Report, "import/reports/") {
		t.Errorf("json = %+v", v)
	}
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // best-effort snapshot
		}
		rel, _ := filepath.Rel(root, p)
		st, _ := d.Info()
		out[rel] = st.ModTime().String() + st.Mode().String()
		return nil
	})
	return out
}

func TestCLI_DryRunWritesOnlyImportDir(t *testing.T) {
	env, inbox := importEnv(t)
	put(t, inbox, "r.jsonl", `{"cot": "full", "id": "cli-a", "lyrics": "[Verse 1]\nla\n[Chorus]\nsing", "seed": 1, "style": "pop"}`+"\n")
	put(t, inbox, "x.flac", "not a flac")
	before := snapshotTree(t, env["MM_DATA_DIR"])
	inBefore := snapshotTree(t, inbox)
	if code, out, errs := do(t, env, "", "import", "--dry-run"); code != 1 && code != 0 {
		t.Fatalf("exit %d %s %s", code, out, errs)
	}
	after := snapshotTree(t, env["MM_DATA_DIR"])
	for p := range after {
		if _, ok := before[p]; ok {
			continue
		}
		if p != "import" && !strings.HasPrefix(p, "import"+string(filepath.Separator)) {
			t.Errorf("dry-run created %s", p)
		}
	}
	for p, v := range before {
		if p != "." && after[p] != v {
			t.Errorf("dry-run changed %s", p)
		}
	}
	for _, bad := range []string{"metamusic.db", "metamusic.db-wal", "metamusic.db-shm", "incoming", "audio", "derived", "takes"} {
		if _, ok := after[bad]; ok {
			t.Errorf("dry-run created %s", bad)
		}
	}
	for p, v := range snapshotTree(t, inbox) {
		if inBefore[p] != v {
			t.Errorf("inbox entry %s changed", p)
		}
	}
	// --report and --mapping-template write extra copies, O_EXCL
	rep, tpl := filepath.Join(t.TempDir(), "rep.json"), filepath.Join(t.TempDir(), "todo.csv")
	if code, _, errs := do(t, env, "", "import", "--dry-run", "--report", rep, "--mapping-template", tpl); code != 1 && code != 0 {
		t.Fatalf("exit %d %s", code, errs)
	}
	if b, err := os.ReadFile(rep); err != nil || !strings.Contains(string(b), "mm.inventory/1") {
		t.Errorf("--report copy: %v", err)
	}
	if _, err := os.Stat(tpl); err != nil {
		t.Errorf("--mapping-template file: %v", err)
	}
	if code, _, _ := do(t, env, "", "import", "--dry-run", "--report", rep); code != 2 {
		t.Errorf("--report over an existing file exited %d, want 2", code)
	}
}

func TestCLI_OutputEscapesControlChars(t *testing.T) {
	env, inbox := importEnv(t)
	put(t, inbox, "r.jsonl", `{"cot": "full", "id": "cli-a", "lyrics": "[Verse 1]\nla\n[Chorus]\nsing", "seed": 1, "style": "pop"}`+"\n")
	name := "evil\x1b[31m\u202egnp.flac"
	p := filepath.Join(inbox, name)
	flactest.WriteFile(t, p, flactest.Spec{SampleRate: 48000, Channels: 2, BitsPerSample: 24, TotalSamples: 48000, Vendor: "t", Frames: []byte("x")})
	old := timeAgo()
	_ = os.Chtimes(p, old, old)
	code, out, errs := do(t, env, "", "import", "--dry-run")
	if code != 0 && code != 1 {
		t.Fatalf("exit %d %s", code, errs)
	}
	for _, s := range []string{out, errs} {
		if strings.ContainsRune(s, 0x1b) || strings.ContainsRune(s, 0x202e) {
			t.Errorf("raw control character in output:\n%q", s)
		}
	}
	if !strings.Contains(out, `\x1b`) || !strings.Contains(out, `\u202e`) {
		t.Errorf("escaped form missing:\n%s", out)
	}
}

var (
	archOnce sync.Once
	archDir  string
	archErr  error
)

// e2eArchive generates the e2e profile once per test binary and copies it to dst.
func e2eInbox(t *testing.T, dst string) {
	t.Helper()
	archOnce.Do(func() {
		archDir, archErr = os.MkdirTemp("", "mm-cli-arch-")
		if archErr == nil {
			_, archErr = synth.Archive(context.Background(), testtools.Tools(), archDir, synth.ProfileE2E, 1)
		}
	})
	if archErr != nil {
		t.Fatal(archErr)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cp", "-a", archDir+"/.", dst).CombinedOutput(); err != nil {
		t.Fatalf("cp: %v %s", err, out)
	}
}

func TestMain(m *testing.M) {
	code := m.Run()
	if archDir != "" {
		_ = os.RemoveAll(archDir)
	}
	os.Exit(code)
}

func TestCLI_ImportSpreadLine(t *testing.T) {
	t.Parallel()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	env, inbox := importEnv(t)
	e2eInbox(t, inbox)
	code, out, errs := do(t, env, "", "import", "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errs)
	}
	if !strings.Contains(out, "loudness spread") || !strings.Contains(out, "LU") {
		t.Errorf("no spread line:\n%s", out)
	}
	if n := dbRow(t, env, `SELECT count(*) FROM tracks`); n != 6 {
		t.Errorf("tracks = %d", n)
	}
}

// Reference values must not reach stdout, stderr or the report files (C16.9).
func TestCLI_ImportPrivacy(t *testing.T) {
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	env, inbox := importEnv(t)
	const s1, s2, s3 = "zqxS1clisentinel", "zqxS2clisentinel", "zqxS3clisentinel"
	put(t, inbox, "r.jsonl", `{"cot": "full", "id": "cli-a", "lyrics": "[Verse 1]\nla\n[Chorus]\nsing", "seed": 1, "style": "pop"}`+"\n")
	put(t, inbox, "index.csv", "variant_id,track_n,source_title,source_artist,variant,variant_type,seed\ncli-a,"+s1+","+s1+","+s1+",v1,t,1\n")
	put(t, inbox, "pilot.json", `{"name": "c", "rule": "`+s2+`", "songs": [{"title": "`+s2+`", "artist": "`+s2+`", "why": "`+s2+`", "variant_ids": ["cli-a"]}]}`)
	put(t, inbox, "list.json", `{"playlist": "p", "owner": "`+s3+`", "url": "https://mm.example.com/`+s3+`", "tracks": [{"title": "`+s3+`", "artist": "`+s3+`"}]}`)
	var all strings.Builder
	for _, args := range [][]string{{"import", "--dry-run"}, {"import", "--dry-run", "--json"}, {"import", "--yes"}} {
		code, out, errs := do(t, env, "", args...)
		if code != 0 {
			t.Fatalf("%v: exit %d\n%s\n%s", args, code, out, errs)
		}
		all.WriteString(out + errs)
	}
	_ = filepath.WalkDir(filepath.Join(env["MM_DATA_DIR"], "import"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			all.Write(b)
		}
		return nil
	})
	for _, s := range []string{s1, s2, s3} {
		if strings.Contains(all.String(), s) {
			t.Errorf("CLI output or import files contain %s", s)
		}
	}
}
