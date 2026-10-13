package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

func seedEnv(t *testing.T) map[string]string {
	t.Helper()
	env := testEnv(t, "local")
	env["MM_E2E"] = "1"
	return env
}

func TestSeedSynthetic_E2EProfile(t *testing.T) {
	t.Parallel()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	env := seedEnv(t)
	code, out, errs := do(t, env, "", "seed-synthetic", "--profile", "e2e")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errs)
	}
	counts := func() [5]int {
		return [5]int{
			dbRow(t, env, `SELECT count(*) FROM recipes`), dbRow(t, env, `SELECT count(*) FROM tracks`),
			dbRow(t, env, `SELECT count(*) FROM recipe_drafts`), dbRow(t, env, `SELECT count(*) FROM recipe_drafts WHERE status='backlog'`),
			dbRow(t, env, `SELECT count(*) FROM import_runs`),
		}
	}
	got := counts()
	if got != [5]int{22, 9, 22, 13, 1} {
		t.Fatalf("recipes/tracks/drafts/backlog/runs = %v", got)
	}
	// Tier A ran after the import: every track has its analyses and a spectrogram
	for _, an := range []string{"silence", "clipping"} {
		if n := dbRow(t, env, `SELECT count(*) FROM analyses WHERE analyzer=? AND tier='A'`, an); n != 9 {
			t.Errorf("%s analyses = %d, want 9", an, n)
		}
	}
	if n := dbRow(t, env, `SELECT count(*) FROM renditions WHERE kind='spectrogram'`); n != 9 {
		t.Errorf("spectrograms = %d, want 9", n)
	}
	if !strings.Contains(out, "Tier A: 9 tracks, 9 analysed, 0 failed") {
		t.Errorf("no Tier A summary line:\n%s", out)
	}
	if n := dbRow(t, env, `SELECT count(*) FROM import_runs WHERE principal='seed-synthetic' AND inbox_label='synthetic'`); n != 1 {
		t.Errorf("seed run principal/label wrong")
	}
	if _, err := os.Stat(filepath.Join(env["MM_DATA_DIR"], "inbox-synthetic", "music", "songs.jsonl")); err != nil {
		t.Errorf("synthetic inbox missing: %v", err)
	}
	// a second invocation is a no-op
	code, _, errs = do(t, env, "", "seed-synthetic", "--profile", "e2e")
	if code != 0 {
		t.Fatalf("second run exit %d %s", code, errs)
	}
	if again := counts(); again != got {
		t.Errorf("second run changed the library: %v -> %v", got, again)
	}
}

func TestSeedSynthetic_Guard(t *testing.T) {
	t.Parallel()
	t.Run("plain data dir is refused", func(t *testing.T) {
		env := testEnv(t, "local")
		code, _, errs := do(t, env, "", "seed-synthetic", "--profile", "e2e")
		if code != 2 || !strings.Contains(errs, "--force") {
			t.Fatalf("exit %d: %s", code, errs)
		}
		if _, err := os.Stat(filepath.Join(env["MM_DATA_DIR"], "inbox-synthetic")); err == nil {
			t.Error("a refused seed generated an archive")
		}
	})
	t.Run("a real import run refuses even with --force", func(t *testing.T) {
		env := seedEnv(t)
		st, err := store.Open(context.Background(), filepath.Join(env["MM_DATA_DIR"], "metamusic.db"))
		if err != nil {
			t.Fatal(err)
		}
		now := timeNowRFC()
		if _, err := st.W.Exec(`INSERT INTO import_runs(id, started_at, heartbeat_at, finished_at, state, principal, inbox_label, plan_digest)
			VALUES ('01a00000-0000-7000-8000-000000000002', ?, ?, ?, 'completed', 'cli', 'inbox', 'x')`, now, now, now); err != nil {
			t.Fatal(err)
		}
		st.Close()
		code, _, errs := do(t, env, "", "seed-synthetic", "--profile", "e2e", "--force")
		if code != 2 || !strings.Contains(errs, "import") {
			t.Fatalf("exit %d: %s", code, errs)
		}
		delete(env, "MM_E2E")
		if code, _, _ := do(t, env, "", "seed-synthetic", "--profile", "e2e", "--force"); code != 2 {
			t.Fatalf("without MM_E2E exit %d", code)
		}
	})
	t.Run("bad profile is a usage error", func(t *testing.T) {
		env := seedEnv(t)
		if code, _, _ := do(t, env, "", "seed-synthetic", "--profile", "acceptance"); code != 2 {
			t.Fatalf("exit %d", code)
		}
	})
	t.Run("--force seeds a plain data dir", func(t *testing.T) {
		testtools.Require(t, "ffmpeg", "flac", "metaflac")
		env := testEnv(t, "local")
		if code, out, errs := do(t, env, "", "seed-synthetic", "--profile", "dev", "--force"); code != 0 {
			t.Fatalf("exit %d\n%s\n%s", code, out, errs)
		}
		if n := dbRow(t, env, `SELECT count(*) FROM tracks`); n != 9 {
			t.Errorf("tracks = %d", n)
		}
	})
}

