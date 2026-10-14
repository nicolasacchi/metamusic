package main

import (
	"bytes"
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

// m3Env is testEnv with the fake backend (never a real one: the tests build their key files from canary
// placeholders and never reach a network).
func m3Env(t *testing.T, extra map[string]string) map[string]string {
	t.Helper()
	env := testEnv(t, "local")
	env["MM_BACKEND"] = "fake"
	env["MM_FAKE_BACKEND"] = "fast"
	for k, v := range extra {
		env[k] = v
	}
	return env
}

// startCmd runs `metamusic <args>` until it listens (or exits) and returns the address, the captured output
// and a stop func that cancels and waits for the exit code.
func startCmd(t *testing.T, env map[string]string, args ...string) (addr string, out, errb *syncBuf, stop func() int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	out, errb = &syncBuf{}, &syncBuf{}
	done := make(chan int, 1)
	go func() { done <- run(ctx, args, envFn(env), strings.NewReader(""), out, errb) }()
	stop = func() int {
		cancel()
		select {
		case c := <-done:
			done <- c
			return c
		case <-time.After(45 * time.Second):
			t.Error("process did not stop")
			return -1
		}
	}
	t.Cleanup(func() { stop() })
	for deadline := time.Now().Add(startDeadline); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		if _, rest, ok := strings.Cut(out.String(), "listening on http://"); ok {
			a, _, _ := strings.Cut(rest, " ")
			return a, out, errb, stop
		}
		select {
		case c := <-done:
			done <- c
			return "", out, errb, stop
		default:
		}
	}
	t.Fatalf("did not start: %s %s", out.String(), errb.String())
	return "", out, errb, stop
}

func leaseHeld(t *testing.T, env map[string]string) bool {
	t.Helper()
	return dbRow(t, env, `SELECT count(*) FROM runner_lease WHERE id = 1 AND expires_at > ?`, store.FormatTime(time.Now())) > 0
}

func waitFor(t *testing.T, what string, limit time.Duration, f func() bool) {
	t.Helper()
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestServe_RunnerStartsAndStops(t *testing.T) {
	t.Parallel()
	env := m3Env(t, nil)
	_, _, errb, stop := startCmd(t, env, "serve")
	waitFor(t, "the runner lease", 20*time.Second, func() bool { return leaseHeld(t, env) })
	if code := stop(); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if leaseHeld(t, env) {
		t.Error("lease still held after shutdown")
	}
}

func TestServe_RunnerFalse(t *testing.T) {
	t.Parallel()
	env := m3Env(t, nil)
	addr, _, errb, stop := startCmd(t, env, "serve", "--runner=false")
	time.Sleep(400 * time.Millisecond) // a runner would have taken the lease at start
	if leaseHeld(t, env) || dbRow(t, env, `SELECT count(*) FROM runner_lease`) != 0 {
		t.Error("--runner=false took the lease")
	}
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Errorf("healthz: %v", err)
	} else {
		resp.Body.Close()
	}
	if code := stop(); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}

func TestServe_RunnerDisabledByEnv(t *testing.T) {
	t.Parallel()
	env := m3Env(t, map[string]string{"MM_RUNNER": "false"})
	_, _, _, stop := startCmd(t, env, "serve")
	time.Sleep(400 * time.Millisecond) // a runner would have taken the lease at start
	if leaseHeld(t, env) {
		t.Error("MM_RUNNER=false took the lease")
	}
	stop()
}

