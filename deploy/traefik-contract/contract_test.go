//go:build routing

// Package contract proves deploy/labels.example.yml against a real Traefik: the example labels
// are applied to an echo backend, Authelia is replaced by a stub forward-auth, and every router
// in the C24 table is exercised over TLS (Traefik's default certificate).
package contract

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	host       = "mm.example.com"
	edgeSecret = "edge-secret-for-contract-tests"
	stubUser   = "contract-user"
)

var identityHeaders = []string{"Remote-User", "Remote-Groups", "Remote-Name", "Remote-Email", "X-MM-Edge"}

type edge struct {
	t       *testing.T
	baseURL string
	stubURL string
	client  *http.Client
	logs    func() string
}

type echoReport struct {
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Headers map[string][]string `json:"headers"`
}

type reply struct {
	Status int
	Header http.Header
	Body   []byte
}

func (r reply) router() string { return r.Header.Get("X-Test-Router") }

func (r reply) echo(t *testing.T) echoReport {
	t.Helper()
	var e echoReport
	if err := json.Unmarshal(r.Body, &e); err != nil {
		t.Fatalf("echo body is not JSON (status %d): %v: %.200s", r.Status, err, r.Body)
	}
	return e
}

func run(t *testing.T, dir string, env []string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// labelLines returns every `- traefik...` list item of the example file in order.
func labelLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, ln := range strings.Split(string(b), "\n") {
		if s := strings.TrimSpace(ln); strings.HasPrefix(s, "- traefik.") {
			out = append(out, strings.TrimPrefix(s, "- "))
		}
	}
	if len(out) == 0 {
		t.Fatal("no labels found in " + path)
	}
	return out
}

var routerMW = regexp.MustCompile(`^traefik\.http\.routers\.([^.]+)\.middlewares=(.*)$`)

// middlewareOrder parses the middleware list of each router from the example labels.
func middlewareOrder(labels []string) map[string][]string {
	m := map[string][]string{}
	for _, l := range labels {
		if g := routerMW.FindStringSubmatch(l); g != nil {
			m[g[1]] = strings.Split(g[2], ",")
		}
	}
	return m
}

func twoFactorPatterns(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pats []string
	in := false
	for _, ln := range strings.Split(string(b), "\n") {
		s := strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(s, "policy:"):
			in = strings.TrimSpace(strings.TrimPrefix(s, "policy:")) == "two_factor"
		case in && strings.HasPrefix(s, "- '"):
			pats = append(pats, strings.TrimSuffix(strings.TrimPrefix(s, "- '"), "'"))
		}
	}
	if len(pats) == 0 {
		t.Fatal("no two_factor resources in " + path)
	}
	return strings.Join(pats, "\n")
}

