package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/connectors"
	"github.com/nicolasacchi/metamusic/internal/oidc/oidctest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestCLI_OIDCUsage: the oidc command and its flags exist (C114); usage errors exit 2 without touching the network.
func TestCLI_OIDCUsage(t *testing.T) {
	t.Parallel()
	env := testEnv(t, "local")
	for _, tc := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{"no subcommand", []string{"oidc"}, 2, "expected probe"},
		{"unknown subcommand", []string{"oidc", "login"}, 2, "expected probe"},
		{"missing flags", []string{"oidc", "probe"}, 2, "--server"},
		{"missing client id", []string{"oidc", "probe", "--server", "https://mm.example.com"}, 2, "--client-id"},
		{"stray argument", []string{"oidc", "probe", "--server", "https://mm.example.com", "--client-id", "x", "extra"}, 2, "unexpected argument"},
		{"unreadable secret", []string{"oidc", "probe", "--server", "https://mm.example.com", "--client-id", "x", "--client-secret-file", "/nonexistent/secret"}, 1, "cannot read --client-secret-file"},
	} {
		code, out, errs := do(t, env, "", tc.args...)
		if code != tc.code || !strings.Contains(errs, tc.want) {
			t.Errorf("%s: exit %d, want %d; stdout %q stderr %q (want %q)", tc.name, code, tc.code, out, errs, tc.want)
		}
		if strings.Contains(out+errs, "/nonexistent/secret") {
			t.Errorf("%s: echoed the secret file path", tc.name)
		}
	}
	if _, out, _ := do(t, env, "", "help"); !strings.Contains(out, "oidc probe --server URL --client-id ID") {
		t.Errorf("usage does not list oidc probe:\n%s", out)
	}
	if _, _, errs := do(t, env, "", "oidc", "probe", "--help"); !strings.Contains(errs, "paste") {
		t.Errorf("--help does not document the paste flow:\n%s", errs)
	}
}

// urlWatcher is a stdout that reports the first authorization URL the probe prints.
type urlWatcher struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	urls chan string
	sent bool
}

func (w *urlWatcher) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	if !w.sent {
		for _, l := range strings.Split(w.buf.String(), "\n") {
			if strings.HasPrefix(l, "http") && strings.Contains(l, "code_challenge") {
				w.sent = true
				w.urls <- l
				break
			}
		}
	}
	return len(p), nil
}

func (w *urlWatcher) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

type probeResult struct {
	code      int
	out, errs string
	redirect  string // the pasted redirect URL (holds the authorization code)
}

// runProbe drives `oidc probe` like an operator: it waits for the printed authorization URL, "signs in" by calling it
// without following the redirect, and pastes the redirect (edit may alter it) on stdin.
func runProbe(t *testing.T, args []string, edit func(*url.URL)) probeResult {
	t.Helper()
	pr, pw := io.Pipe()
	out := &urlWatcher{urls: make(chan string, 1)}
	var errb bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- run(context.Background(), args, envFn(map[string]string{}), pr, out, &errb)
	}()
	res := probeResult{}
	select {
	case authURL := <-out.urls:
		hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := hc.Get(authURL)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		loc, err := url.Parse(resp.Header.Get("Location"))
		if err != nil || loc.String() == "" {
			t.Fatalf("authorization answered %d without a redirect", resp.StatusCode)
		}
		if edit != nil {
			edit(loc)
		}
		res.redirect = loc.String()
		_, _ = io.WriteString(pw, loc.String()+"\n")
	case c := <-done:
		_ = pw.Close()
		res.code, res.out, res.errs = c, out.String(), errb.String()
		return res
	case <-time.After(30 * time.Second):
		t.Fatal("the probe never printed an authorization URL")
	}
	select {
	case res.code = <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("the probe did not finish")
	}
	_ = pw.Close()
	res.out, res.errs = out.String(), errb.String()
	return res
}

