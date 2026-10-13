package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// offlineEnv is testEnv without MM_AUTH (mcp-stdio never needs it) plus a
// freshly created token; it returns the env, the token and its id.
func offlineEnv(t *testing.T) (map[string]string, string, string) {
	t.Helper()
	env := testEnv(t, "local")
	code, out, errs := do(t, env, "", "token", "create", "--name", "stdio-agent", "--scopes", "read,write")
	if code != 0 {
		t.Fatalf("token create: %d %s", code, errs)
	}
	tok := strings.TrimSpace(out)
	id := strings.TrimSpace(strings.TrimPrefix(strings.SplitN(errs, " (", 2)[0], "token id: "))
	delete(env, "MM_AUTH")
	env["MM_TOKEN"] = tok
	return env, tok, id
}

// stdioSession runs mcpStdio over in-memory transports and returns the client.
func stdioSession(t *testing.T, env map[string]string) (*mcp.ClientSession, *syncBuf) {
	t.Helper()
	ct, st := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	var errb syncBuf
	done := make(chan error, 1)
	go func() { done <- mcpStdio(ctx, envFn(env), streams{strings.NewReader(""), &bytes.Buffer{}, &errb}, st) }()
	sess, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		cancel()
		t.Fatalf("connect: %v (%s)", err, errb.String())
	}
	t.Cleanup(func() {
		_ = sess.Close()
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("mcp-stdio did not stop")
		}
	})
	return sess, &errb
}

