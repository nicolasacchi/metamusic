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

	"github.com/nicolasacchi/metamusic/internal/testtools"
)

// ffmpegWrapper writes an ffmpeg stand-in around the real binary. body is a shell fragment run
// before the real ffmpeg is exec'd; it sees the original arguments.
func ffmpegWrapper(t *testing.T, body string) string {
	t.Helper()
	testtools.Require(t, "ffmpeg")
	p := filepath.Join(t.TempDir(), "ffmpeg")
	ffmpegBin := testtools.Tools().FFmpeg
	script := "#!/bin/sh\n" + strings.ReplaceAll(body, "@REAL@", ffmpegBin) + "\nexec \"" + ffmpegBin + "\" \"$@\"\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil { // #nosec G306 -- test executable
		t.Fatal(err)
	}
	return p
}

// hideShowspectrumpic filters the filter listing, so ffmpeg.New succeeds and RequireTierA fails.
const hideShowspectrumpic = `if [ "$2" = "-filters" ]; then "@REAL@" "$@" | grep -v showspectrumpic; exit 0; fi`

// failShowspectrumpic passes the capability check and fails every real spectrogram render.
const failShowspectrumpic = `case "$*" in *showspectrumpic*) echo boom >&2; exit 1;; esac`

func TestCLI_ImportRunsTierA(t *testing.T) {
	t.Parallel()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	t.Run("summary line and rows", func(t *testing.T) {
		t.Parallel()
		env, inbox := importEnv(t)
		e2eInbox(t, inbox)
		code, out, errs := do(t, env, "", "import", "--yes")
		if code != 0 {
			t.Fatalf("exit %d\n%s\n%s", code, out, errs)
		}
		if !strings.Contains(out, "Tier A: 9 tracks, 9 analysed, 0 failed") {
			t.Errorf("no Tier A summary:\n%s", out)
		}
		if n := dbRow(t, env, `SELECT count(*) FROM renditions WHERE kind='spectrogram'`); n != 9 {
			t.Errorf("spectrograms = %d", n)
		}
	})
	t.Run("--json carries the line", func(t *testing.T) {
		t.Parallel()
		env, inbox := importEnv(t)
		e2eInbox(t, inbox)
		code, out, errs := do(t, env, "", "import", "--yes", "--json")
		if code != 0 {
			t.Fatalf("exit %d\n%s\n%s", code, out, errs)
		}
		var got struct {
			TierA string `json:"tier_a"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil || !strings.HasPrefix(got.TierA, "Tier A: 9 tracks") {
			t.Errorf("json tier_a = %q (%v)", got.TierA, err)
		}
	})
	t.Run("a forced Tier A failure keeps exit 0", func(t *testing.T) {
		t.Parallel()
		env, inbox := importEnv(t)
		e2eInbox(t, inbox)
		env["MM_FFMPEG"] = ffmpegWrapper(t, failShowspectrumpic)
		code, out, errs := do(t, env, "", "import", "--yes")
		if code != 0 {
			t.Fatalf("exit %d\n%s\n%s", code, out, errs)
		}
		if !strings.Contains(out, "Tier A:") || !strings.Contains(out, "9 failed") {
			t.Errorf("failure not summarised:\n%s", out)
		}
		if n := dbRow(t, env, `SELECT count(*) FROM tracks`); n != 9 {
			t.Errorf("tracks = %d", n)
		}
	})
}

func TestCLI_SeedRunsTierA(t *testing.T) {
	t.Parallel()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	env := seedEnv(t)
	code, out, errs := do(t, env, "", "seed-synthetic", "--profile", "e2e")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errs)
	}
	if !strings.Contains(out, "Tier A: 9 tracks, 9 analysed, 0 failed") {
		t.Errorf("no Tier A summary:\n%s", out)
	}
	if n := dbRow(t, env, `SELECT count(DISTINCT track_id) FROM analyses WHERE tier='A'`); n != 9 {
		t.Errorf("tracks with Tier A rows = %d, want 9", n)
	}
}

func TestFFmpeg_TierAMissingDoesNotBreakImport(t *testing.T) {
	t.Parallel()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	const line = "Tier A: unavailable (ffmpeg lacks showspectrumpic)"
	wrapper := ffmpegWrapper(t, hideShowspectrumpic)

	t.Run("import", func(t *testing.T) {
		t.Parallel()
		env, inbox := importEnv(t)
		e2eInbox(t, inbox)
		env["MM_FFMPEG"] = wrapper
		code, out, errs := do(t, env, "", "import", "--yes")
		if code != 0 || !strings.Contains(out, line) {
			t.Fatalf("exit %d\n%s\n%s", code, out, errs)
		}
		if n := dbRow(t, env, `SELECT count(*) FROM analyses WHERE tier='A'`); n != 0 {
			t.Errorf("Tier A rows = %d", n)
		}
	})

	t.Run("seed-synthetic, serve and the read-only operations", func(t *testing.T) {
		t.Parallel()
		env := seedEnv(t)
		env["MM_FFMPEG"] = wrapper
		code, out, errs := do(t, env, "", "seed-synthetic", "--profile", "e2e")
		if code != 0 || !strings.Contains(out, line) {
			t.Fatalf("exit %d\n%s\n%s", code, out, errs)
		}
		code, tokOut, errs := do(t, env, "", "token", "create", "--name", "x", "--scopes", "read")
		if code != 0 {
			t.Fatalf("token: %d %s", code, errs)
		}
		tok := strings.TrimSpace(tokOut)
		addr, serr, stop := startServe(t, env)
		defer stop()
		if n := strings.Count(serr.String(), "Tier A unavailable (ffmpeg lacks showspectrumpic)"); n != 1 {
			t.Errorf("serve logged the missing capability %d times: %s", n, serr.String())
		}
		get := func(path string) (int, map[string]any) {
			req, _ := http.NewRequest("GET", "http://"+addr+path, nil)
			req.Header.Set("Authorization", "Bearer "+tok)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			return resp.StatusCode, m
		}
		_, list := get("/api/tracks?limit=1")
		id := list["items"].([]any)[0].(map[string]any)["id"].(string)
		if st, m := get("/api/tracks/" + id + "/analysis"); st != 200 || m["tier"] != "0" {
			t.Errorf("get_track_analysis = %d tier %v", st, m["tier"])
		}
		if st, m := get("/api/tracks/" + id + "/clip"); st != 200 || m["url"] == nil {
			t.Errorf("get_clip = %d %v", st, m)
		}
	})
}

// startServe runs `serve` until it listens and returns its address, its stderr and a stop func.
func startServe(t *testing.T, env map[string]string) (addr string, errb *syncBuf, stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var out syncBuf
	errb = &syncBuf{}
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"serve"}, envFn(env), strings.NewReader(""), &out, errb) }()
	for deadline := time.Now().Add(30 * time.Second); addr == "" && time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		if _, rest, ok := strings.Cut(out.String(), "listening on http://"); ok {
			addr, _, _ = strings.Cut(rest, " ")
		}
	}
	stop = func() {
		cancel()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Error("serve did not stop")
		}
	}
	if addr == "" {
		stop()
		t.Fatalf("serve did not start: %s", errb.String())
	}
	return addr, errb, stop
}

func TestServe_SweepsStaleWorkDirs(t *testing.T) {
	t.Parallel()
	testtools.Require(t, "ffmpeg")
	env := testEnv(t, "local")
	inc := filepath.Join(env["MM_DATA_DIR"], "incoming")
	old := time.Now().Add(-3 * time.Hour)
	mk := func(name string, at time.Time) string {
		p := filepath.Join(inc, name)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "master.flac"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
		return p
	}
	staleClip, staleEars := mk("clip-0001", old), mk("ears-0001", old)
	freshClip := mk("clip-0002", time.Now())
	otherOld := mk("ingest-0001", old) // M1's sweep owns ingest-*, and only after 24 h
	_, _, stop := startServe(t, env)
	stop()
	for _, p := range []string{staleClip, staleEars} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived the sweep (%v)", filepath.Base(p), err)
		}
	}
	for _, p := range []string{freshClip, otherOld} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed: %v", filepath.Base(p), err)
		}
	}
}