func assertNoSecrets(t *testing.T, r probeResult, secrets ...string) {
	t.Helper()
	all := r.out + r.errs
	if u, err := url.Parse(r.redirect); err == nil {
		if c := u.Query().Get("code"); c != "" {
			secrets = append(secrets, c)
		}
	}
	for _, s := range secrets {
		if s != "" && strings.Contains(all, s) {
			t.Errorf("output leaks a secret value %q", s[:4])
		}
	}
	if strings.Contains(all, "eyJ") {
		t.Error("output contains a JWT")
	}
}

// TestCLI_OIDCProbe: against the OIDC rig (loopback PublicURL, test-probe not allow-listed; the rig clock starts at the
// real time because the probe's local verifier reads the wall clock) every check passes, the
// server refuses the probe client with CONNECTOR_FORBIDDEN and no grant row is created.
func TestCLI_OIDCProbe(t *testing.T) {
	t.Parallel()
	rig := servertest.New(t, servertest.Options{Fixture: true, OIDC: true, Clock: servertest.NewClock(time.Now())})
	r := runProbe(t, []string{"oidc", "probe", "--server", rig.URL, "--client-id", servertest.OIDCProbeClient}, nil)
	if r.code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", r.code, r.out, r.errs)
	}
	for _, want := range []string{"iss ok", "aud ok", "typ at+jwt", "alg RS256", "kid known", "scope metamusic: yes",
		"mm_user present: yes", "offline_access: yes", "refresh token: yes", "PKCE S256: yes", "iss on redirect: yes",
		"server verify: CONNECTOR_FORBIDDEN (ok)"} {
		if !strings.Contains(r.out, want) {
			t.Errorf("report lacks %q:\n%s", want, r.out)
		}
	}
	grants, err := rig.Services.Connectors.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 0 {
		t.Errorf("%d connector grants after the probe, want 0", len(grants))
	}
	assertNoSecrets(t, r)
}

// fakeServer is a stand-in for a metamusic server in front of a stub provider: it serves the real protected-resource
// metadata handler and answers POST /mcp with the given status and problem code.
type fakeServer struct {
	URL      string
	Provider *oidctest.Provider
}

func startFake(t *testing.T, po oidctest.Options, mcpStatus int, mcpCode string, withAS bool) *fakeServer {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	base := "http://" + srv.Listener.Addr().String()
	f := &fakeServer{URL: base}
	cfg := config.Config{PublicURL: base}
	if withAS {
		po.Clients = append(po.Clients[:0:0], po.Clients...)
		for i := range po.Clients {
			po.Clients[i].Audience = []string{base + "/mcp"}
		}
		f.Provider = oidctest.New(t, po)
		cfg.OIDCIssuer = f.Provider.URL
		cfg.OIDCScopes = []string{"openid", "metamusic"}
	}
	mux := http.NewServeMux()
	mux.Handle(connectors.PRMPath, connectors.PRMHandler(cfg))
	mux.HandleFunc("POST /mcp", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(mcpStatus)
		_, _ = io.WriteString(w, `{"code":"`+mcpCode+`","message":"x"}`)
	})
	srv.Config.Handler = mux
	srv.Start()
	t.Cleanup(srv.Close)
	return f
}

func probeStub(client oidctest.Client, userClaim bool) oidctest.Options {
	client.Scopes = []string{"openid", "offline_access", "metamusic"}
	return oidctest.Options{Clients: []oidctest.Client{client}, Users: map[string]string{"e2e": "subject-e2e-0001"},
		LoginAs: "e2e", UserClaim: userClaim, IssParam: true}
}

var probeClient = oidctest.Client{ID: "test-probe", AuthMethod: "none", RedirectURIs: []string{"http://127.0.0.1:8765/callback"}}

// TestCLI_OIDCProbe_ServerKeysUnavailable: the server cannot reach the provider's keys (503) => exit 1.
func TestCLI_OIDCProbe_ServerKeysUnavailable(t *testing.T) {
	t.Parallel()
	f := startFake(t, probeStub(probeClient, true), http.StatusServiceUnavailable, "UNAVAILABLE", true)
	r := runProbe(t, []string{"oidc", "probe", "--server", f.URL, "--client-id", "test-probe"}, nil)
	if r.code != 1 || !strings.Contains(r.out, "server verify: keys unavailable") {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errs)
	}
	assertNoSecrets(t, r)
}