func TestServe_BadFlagIsUsageError(t *testing.T) {
	t.Parallel()
	if code, _, _ := do(t, m3Env(t, nil), "", "serve", "--no-such-flag"); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if code, _, _ := do(t, m3Env(t, nil), "", "serve", "extra"); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

// canaryEnvLine builds a KEY=value dotenv line at runtime (never a literal; C85's gitleaks rule).
func canaryEnvLine(k, v string) string { return k + "=" + v + "\n" }

func writeRunPodEnv(t *testing.T, env map[string]string, lines ...string) {
	t.Helper()
	dir := filepath.Join(env["MM_DATA_DIR"], "secrets")
	if err := os.WriteFile(filepath.Join(dir, "runpod.env"), []byte(strings.Join(lines, "")), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestServe_KeysAbsentNoRunner(t *testing.T) {
	t.Parallel()
	env := m3Env(t, map[string]string{"MM_BACKEND": "runpod", "MM_PUBLIC_URL": "http://127.0.0.1:8080"})
	delete(env, "MM_FAKE_BACKEND")
	_, _, errb, stop := startCmd(t, env, "serve")
	time.Sleep(400 * time.Millisecond) // a runner would have taken the lease at start
	if leaseHeld(t, env) {
		t.Error("a runner started without runpod.env")
	}
	if n := dbRow(t, env, `SELECT count(*) FROM provider_state WHERE backend = 'runpod-serverless' AND armed = 0`); n != 1 {
		t.Errorf("disarmed provider_state rows = %d, want 1", n)
	}
	if code := stop(); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
}

func TestServe_RefusesAdminKeyInRunpodEnv(t *testing.T) {
	t.Parallel()
	env := m3Env(t, map[string]string{"MM_BACKEND": "runpod", "MM_PUBLIC_URL": "http://127.0.0.1:8080"})
	delete(env, "MM_FAKE_BACKEND")
	canary := strings.Repeat("c", 12) + "-canary-admin"
	writeRunPodEnv(t, env, canaryEnvLine("RUNPOD_READ_KEY", "r-canary"), canaryEnvLine("RUNPOD_WRITE_KEY", "w-canary"),
		canaryEnvLine("RUNPOD_API_KEY", canary))
	code, out, errs := do(t, env, "", "serve")
	if code == 0 {
		t.Fatalf("serve started with an all-access key: %s", out)
	}
	if !strings.Contains(errs, "all-access") || strings.Contains(errs, canary) || strings.Contains(out, canary) {
		t.Errorf("error = %q", errs)
	}
}

// TestServe_NeverLoadsAdminKey pins that serve does not open runpod-admin.env: a directory of that name would
// make any read fail.
func TestServe_NeverLoadsAdminKey(t *testing.T) {
	t.Parallel()
	env := m3Env(t, nil)
	if err := os.Mkdir(filepath.Join(env["MM_DATA_DIR"], "secrets", "runpod-admin.env"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, _, errb, stop := startCmd(t, env, "serve")
	waitFor(t, "the runner lease", 20*time.Second, func() bool { return leaseHeld(t, env) })
	if code := stop(); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if strings.Contains(errb.String(), "runpod-admin") {
		t.Errorf("serve touched the admin key file: %s", errb.String())
	}
}

func TestServe_SecondProcessNoClaims(t *testing.T) {
	t.Parallel()
	env := m3Env(t, nil)
	_, _, _, stop1 := startCmd(t, env, "serve")
	waitFor(t, "the first runner lease", 20*time.Second, func() bool { return leaseHeld(t, env) })
	var owner1 string
	read := func() string {
		st, err := store.Open(context.Background(), filepath.Join(env["MM_DATA_DIR"], "metamusic.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		var o string
		if err := st.R.QueryRow(`SELECT owner FROM runner_lease WHERE id = 1`).Scan(&o); err != nil {
			t.Fatal(err)
		}
		return o
	}
	owner1 = read()
	_, _, errb2, stop2 := startCmd(t, env, "serve")
	time.Sleep(500 * time.Millisecond)
	if got := read(); got != owner1 {
		t.Errorf("lease owner changed to %q while the first process runs", got)
	}
	if code := stop2(); code != 0 {
		t.Fatalf("second exit %d: %s", code, errb2.String())
	}
	if got := read(); got != owner1 || !leaseHeld(t, env) {
		t.Errorf("second process shutdown disturbed the first one's lease (owner %q)", got)
	}
	stop1()
}

func TestServe_BackfillsRecipeKeys(t *testing.T) {
	t.Parallel()
	env := m3Env(t, nil)
	// a pre-M3 yue2 recipe (no recipe_keys row, as the M1 importer left them) written straight into a
	// migrated database (stub recipes are skipped by lineage's own tests)
	st, err := store.Open(context.Background(), filepath.Join(env["MM_DATA_DIR"], "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	ins := `INSERT INTO recipes(id, engine, request_id, request_json, style, lyrics, seed, cot, hash, core_hash, created_by, created_at)
		VALUES (?, ?, ?, ?, 's', 'l', ?, 'full', ?, ?, 'import', '2026-01-01T00:00:00.000Z')`
	if _, err := st.W.Exec(ins, "bf-yue", "yue2", "bf-1", `{"id":"bf-1","seed":77,"style":"s","lyrics":"l","cot":"full"}`, 77, "h-yue", "c-yue"); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	if n := dbRow(t, env, `SELECT count(*) FROM recipe_keys`); n != 0 {
		t.Fatalf("recipe_keys before serve = %d", n)
	}
	_, _, _, stop := startCmd(t, env, "serve", "--runner=false")
	if n := dbRow(t, env, `SELECT count(*) FROM recipe_keys WHERE recipe_id = 'bf-yue'`); n != 1 {
		t.Errorf("yue2 recipe keys = %d, want 1", n)
	}
	stop()
}

type apiClient struct {
	t    *testing.T
	base string
	tok  string
	c    *http.Client
}

func (a apiClient) do(method, path string, body any, hdr ...string) (int, map[string]any) {
	a.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.base+path, rd)
	if err != nil {
		a.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+a.tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := a.c.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m
}

// devWithToken starts `dev --seed-synthetic` and returns a client holding a read+write+generate token plus
// a known-recipe mother track id.
func devWithToken(t *testing.T, extra map[string]string) (apiClient, string, map[string]string, func() int) {
	t.Helper()
	if testing.Short() {
		t.Skip("heavy: seeds and renders audio")
	}
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	dir := t.TempDir()
	env := map[string]string{"MM_ADDR": "127.0.0.1:0", "MM_AUTH": "proxy"}
	for k, v := range extra {
		env[k] = v
	}
	dbEnv := map[string]string{"MM_AUTH": "local", "MM_DATA_DIR": dir, "MM_SECRETS_DIR": filepath.Join(dir, "secrets")}
	addr, _, errb, stop := startCmd(t, env, "dev", "--data-dir", dir, "--seed-synthetic")
	_ = errb
	code, tokOut, errs := do(t, dbEnv, "", "token", "create", "--name", "gen", "--scopes", "read,write,generate")
	if code != 0 {
		t.Fatalf("token create: %d %s", code, errs)
	}
	st, err := store.Open(context.Background(), filepath.Join(dir, "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var mother string
	if err := st.R.QueryRow(`SELECT t.id FROM tracks t JOIN recipes r ON r.id = t.recipe_id
		WHERE r.engine = 'yue2' AND t.lossy = 0 ORDER BY t.id LIMIT 1`).Scan(&mother); err != nil {
		t.Fatalf("no mother track in the synthetic library: %v", err)
	}
	return apiClient{t, "http://" + addr, strings.TrimSpace(tokOut), &http.Client{Timeout: 70 * time.Second}}, mother, dbEnv, stop
}

func TestDev_FakeProviderEndToEnd(t *testing.T) {
	t.Parallel()
	c, mother, dbe, stop := devWithToken(t, nil)
	defer func() {
		if t.Failed() {
			s2, _ := store.Open(context.Background(), filepath.Join(dbe["MM_DATA_DIR"], "metamusic.db"))
			rows, _ := s2.R.Query(`SELECT event, coalesce(code,''), coalesce(detail_json,'') FROM job_events ORDER BY id`)
			for rows.Next() {
				var a, b, d string
				_ = rows.Scan(&a, &b, &d)
				t.Logf("event %s %s %s", a, b, d)
			}
			s2.Close()
		}
	}()
	st, est := c.do("POST", "/api/tracks/"+mother+"/variants/estimate", map[string]any{"mode": "new_seed"})
	if st != 200 {
		t.Fatalf("estimate: %d %v", st, est)
	}
	tok, _ := est["confirmation_token"].(string)
	st, created := c.do("POST", "/api/tracks/"+mother+"/variants", map[string]any{"confirmation_token": tok}, "Idempotency-Key", "dev-e2e-1")
	if st != 201 {
		t.Fatalf("create: %d %v", st, created)
	}
	batchID, _ := created["id"].(string)
	var child string
	var last map[string]any
	defer func() {
		if t.Failed() {
			t.Logf("last batch: %v", last)
			if jobs, _ := last["jobs"].([]any); len(jobs) > 0 {
				_, j := c.do("GET", "/api/jobs/"+jobs[0].(map[string]any)["id"].(string), nil)
				t.Logf("job: %v", j)
			}
		}
	}()
	waitFor(t, "the child take", 90*time.Second, func() bool {
		time.Sleep(450 * time.Millisecond) // the default rate limit is per second
		_, b := c.do("GET", "/api/batches/"+batchID, nil)
		last = b
		jobs, _ := b["jobs"].([]any)
		for _, j := range jobs {
			jm, _ := j.(map[string]any)
			if jm["state"] == "completed" {
				child, _ = jm["track_id"].(string)
			}
		}
		return child != ""
	})
	st, links := c.do("GET", "/api/tracks/"+child+"/media", nil)
	if st != 200 {
		t.Fatalf("media links: %d %v", st, links)
	}
	flac, _ := links["flac"].(map[string]any)
	link, _ := flac["url"].(string)
	if link == "" {
		t.Fatalf("no playable link on the child: %v", links)
	}
	if strings.HasPrefix(link, "/") {
		link = c.base + link
	}
	resp, err := c.c.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	n, _ := io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 || n == 0 {
		t.Fatalf("media: %d, %d bytes", resp.StatusCode, n)
	}
	if code := stop(); code != 0 {
		t.Errorf("dev exit %d", code)
	}
}

func TestServe_ShutdownReturnsLongPolls(t *testing.T) {
	t.Parallel()
	c, mother, _, stop := devWithToken(t, map[string]string{"MM_FAKE_BACKEND": "slow:60s"})
	st, est := c.do("POST", "/api/tracks/"+mother+"/variants/estimate", map[string]any{"mode": "new_seed"})
	if st != 200 {
		t.Fatalf("estimate: %d %v", st, est)
	}
	st, created := c.do("POST", "/api/tracks/"+mother+"/variants", map[string]any{"confirmation_token": est["confirmation_token"]}, "Idempotency-Key", "dev-wait-1")
	if st != 201 {
		t.Fatalf("create: %d %v", st, created)
	}
	_, b := c.do("GET", "/api/batches/"+created["id"].(string), nil)
	jobs, _ := b["jobs"].([]any)
	if len(jobs) == 0 {
		t.Fatalf("no jobs: %v", b)
	}
	jobID, _ := jobs[0].(map[string]any)["id"].(string)
	returned := make(chan time.Time, 1)
	go func() {
		_, _ = c.do("GET", "/api/jobs/"+jobID+"/wait?timeout_s=55", nil)
		returned <- time.Now()
	}()
	time.Sleep(1 * time.Second) // let the poll park
	begin := time.Now()
	go stop()
	select {
	case at := <-returned:
		if d := at.Sub(begin); d > time.Second {
			t.Errorf("wait_for_job returned %v after shutdown began", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wait_for_job did not return after shutdown")
	}
}

// startDeadline bounds how long a test waits for `dev` to listen. Seeding renders and analyses audio: ~15-20 s of CPU
// under -race on an idle host, but whole-repo runs execute every package at once, so on a busy host the same work
// stretches several-fold (90 s was exceeded in `make cover`). It bounds real work that scales with load; it is not
// a sleep tuned to make a test pass, and a genuine hang still fails.
const startDeadline = 5 * time.Minute