// startDev runs `dev` until it listens, then returns its address and a stop func.
func startDev(t *testing.T, env map[string]string, args ...string) (addr string, out *syncBuf, stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	out = &syncBuf{}
	var errb syncBuf
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, append([]string{"dev"}, args...), envFn(env), strings.NewReader(""), out, &errb)
	}()
	for deadline := time.Now().Add(90 * time.Second); addr == "" && time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		if _, rest, ok := strings.Cut(out.String(), "listening on http://"); ok {
			addr, _, _ = strings.Cut(rest, " ")
		}
	}
	stop = func() {
		cancel()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Error("dev did not stop")
		}
	}
	if addr == "" {
		stop()
		t.Fatalf("dev did not start: %s %s", out.String(), errb.String())
	}
	return addr, out, stop
}

func TestDev_SeedsOnlyWhenEmpty(t *testing.T) {
	t.Parallel()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	dir := t.TempDir()
	env := map[string]string{"MM_ADDR": "127.0.0.1:0", "MM_AUTH": "proxy"}
	dbEnv := map[string]string{"MM_AUTH": "local", "MM_DATA_DIR": dir}
	// without the flag the library stays empty
	_, _, stop := startDev(t, env, "--data-dir", dir)
	stop()
	if n := dbRow(t, dbEnv, `SELECT count(*) FROM tracks`); n != 0 {
		t.Fatalf("dev without --seed-synthetic has %d tracks", n)
	}
	// with the flag an empty library is seeded
	addr, _, stop := startDev(t, env, "--data-dir", dir, "--seed-synthetic")
	if n := dbRow(t, dbEnv, `SELECT count(*) FROM tracks`); n != 9 {
		t.Errorf("tracks = %d", n)
	}
	// the probe is wired: serve() ensured the probe file and the media handler serves it
	if _, err := os.Stat(filepath.Join(dir, "derived", "probe", "probe-v1.flac")); err != nil {
		t.Errorf("probe file: %v", err)
	}
	if resp, err := http.Get("http://" + addr + "/healthz"); err != nil || resp.StatusCode != 200 {
		t.Errorf("healthz: %v", err)
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	stop()
	runs := dbRow(t, dbEnv, `SELECT count(*) FROM import_runs`)
	// a second start with the flag leaves a non-empty library alone
	_, out, stop := startDev(t, env, "--data-dir", dir, "--seed-synthetic")
	stop()
	if n := dbRow(t, dbEnv, `SELECT count(*) FROM tracks`); n != 9 {
		t.Errorf("second start: tracks = %d", n)
	}
	if n := dbRow(t, dbEnv, `SELECT count(*) FROM import_runs`); n != runs {
		t.Errorf("second start added import runs: %d -> %d (%s)", runs, n, out.String())
	}
}

func TestDev_SeedWithoutToolsContinues(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := map[string]string{"MM_ADDR": "127.0.0.1:0", "MM_AUTH": "proxy", "MM_FFMPEG": "/nonexistent/ffmpeg"}
	_, _, stop := startDev(t, env, "--data-dir", dir, "--seed-synthetic")
	stop()
	if n := dbRow(t, map[string]string{"MM_DATA_DIR": dir}, `SELECT count(*) FROM tracks`); n != 0 {
		t.Errorf("tracks = %d", n)
	}
}

func TestServe_ProbeWired(t *testing.T) {
	t.Parallel()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	env := testEnv(t, "local")
	code, tokOut, errs := do(t, env, "", "token", "create", "--name", "x", "--scopes", "read")
	if code != 0 {
		t.Fatalf("token: %d %s", code, errs)
	}
	tok := strings.TrimSpace(tokOut)
	ctx, cancel := context.WithCancel(context.Background())
	var out, errb syncBuf
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"serve"}, envFn(env), strings.NewReader(""), &out, &errb)
	}()
	defer func() { cancel(); <-done }()
	var addr string
	for i := 0; i < 400 && addr == ""; i++ {
		time.Sleep(25 * time.Millisecond)
		if _, rest, ok := strings.Cut(out.String(), "listening on http://"); ok {
			addr, _, _ = strings.Cut(rest, " ")
		}
	}
	if addr == "" {
		t.Fatalf("no server: %s", errb.String())
	}
	req, _ := http.NewRequest("GET", "http://"+addr+"/api/device-probe", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "/media/") {
		t.Fatalf("device-probe: %d %s", resp.StatusCode, b)
	}
	var link struct {
		URL string `json:"url"`
	}
	if err := jsonUnmarshal(b, &link); err != nil {
		t.Fatal(err)
	}
	r2, err := http.Get("http://" + addr + link.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode != 200 || len(body) < 4 || string(body[:4]) != "fLaC" || r2.Header.Get("Content-Type") != "audio/flac" {
		t.Fatalf("probe media: %d %q %s", r2.StatusCode, r2.Header.Get("Content-Type"), body[:min(len(body), 8)])
	}
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