// TestCLI_OIDCProbe_ServerWrongKeys: local checks pass but the server answers 401 invalid_token => exit 1.
func TestCLI_OIDCProbe_ServerWrongKeys(t *testing.T) {
	t.Parallel()
	f := startFake(t, probeStub(probeClient, true), http.StatusUnauthorized, "UNAUTHENTICATED", true)
	r := runProbe(t, []string{"oidc", "probe", "--server", f.URL, "--client-id", "test-probe"}, nil)
	if r.code != 1 || !strings.Contains(r.out, "server verify: invalid_token") || !strings.Contains(r.out, "kid known") {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errs)
	}
}

// TestCLI_OIDCProbe_UnexpectedAnswer: any other server answer is reported with its status.
func TestCLI_OIDCProbe_UnexpectedAnswer(t *testing.T) {
	t.Parallel()
	f := startFake(t, probeStub(probeClient, true), http.StatusOK, "OK", true)
	r := runProbe(t, []string{"oidc", "probe", "--server", f.URL, "--client-id", "test-probe"}, nil)
	if r.code != 1 || !strings.Contains(r.out, "server verify: unexpected 200") {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errs)
	}
}

// TestCLI_OIDCProbe_MissingUserClaim: a token without mm_user fails the probe.
func TestCLI_OIDCProbe_MissingUserClaim(t *testing.T) {
	t.Parallel()
	f := startFake(t, probeStub(probeClient, false), http.StatusForbidden, "CONNECTOR_FORBIDDEN", true)
	r := runProbe(t, []string{"oidc", "probe", "--server", f.URL, "--client-id", "test-probe"}, nil)
	if r.code != 1 || !strings.Contains(r.out, "mm_user present: no") {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errs)
	}
}

// TestCLI_OIDCProbe_StateMismatch: a pasted redirect with another state is refused.
func TestCLI_OIDCProbe_StateMismatch(t *testing.T) {
	t.Parallel()
	f := startFake(t, probeStub(probeClient, true), http.StatusForbidden, "CONNECTOR_FORBIDDEN", true)
	r := runProbe(t, []string{"oidc", "probe", "--server", f.URL, "--client-id", "test-probe"}, func(u *url.URL) {
		q := u.Query()
		q.Set("state", "not-the-state")
		u.RawQuery = q.Encode()
	})
	if r.code != 1 || !strings.Contains(r.errs, "state mismatch") || strings.Contains(r.out, "server verify") {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errs)
	}
	assertNoSecrets(t, r)
}

// TestCLI_OIDCProbe_NoAuthorizationServers: a server with OIDC off is a usage-class failure (exit 2).
func TestCLI_OIDCProbe_NoAuthorizationServers(t *testing.T) {
	t.Parallel()
	f := startFake(t, oidctest.Options{}, http.StatusUnauthorized, "UNAUTHENTICATED", false)
	code, _, errs := do(t, testEnv(t, "local"), "", "oidc", "probe", "--server", f.URL, "--client-id", "test-probe")
	if code != 2 || !strings.Contains(errs, "the server advertises no authorization server") {
		t.Fatalf("exit %d: %s", code, errs)
	}
}

// TestCLI_OIDCProbe_ConfidentialClient: the client secret is read from the file by the binary and never printed.
func TestCLI_OIDCProbe_ConfidentialClient(t *testing.T) {
	t.Parallel()
	secret := "canary-" + strings.Repeat("s3cr3t", 4)
	file := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(file, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := oidctest.Client{ID: "test-probe", Secret: secret, AuthMethod: "client_secret_post", RedirectURIs: []string{"http://127.0.0.1:8765/callback"}}
	f := startFake(t, probeStub(c, true), http.StatusForbidden, "CONNECTOR_FORBIDDEN", true)
	r := runProbe(t, []string{"oidc", "probe", "--server", f.URL, "--client-id", "test-probe", "--client-secret-file", file}, nil)
	if r.code != 0 {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.out, r.errs)
	}
	assertNoSecrets(t, r, secret)
}