func startEdge(t *testing.T) (*edge, map[string][]string) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil || exec.Command("docker", "compose", "version").Run() != nil {
		if os.Getenv("MM_REQUIRE_ROUTING") == "1" {
			t.Fatal("docker compose is required (MM_REQUIRE_ROUTING=1)")
		}
		t.Skip("docker compose not available")
	}
	deploy, _ := filepath.Abs("..")
	root := filepath.Join(deploy, "..")
	binDir := t.TempDir()
	for _, p := range []string{"stub-forwardauth", "echo"} {
		out := "stub"
		if p == "echo" {
			out = "echo"
		}
		run(t, root, []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=" + runtime.GOARCH},
			"go", "build", "-tags", "routing", "-o", filepath.Join(binDir, out), "./deploy/traefik-contract/"+p)
	}

	labels := labelLines(t, filepath.Join(deploy, "labels.example.yml"))
	order := middlewareOrder(labels)
	var gen []string
	for _, l := range labels {
		if l == "traefik.enable=true" {
			continue // the fixture must stay invisible to a host Traefik (exposedbydefault=false)
		}
		if strings.Contains(l, ".tls.certresolver=") {
			continue // no ACME in the test; Traefik serves its default certificate
		}
		l = strings.ReplaceAll(l, "${MM_EDGE_SECRET:-}", edgeSecret)
		if g := routerMW.FindStringSubmatch(l); g != nil {
			// test-only response header goes first so it also marks forward-auth rejections
			l = fmt.Sprintf("traefik.http.routers.%s.middlewares=mm-test-%s,%s", g[1], g[1], g[2])
			gen = append(gen, fmt.Sprintf("traefik.http.middlewares.mm-test-%s.headers.customresponseheaders.X-Test-Router=%s", g[1], g[1]))
		}
		gen = append(gen, l)
	}
	var sb strings.Builder
	sb.WriteString("services:\n  echo:\n    labels:\n")
	for _, l := range gen {
		q, _ := json.Marshal(strings.ReplaceAll(l, "$", "$$"))
		sb.WriteString("      - " + string(q) + "\n")
	}
	override := filepath.Join(t.TempDir(), "override.yml")
	if err := os.WriteFile(override, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	rnd := make([]byte, 4)
	if _, err := rand.Read(rnd); err != nil {
		t.Fatal(err)
	}
	project := "mm-routing-" + hex.EncodeToString(rnd)
	env := []string{
		"PROJECT=" + project,
		"BIN_DIR=" + binDir,
		"STUB_USER=" + stubUser,
		"STUB_TWO_FACTOR=" + twoFactorPatterns(t, filepath.Join(deploy, "authelia-rules.example.yml")),
	}
	dc := func(args ...string) string {
		return run(t, ".", env, "docker", append([]string{"compose", "-p", project, "-f", "compose.yml", "-f", override}, args...)...)
	}
	t.Cleanup(func() { dc("down", "--volumes", "--remove-orphans", "--timeout", "2") })
	dc("up", "-d", "--quiet-pull")

	// guard: no fixture container may be visible to a host Traefik with exposedbydefault=false
	for _, id := range strings.Fields(dc("ps", "-q")) {
		lbl := run(t, ".", nil, "docker", "inspect", "--format", `{{json .Config.Labels}}`, id)
		if strings.Contains(lbl, `"traefik.enable"`) {
			t.Fatalf("fixture container %s carries a traefik.enable label: %s", id, lbl)
		}
	}

	hostPort := func(svc, port string) string {
		out := dc("port", svc, port) // 127.0.0.1:49153
		return out[strings.LastIndex(out, ":")+1:]
	}
	e := &edge{
		logs:    func() string { return dc("logs", "--no-color", "traefik") },
		t:       t,
		baseURL: "https://127.0.0.1:" + hostPort("traefik", "443"),
		stubURL: "http://127.0.0.1:" + hostPort("stub", "9091"),
		client: &http.Client{
			Timeout:       10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Transport: &http.Transport{
				TLSClientConfig:    &tls.Config{InsecureSkipVerify: true, ServerName: host}, //nolint:gosec // test cert
				DisableCompression: true,
			},
		},
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		r, err := e.try(http.MethodGet, "/manifest.webmanifest", nil, nil)
		if err == nil && r.router() == "mm-public" && r.Status == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			logs := dc("logs", "--no-color", "--tail", "40", "traefik")
			t.Fatalf("edge not ready: %v\n%s", err, logs)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return e, order
}

func (e *edge) try(method, path string, hdr http.Header, raw map[string][]string) (reply, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, e.baseURL+path, nil)
	if err != nil {
		return reply{}, err
	}
	req.Host = host
	for k, v := range hdr {
		req.Header[k] = v // keys as given: lets tests send non-canonical names
	}
	for k, v := range raw {
		req.Header[k] = v
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return reply{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	return reply{Status: resp.StatusCode, Header: resp.Header, Body: b}, err
}

func (e *edge) do(method, path string, hdr http.Header) reply {
	e.t.Helper()
	r, err := e.try(method, path, hdr, nil)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	return r
}

func (e *edge) stubCalls() int {
	e.t.Helper()
	resp, err := http.Get(e.stubURL + "/__calls") //nolint:noctx // test helper
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	var n int
	if _, err := fmt.Sscan(string(b), &n); err != nil {
		e.t.Fatalf("stub calls %q: %v", b, err)
	}
	return n
}

func html() http.Header { return http.Header{"Accept": {"text/html"}} }

func session(level string) http.Header {
	return http.Header{"Cookie": {"mm_stub_session=" + level}}
}

func bearer() http.Header { return http.Header{"Authorization": {"Bearer abc.def"}} }

func merge(hs ...http.Header) http.Header {
	out := http.Header{}
	for _, h := range hs {
		for k, v := range h {
			out[k] = v
		}
	}
	return out
}

func TestRoutingContract(t *testing.T) {
	e, order := startEdge(t)

	t.Run("routing", func(t *testing.T) {
		cases := []struct {
			path   string
			hdr    http.Header
			router string
		}{
			{"/media/x", nil, "mm-media"},
			{"/rest/x", nil, "mm-media"},
			{"/worker/x", nil, "mm-media"},
			{"/worker/v1/uploads/x", nil, "mm-media"},
			{"/worker/v1/attempts/x/start", nil, "mm-media"},
			{"/mcp", nil, "mm-agent"},
			{"/mcp/x", nil, "mm-agent"},
			{"/.well-known/oauth-protected-resource/mcp", nil, "mm-agent"},
			{"/oauth/token", nil, "mm-agent"},
			{"/oauth/register", nil, "mm-agent"},
			{"/oauth/revoke", nil, "mm-agent"},
			{"/api/openapi.json", nil, "mm-agent"},
			{"/api/tracks", bearer(), "mm-api-bearer"},
			{"/api/openapi.json", bearer(), "mm-api-bearer"},
			{"/manifest.webmanifest", nil, "mm-public"},
			{"/icons/a.png", nil, "mm-public"},
		}
		for _, c := range cases {
			before := e.stubCalls()
			r := e.do(http.MethodGet, c.path, c.hdr)
			if r.router() != c.router || r.Status != http.StatusOK {
				t.Errorf("%s: router %q status %d, want %s 200", c.path, r.router(), r.Status, c.router)
			}
			if n := e.stubCalls(); n != before {
				t.Errorf("%s: reached forward-auth (%d calls)", c.path, n-before)
			}
		}
		uiCases := []struct {
			path string
			hdr  http.Header
		}{
			{"/restore", session("two")},
			{"/mediax", session("two")},
			{"/api/tracks", session("two")},
			{"/human/api/x", merge(session("two"), bearer())},
			{"/", session("one")},
		}
		for _, c := range uiCases {
			before := e.stubCalls()
			r := e.do(http.MethodGet, c.path, c.hdr)
			if r.router() != "mm-ui" || r.Status != http.StatusOK {
				t.Errorf("%s: router %q status %d, want mm-ui 200", c.path, r.router(), r.Status)
			}
			if n := e.stubCalls(); n != before+1 {
				t.Errorf("%s: forward-auth calls = %d, want 1", c.path, n-before)
			}
		}
	})

	t.Run("pair and device", func(t *testing.T) {
		cookie := func(v string) http.Header { return http.Header{"Cookie": {v}} }
		for _, path := range []string{"/start", "/pair", "/pair.js", "/auth/pair"} {
			before := e.stubCalls()
			r := e.do(http.MethodGet, path, nil)
			if r.router() != "mm-pair" || r.Status != http.StatusOK {
				t.Errorf("%s: router %q status %d, want mm-pair 200", path, r.router(), r.Status)
			}
			if n := e.stubCalls(); n != before {
				t.Errorf("%s: reached forward-auth (%d calls)", path, n-before)
			}
			spoof := map[string][]string{}
			for _, h := range identityHeaders {
				spoof[h] = []string{"evil"}
			}
			r, err := e.try(http.MethodGet, path, nil, spoof)
			if err != nil || r.router() != "mm-pair" {
				t.Errorf("%s spoof: err %v router %q", path, err, r.router())
				continue
			}
			for _, h := range identityHeaders {
				if v, ok := r.echo(t).Headers[http.CanonicalHeaderKey(h)]; ok {
					t.Errorf("%s: %s arrived as %q", path, h, v)
				}
			}
		}
		deviceCases := []struct {
			path   string
			hdr    http.Header
			router string
			stub   bool
		}{
			{"/", cookie("mm_dev=x"), "mm-device", false},
			{"/api/tracks", cookie("mm_dev=x"), "mm-device", false},
			{"/api/tracks", cookie("a=1; mm_dev=x"), "mm-device", false},
			{"/api/tracks", cookie("a=1;mm_dev=x"), "mm-device", false},
			{"/api/tracks", cookie("xmm_dev=1"), "mm-ui", true},
			{"/api/tracks", cookie("mm_dev_x=1"), "mm-ui", true},
			{"/human", cookie("mm_dev=x"), "mm-ui", true},
			{"/human/", cookie("mm_dev=x"), "mm-ui", true},
			{"/human/tokens", cookie("mm_dev=x"), "mm-ui", true},
			{"/auth/step-up", cookie("mm_dev=x"), "mm-ui", true},
			{"/media/x", cookie("mm_dev=x"), "mm-media", false},
			{"/mcp", cookie("mm_dev=x"), "mm-agent", false},
			{"/human/api/generations", cookie("mm_dev=x"), "mm-ui", true},
			{"/worker/v1/uploads/x", cookie("mm_dev=x"), "mm-media", false},
		}
		for _, c := range deviceCases {
			before := e.stubCalls()
			r := e.do(http.MethodGet, c.path, c.hdr)
			if r.router() != c.router {
				t.Errorf("%s %v: router %q, want %s", c.path, c.hdr["Cookie"], r.router(), c.router)
			}
			want := before
			if c.stub {
				want++
			}
			if n := e.stubCalls(); n != want {
				t.Errorf("%s %v: forward-auth calls = %d, want %d", c.path, c.hdr["Cookie"], n-before, want-before)
			}
		}
		// mm-device: identity headers stripped, response compressed
		spoof := map[string][]string{}
		for _, h := range identityHeaders {
			spoof[h] = []string{"evil"}
		}
		r, err := e.try(http.MethodGet, "/api/tracks", cookie("mm_dev=x"), spoof)
		if err != nil || r.router() != "mm-device" || r.Status != http.StatusOK {
			t.Fatalf("mm-device spoof: err %v router %q status %d", err, r.router(), r.Status)
		}
		for _, h := range identityHeaders {
			if v, ok := r.echo(t).Headers[http.CanonicalHeaderKey(h)]; ok {
				t.Errorf("mm-device: %s arrived as %q", h, v)
			}
		}
		gz := merge(cookie("mm_dev=x"), http.Header{"Accept-Encoding": {"gzip"}})
		if r := e.do(http.MethodGet, "/restore?size=5000", gz); r.router() != "mm-device" || r.Header.Get("Content-Encoding") != "gzip" {
			t.Errorf("mm-device compress: router %q Content-Encoding %q", r.router(), r.Header.Get("Content-Encoding"))
		}
	})

	t.Run("identity", func(t *testing.T) {
		spoof := map[string][]string{}
		for _, h := range identityHeaders {
			spoof[h] = []string{"evil", "evil2"} // duplicate values
			spoof[strings.ToLower(h)] = []string{"evil3"}
			spoof[strings.ToUpper(h)] = []string{"evil4"}
		}
		nonUI := []struct {
			path string
			hdr  http.Header
		}{
			{"/media/x", nil}, {"/rest/x", nil}, {"/worker/x", nil}, {"/mcp", nil},
			{"/worker/v1/uploads/x", nil}, {"/worker/v1/attempts/x/start", nil},
			{"/oauth/token", nil}, {"/.well-known/x", nil}, {"/api/openapi.json", nil},
			{"/api/tracks", bearer()}, {"/manifest.webmanifest", nil}, {"/icons/a.png", nil},
		}
		for _, c := range nonUI {
			r, err := e.try(http.MethodGet, c.path, c.hdr, spoof)
			if err != nil || r.Status != http.StatusOK {
				t.Errorf("%s: err %v status %d", c.path, err, r.Status)
				continue
			}
			got := r.echo(t).Headers
			for _, h := range identityHeaders {
				if v, ok := got[http.CanonicalHeaderKey(h)]; ok {
					t.Errorf("%s (%s): %s arrived as %q", c.path, r.router(), h, v)
				}
			}
		}
		r, err := e.try(http.MethodGet, "/api/tracks", session("two"), spoof)
		if err != nil || r.router() != "mm-ui" || r.Status != http.StatusOK {
			t.Fatalf("mm-ui spoof request: err %v router %q status %d", err, r.router(), r.Status)
		}
		got := r.echo(t).Headers
		if v := got["Remote-User"]; len(v) != 1 || v[0] != stubUser {
			t.Errorf("mm-ui Remote-User = %q, want [%s]", v, stubUser)
		}
		if v := got["X-Mm-Edge"]; len(v) != 1 || v[0] != edgeSecret {
			t.Errorf("mm-ui X-MM-Edge = %q, want exactly [%s]", v, edgeSecret)
		}
		for _, h := range []string{"Remote-Groups", "Remote-Name", "Remote-Email"} {
			if v := got[h]; len(v) != 1 || strings.HasPrefix(v[0], "evil") {
				t.Errorf("mm-ui %s = %q, want the stub's single value", h, v)
			}
		}
	})

	t.Run("middleware order", func(t *testing.T) {
		wantUI := []string{"authelia@docker", "mm-edge-mark", "sec-headers@docker", "mm-compress"}
		if got := order["mm-ui"]; strings.Join(got, ",") != strings.Join(wantUI, ",") {
			t.Errorf("mm-ui middlewares = %v, want %v", got, wantUI)
		}
		for _, r := range []string{"mm-public", "mm-api-bearer", "mm-agent", "mm-media", "mm-pair", "mm-device"} {
			mws := order[r]
			if len(mws) == 0 || mws[0] != "mm-strip-identity" {
				t.Errorf("%s: first middleware must be mm-strip-identity, got %v", r, mws)
			}
		}
		for _, mw := range order["mm-ui"] {
			if mw == "mm-strip-identity" {
				t.Error("mm-strip-identity must not be on mm-ui")
			}
		}
		for _, mw := range order["mm-media"] {
			if strings.Contains(mw, "compress") {
				t.Errorf("mm-media has %s", mw)
			}
		}
		for r, want := range map[string][]string{
			"mm-pair":   {"mm-strip-identity", "sec-ratelimit@docker", "sec-headers@docker"},
			"mm-device": {"mm-strip-identity", "sec-ratelimit@docker", "sec-headers@docker", "mm-compress"},
		} {
			if got := order[r]; strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("%s middlewares = %v, want %v", r, got, want)
			}
		}
		for r, mws := range order {
			has := false
			for _, mw := range mws {
				has = has || mw == "mm-compress"
			}
			if has != (r == "mm-device" || r == "mm-ui") {
				t.Errorf("%s: mm-compress present = %v", r, has)
			}
		}
		if len(order) != 7 {
			t.Errorf("routers = %d, want 7", len(order))
		}
	})

	t.Run("second factor", func(t *testing.T) {
		// Authelia 4.39 getAuthzRedirectStatusCode: 401 XHR/non-HTML, 302 GET, 303 other methods
		xhr := http.Header{"X-Requested-With": {"XMLHttpRequest"}}
		if r := e.do(http.MethodPost, "/human/api/x", merge(session("one"), html())); r.Status != http.StatusSeeOther || r.router() != "mm-ui" {
			t.Errorf("one factor POST navigation: status %d router %q, want 303 mm-ui", r.Status, r.router())
		}
		if r := e.do(http.MethodPost, "/human/api/x", merge(session("one"), xhr)); r.Status != http.StatusUnauthorized {
			t.Errorf("one factor XHR: status %d, want 401", r.Status)
		}
		if r := e.do(http.MethodPost, "/human/api/x", session("two")); r.Status != http.StatusOK {
			t.Errorf("two factors: status %d, want 200", r.Status)
		}
		if r := e.do(http.MethodGet, "/oauth/authorize?client_id=x", merge(session("one"), html())); r.Status != http.StatusFound {
			t.Errorf("oauth authorize at one factor: status %d, want 302", r.Status)
		}
		if r := e.do(http.MethodPost, "/api/tracks", session("one")); r.Status != http.StatusOK {
			t.Errorf("one_factor path at one factor: status %d, want 200", r.Status)
		}
	})

	t.Run("m3 spend and provider ops need a second factor", func(t *testing.T) {
		xhr := http.Header{"X-Requested-With": {"XMLHttpRequest"}}
		ops := []struct{ method, path string }{
			{http.MethodPost, "/human/api/batches/x/approve"},
			{http.MethodPost, "/human/api/generations"},
			{http.MethodPut, "/human/api/spend/caps"},
			{http.MethodPost, "/human/api/provider/state"},
		}
		for _, o := range ops {
			before := e.stubCalls()
			if r := e.do(o.method, o.path, merge(session("one"), xhr)); r.Status != http.StatusUnauthorized {
				t.Errorf("%s %s at one factor: status %d, want 401", o.method, o.path, r.Status)
			}
			r := e.do(o.method, o.path, session("two"))
			if r.Status != http.StatusOK || r.router() != "mm-ui" {
				t.Errorf("%s %s at two factors: status %d router %q, want 200 mm-ui", o.method, o.path, r.Status, r.router())
			}
			if n := e.stubCalls(); n != before+2 {
				t.Errorf("%s %s: forward-auth calls = %d, want 2", o.method, o.path, n-before)
			}
		}
	})

	t.Run("worker routes skip forward-auth and are not logged or compressed", func(t *testing.T) {
		for _, path := range []string{"/worker/v1/uploads/x", "/worker/v1/attempts/x/start"} {
			before := e.stubCalls()
			r := e.do(http.MethodPost, path+"?size=5000", http.Header{"Accept-Encoding": {"gzip"}})
			if r.router() != "mm-media" || r.Status != http.StatusOK {
				t.Errorf("POST %s: router %q status %d, want mm-media 200", path, r.router(), r.Status)
			}
			if enc := r.Header.Get("Content-Encoding"); enc != "" {
				t.Errorf("POST %s: Content-Encoding %q", path, enc)
			}
			if n := e.stubCalls(); n != before {
				t.Errorf("POST %s: reached forward-auth (%d calls)", path, n-before)
			}
		}
	})

	t.Run("xhr session loss", func(t *testing.T) {
		xhr := http.Header{"X-Requested-With": {"XMLHttpRequest"}}
		if r := e.do(http.MethodGet, "/api/tracks", xhr); r.Status != http.StatusUnauthorized || r.router() != "mm-ui" {
			t.Errorf("XHR without session: status %d router %q, want 401 mm-ui", r.Status, r.router())
		}
		r := e.do(http.MethodGet, "/api/tracks", html())
		if r.Status != http.StatusFound {
			t.Errorf("navigation without session: status %d, want 302", r.Status)
		}
		if u, err := url.Parse(r.Header.Get("Location")); err != nil || u.Host == "" {
			t.Errorf("redirect Location = %q", r.Header.Get("Location"))
		}
	})

	t.Run("media is not compressed", func(t *testing.T) {
		gz := http.Header{"Accept-Encoding": {"gzip"}}
		r := e.do(http.MethodGet, "/media/x?size=5000", gz)
		if enc := r.Header.Get("Content-Encoding"); enc != "" || len(r.Body) < 5000 {
			t.Errorf("/media/ Content-Encoding %q, body %d bytes", enc, len(r.Body))
		}
		// positive control: the compress middleware really is active on mm-ui
		r = e.do(http.MethodGet, "/restore?size=5000", merge(gz, session("two")))
		if r.Header.Get("Content-Encoding") != "gzip" {
			t.Errorf("mm-ui control: Content-Encoding %q, want gzip", r.Header.Get("Content-Encoding"))
		}
	})

	t.Run("media access log is off", func(t *testing.T) {
		e.do(http.MethodGet, "/media/canary-path?sig=canary-sig", nil)
		e.do(http.MethodPost, "/worker/v1/attempts/canary-attempt/start?sig=canary-wsig", nil)
		e.do(http.MethodGet, "/poscontrol-ui", session("two"))
		deadline := time.Now().Add(10 * time.Second)
		var logs string
		for time.Now().Before(deadline) {
			if logs = e.logs(); strings.Contains(logs, "poscontrol-ui") {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if !strings.Contains(logs, "poscontrol-ui") {
			t.Fatal("positive control: mm-ui request missing from the access log")
		}
		if strings.Contains(logs, "canary-sig") || strings.Contains(logs, "canary-path") ||
			strings.Contains(logs, "canary-wsig") || strings.Contains(logs, "canary-attempt") {
			t.Error("a /media/ request was access-logged")
		}
	})
}