func callTool(t *testing.T, s *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

func resultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestMCPStdio_RequiresToken(t *testing.T) {
	env, tok, id := offlineEnv(t)
	noTok := map[string]string{}
	for k, v := range env {
		if k != "MM_TOKEN" {
			noTok[k] = v
		}
	}
	code, out, errs := do(t, noTok, "", "mcp-stdio")
	if code != 2 || !strings.Contains(errs, "MM_TOKEN is required") || out != "" {
		t.Fatalf("missing token: %d %q %q", code, out, errs)
	}
	if code, _, errs := do(t, withAuth(env), "", "token", "revoke", id); code != 0 {
		t.Fatalf("revoke: %d %s", code, errs)
	}
	code, out, errs = do(t, env, "", "mcp-stdio")
	if code != 2 || !strings.Contains(errs, "MM_TOKEN rejected") || strings.Contains(errs, tok) || strings.Contains(out, tok) {
		t.Fatalf("revoked token: %d %q %q", code, out, errs)
	}
}

func TestMCPStdio_AgentPrincipal(t *testing.T) {
	env, tok, _ := offlineEnv(t)
	s, errb := stdioSession(t, env)
	res := callTool(t, s, "whoami", nil)
	var who map[string]any
	if err := json.Unmarshal([]byte(resultText(res)), &who); err != nil || res.IsError {
		t.Fatalf("whoami: %v %s", err, resultText(res))
	}
	if who["kind"] != "agent" || who["name"] != "stdio-agent" {
		t.Fatalf("principal %v", who)
	}
	tools, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools.Tools {
		if strings.HasPrefix(tl.Name, "human_") {
			t.Errorf("human tool %q listed", tl.Name)
		}
	}
	if strings.Contains(errb.String(), tok) {
		t.Error("the token reached stderr")
	}
}

func TestMCPStdio_RevokedMidSession(t *testing.T) {
	env, tok, id := offlineEnv(t)
	s, _ := stdioSession(t, env)
	if res := callTool(t, s, "whoami", nil); res.IsError {
		t.Fatalf("first call: %s", resultText(res))
	}
	if code, _, errs := do(t, withAuth(env), "", "token", "revoke", id); code != 0 {
		t.Fatalf("revoke: %d %s", code, errs)
	}
	res := callTool(t, s, "whoami", nil)
	if !res.IsError || !strings.Contains(resultText(res), "UNAUTHENTICATED") || strings.Contains(resultText(res), tok) {
		t.Fatalf("after revoke: %+v", res)
	}
}

func TestCLI_OfflineCommandsWithoutMMAuth(t *testing.T) {
	env, _, _ := offlineEnv(t)
	if env["MM_AUTH"] != "" {
		t.Fatal("MM_AUTH must be unset for this test")
	}
	t.Run("mcp-stdio", func(t *testing.T) {
		s, _ := stdioSession(t, env)
		if res := callTool(t, s, "whoami", nil); res.IsError {
			t.Fatalf("whoami: %s", resultText(res))
		}
	})
	t.Run("restore", func(t *testing.T) {
		src := backupEnv(t)
		if code, _, errs := do(t, src, "", "backup"); code != 0 {
			t.Fatalf("backup: %d %s", code, errs)
		}
		snap := filepath.Join(src["MM_BACKUP_DIR"], snapshotsIn(t, src["MM_BACKUP_DIR"])[0])
		noAuth := map[string]string{} // MM_AUTH, MM_DATA_DIR and every secret unset
		if code, out, errs := do(t, noAuth, "", "restore", "--from", snap, "--into", filepath.Join(t.TempDir(), "r")); code != 0 {
			t.Fatalf("restore: %d %s %s", code, out, errs)
		}
	})
}

// TestCLI_MCPStdio_Smoke (heavy) drives the real binary over a command
// transport: tools/list equals the HTTP tools/list and whoami is an agent.
func TestCLI_MCPStdio_Smoke(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy: builds and runs the binary")
	}
	bin := os.Getenv("MM_E2E_BIN")
	if bin == "" {
		bin = filepath.Join(t.TempDir(), "metamusic")
		if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
			t.Fatalf("build: %v\n%s", err, out)
		}
	}
	env, tok, _ := offlineEnv(t)

	// HTTP side: an in-process server over the same data directory.
	httpEnv := map[string]string{}
	for k, v := range env {
		httpEnv[k] = v
	}
	httpEnv["MM_AUTH"] = "local"
	ctx, cancel := context.WithCancel(context.Background())
	var out, errb syncBuf
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"serve"}, envFn(httpEnv), strings.NewReader(""), &out, &errb) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
		}
	})
	var addr string
	for i := 0; i < 400 && addr == ""; i++ {
		time.Sleep(25 * time.Millisecond)
		if _, rest, ok := strings.Cut(out.String(), "listening on http://"); ok {
			addr, _, _ = strings.Cut(rest, " ")
		}
	}
	if addr == "" {
		t.Fatalf("server did not start: %s %s", out.String(), errb.String())
	}
	hs, err := mcp.NewClient(&mcp.Implementation{Name: "h", Version: "0"}, nil).Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: "http://" + addr + "/mcp", MaxRetries: -1,
		HTTPClient: &http.Client{Transport: bearerTransport(tok)},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer hs.Close()
	httpTools, err := hs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "mcp-stdio")
	cmd.Env = append(os.Environ(), "MM_TOKEN="+tok, "MM_DATA_DIR="+env["MM_DATA_DIR"], "MM_AUTH=")
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "s", Version: "0"}, nil).Connect(context.Background(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	stdioTools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if d := cmp.Diff(httpTools.Tools, stdioTools.Tools); d != "" {
		t.Errorf("tools/list differs (-http +stdio):\n%s", d)
	}
	res := callTool(t, cs, "whoami", nil)
	if res.IsError || !strings.Contains(resultText(res), `"kind":"agent"`) {
		t.Fatalf("whoami: %s", resultText(res))
	}
}

type bearerTransport string

func (b bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+string(b))
	return http.DefaultTransport.RoundTrip(req)
}

// withAuth adds MM_AUTH=local for the token commands, which still use Load.
func withAuth(env map[string]string) map[string]string {
	out := map[string]string{"MM_AUTH": "local"}
	for k, v := range env {
		out[k] = v
	}
	return out
}
