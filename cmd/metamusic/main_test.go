package main

import (
	"bytes"
	"context"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/httpserver"
	"github.com/nicolasacchi/metamusic/web"
)

var update = flag.Bool("update", false, "rewrite openapi.json golden")

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func envFn(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func do(t *testing.T, env map[string]string, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(context.Background(), args, envFn(env), strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func testEnv(t *testing.T, mode string) map[string]string {
	t.Helper()
	dir := t.TempDir()
	sec := filepath.Join(dir, "secrets")
	if err := os.MkdirAll(sec, 0o700); err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("ab", 32)
	if err := os.WriteFile(filepath.Join(sec, "secret_key"), []byte(key), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sec, "edge_secret"), []byte("edge"), 0o600); err != nil {
		t.Fatal(err)
	}
	return map[string]string{"MM_AUTH": mode, "MM_DATA_DIR": dir, "MM_ADDR": "127.0.0.1:0"}
}

func TestOpenAPIGolden(t *testing.T) {
	code, out, errs := do(t, nil, "", "openapi")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	golden := filepath.Join("..", "..", "openapi.json")
	if *update {
		if err := os.WriteFile(golden, []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if d := cmp.Diff(string(want), out); d != "" {
		t.Fatalf("openapi.json drift (run go test ./cmd/metamusic -update):\n%s", d)
	}
	if !strings.HasSuffix(out, "}\n") || !strings.HasPrefix(out, "{\n  \"") {
		t.Fatal("format: want 2-space indent and trailing newline")
	}
}

func TestVersionAndUsage(t *testing.T) {
	if code, out, _ := do(t, nil, "", "version"); code != 0 || !strings.HasPrefix(out, "metamusic dev") {
		t.Fatalf("%d %q", code, out)
	}
	if code, _, _ := do(t, nil, ""); code != 2 {
		t.Fatalf("no args exit %d", code)
	}
	if code, _, _ := do(t, nil, "", "bogus"); code != 2 {
		t.Fatalf("bogus exit %d", code)
	}
}

func TestServeRefusesWithoutAuth(t *testing.T) {
	for _, env := range []map[string]string{{}, {"MM_AUTH": "nonsense"}} {
		code, out, errs := do(t, env, "", "serve")
		if code != 1 || out != "" || !strings.Contains(errs, "MM_AUTH") {
			t.Fatalf("exit %d out=%q err=%q", code, out, errs)
		}
	}
}

func TestServeMissingSecretNamesFile(t *testing.T) {
	env := map[string]string{"MM_AUTH": "local", "MM_DATA_DIR": t.TempDir()}
	code, _, errs := do(t, env, "", "serve")
	if code != 1 || !strings.Contains(errs, "secret_key") {
		t.Fatalf("%d %q", code, errs)
	}
}

func TestServeAndHealthcheck(t *testing.T) {
	env := testEnv(t, "local")
	ctx, cancel := context.WithCancel(context.Background())
	var out, errb syncBuf
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"serve"}, envFn(env), strings.NewReader(""), &out, &errb) }()
	var addr string
	for i := 0; i < 200 && addr == ""; i++ {
		time.Sleep(25 * time.Millisecond)
		if _, rest, ok := strings.Cut(out.String(), "listening on http://"); ok {
			addr, _, _ = strings.Cut(rest, " ")
		}
	}
	if addr == "" {
		t.Fatalf("server did not start: %s %s", out.String(), errb.String())
	}
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("healthz: %v %v", resp, err)
	}
	resp.Body.Close()
	// /mcp is wired (not 404): anonymous callers get the OAuth challenge.
	resp, err = http.Post("http://"+addr+"/mcp", "application/json", strings.NewReader("{}"))
	if err != nil || resp.StatusCode != 401 || !strings.Contains(resp.Header.Get("WWW-Authenticate"), "resource_metadata=") {
		t.Fatalf("mcp anonymous: %v %v", resp, err)
	}
	resp.Body.Close()

	var o, e bytes.Buffer
	hc := map[string]string{"MM_ADDR": addr}
	if code := run(context.Background(), []string{"healthcheck"}, envFn(hc), nil, &o, &e); code != 0 {
		t.Fatalf("healthcheck %d %s", code, e.String())
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errb.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("shutdown timeout")
	}
	if code := run(context.Background(), []string{"healthcheck"}, envFn(hc), nil, &o, &e); code != 1 {
		t.Fatalf("healthcheck on stopped server = %d", code)
	}
}

func TestTokenCreateAuthenticates(t *testing.T) {
	env := testEnv(t, "local")
	if code, _, errs := do(t, env, "", "token", "create", "--scopes", "read"); code != 2 {
		t.Fatalf("missing --name: %d %s", code, errs)
	}
	if code, _, _ := do(t, env, "", "token", "create", "--name", "x", "--scopes", "root"); code != 1 {
		t.Fatalf("bad scope: %d", code)
	}
	code, out, errs := do(t, env, "", "token", "create", "--name", "cli-agent", "--scopes", "read,write")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	tok := strings.TrimSpace(out)
	if !strings.HasPrefix(tok, "mmp_") || len(tok) != 56 || strings.Contains(errs, tok) {
		t.Fatalf("token output %q / %q", out, errs)
	}

	b, err := boot(context.Background(), envFn(env), false)
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	h, err := httpserver.New(httpserver.Options{Cfg: b.cfg, Sec: b.sec, Store: b.st, Tokens: b.tokens,
		Sessions: b.local, SPA: web.Dist(), Version: "t"})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/me", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"cli-agent"`) {
		t.Fatalf("/api/me: %d %s", w.Code, w.Body)
	}

	// list shows it without the secret; revoke kills it.
	_, list, _ := do(t, env, "", "token", "list")
	if !strings.Contains(list, "cli-agent") || strings.Contains(list, tok) {
		t.Fatalf("list: %s", list)
	}
	id := strings.Fields(strings.Split(list, "\n")[1])[0]
	if code, _, errs := do(t, env, "", "token", "revoke", id); code != 0 {
		t.Fatalf("revoke: %s", errs)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("revoked token: %d", w.Code)
	}
	if code, _, _ := do(t, env, "", "token", "revoke", "nope"); code != 1 {
		t.Fatalf("revoke unknown exit %d", code)
	}
}

func TestSetPassword(t *testing.T) {
	env := testEnv(t, "local")
	if code, _, errs := do(t, env, "s3cret-pw\n", "user", "set-password", "--username", "bob"); code != 0 {
		t.Fatalf("exit %d %s", code, errs)
	}
	b, err := boot(context.Background(), envFn(env), false)
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	if _, err := b.local.Login(context.Background(), "bob", "s3cret-pw"); err != nil {
		t.Fatalf("login: %v", err)
	}
	if code, _, _ := do(t, env, "\n", "user", "set-password", "--username", "bob"); code != 1 {
		t.Fatalf("blank password accepted: %d", code)
	}
	proxy := testEnv(t, "proxy")
	if code, _, _ := do(t, proxy, "pw\n", "user", "set-password", "--username", "bob"); code != 1 {
		t.Fatalf("proxy mode accepted: %d", code)
	}
}

func TestDevBootstrap(t *testing.T) {
	dir := t.TempDir()
	// MM_AUTH, MM_DATA_DIR and MM_SECRETS_DIR from the environment are ignored by dev.
	env := map[string]string{"MM_DATA_DIR": t.TempDir(), "MM_SECRETS_DIR": t.TempDir(), "MM_ADDR": "127.0.0.1:0", "MM_AUTH": "proxy"}
	ctx, cancel := context.WithCancel(context.Background())
	var out, errb syncBuf
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"dev", "--data-dir", dir}, envFn(env), strings.NewReader(""), &out, &errb)
	}()
	for deadline := time.Now().Add(60 * time.Second); !strings.Contains(out.String(), "listening on") && time.Now().Before(deadline); {
		time.Sleep(25 * time.Millisecond)
	}
	cancel()
	<-done
	if !strings.Contains(out.String(), "dev login: username=dev password=") || !strings.Contains(out.String(), "listening on") ||
		!strings.Contains(out.String(), "auth=local") {
		t.Fatalf("out %q err %q", out.String(), errb.String())
	}
	if st, err := os.Stat(filepath.Join(dir, "secrets", "secret_key")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("secret_key: %v %v", st, err)
	}
}

func TestSetPasswordTooLong(t *testing.T) {
	env := testEnv(t, "local")
	long := strings.Repeat("a", 4097) + "\n"
	if code, _, errb := do(t, env, long, "user", "set-password", "--username", "bob"); code != 1 || !strings.Contains(errb, "exceeds") {
		t.Fatalf("code %d err %q", code, errb)
	}
}
