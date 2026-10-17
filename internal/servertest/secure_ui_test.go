package servertest_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// Wave 4a (D1): secure pages open inside the app. Only the client moved; these tests pin that the server gates
// did not (invariants 1 and 2) and that the second-tab step-up has what it needs from the server.

// Invariant 1: /human/api/* refuses every non-human principal, in both auth modes.
func TestSecureUI_HumanAPIRefusesNonHumanPrincipals(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"proxy", "local"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			r := servertest.New(t, servertest.Options{Mode: mode})
			agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeAdmin, auth.ScopeGenerate)
			if resp := agent.Do(t, "GET", "/human/api/devices", nil); resp.Status != 403 {
				t.Errorf("agent token with every scope: %d %s", resp.Status, resp.Body)
			}
			if resp := r.Device(t).Do(t, "GET", "/human/api/devices", nil); resp.Status != 401 && resp.Status != 403 {
				t.Errorf("device cookie: %d %s", resp.Status, resp.Body)
			}
			if resp := servertest.Bearer(r, "mm_nonsense").Do(t, "GET", "/human/api/devices", nil); resp.Status != 401 {
				t.Errorf("bad bearer: %d", resp.Status)
			}
			if resp := r.Human(t).Do(t, "GET", "/human/api/devices", nil); resp.Status != 200 {
				t.Errorf("human control: %d %s", resp.Status, resp.Body)
			}
		})
	}
}

// Invariants 1 and 2 in local mode: a stale step-up refuses the API with STEP_UP_REQUIRED, and a DOCUMENT load of
// a secure page (typed URL, notification link) is redirected to the step-up form, exactly as before D1.
func TestSecureUI_LocalStaleStepUpGatesAPIAndDocuments(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Mode: "local"})
	h := r.Human(t)
	r.Clock.Advance(10*time.Minute + time.Second)
	for _, p := range []string{"/human/api/devices", "/human/api/spend/caps"} {
		resp := h.Do(t, "GET", p, nil)
		if resp.Status != 403 || !strings.Contains(string(resp.Body), "STEP_UP_REQUIRED") {
			t.Errorf("%s: %d %s", p, resp.Status, resp.Body)
		}
	}
	for _, p := range []string{"/human/spend", "/human/generate", "/human/confirm/b1", "/human/"} {
		resp := h.Do(t, "GET", p, nil, "Accept", "text/html", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document")
		want := "/auth/step-up?next=" + url.QueryEscape(p)
		if resp.Status != 303 || resp.Header.Get("Location") != want {
			t.Errorf("document %s: %d %q (want %q)", p, resp.Status, resp.Header.Get("Location"), want)
		}
	}
}

// The second-tab flow: /auth/step-up?next=/auth/step-up/done lands on the done page, which carries the signalling
// script only while the confirmation is fresh.
func TestSecureUI_StepUpDonePage(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Mode: "local"})
	h := r.Human(t)
	r.Clock.Advance(10*time.Minute + time.Second)

	stale := h.Do(t, "GET", "/auth/step-up/done", nil)
	if stale.Status != 403 || strings.Contains(string(stale.Body), "<script") || !strings.Contains(string(stale.Body), "/auth/step-up?next=/auth/step-up/done") {
		t.Fatalf("stale: %d %s", stale.Status, stale.Body)
	}

	form := url.Values{"password": {"correct horse battery staple"}, "next": {"/auth/step-up/done"}}.Encode()
	resp := h.Do(t, "POST", "/auth/step-up", form, "Content-Type", "application/x-www-form-urlencoded")
	if resp.Status != 303 || resp.Header.Get("Location") != "/auth/step-up/done" {
		t.Fatalf("step-up with next=done: %d %q", resp.Status, resp.Header.Get("Location"))
	}
	done := h.Do(t, "GET", "/auth/step-up/done", nil)
	if done.Status != 200 || !strings.Contains(string(done.Body), `<script src="/auth/step-up/done.js"`) {
		t.Fatalf("fresh: %d %s", done.Status, done.Body)
	}
	csp := done.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") || done.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("headers: csp=%q cc=%q", csp, done.Header.Get("Cache-Control"))
	}
	js := h.Do(t, "GET", "/auth/step-up/done.js", nil)
	if js.Status != 200 || !strings.Contains(string(js.Body), "BroadcastChannel(\"mm-stepup\")") {
		t.Fatalf("done.js: %d %s", js.Status, js.Body)
	}
	// the confirmation it announces is real: the gated API now answers
	if resp := h.Do(t, "GET", "/human/api/devices", nil); resp.Status != 200 {
		t.Errorf("after done: %d", resp.Status)
	}

	// anonymous callers get the login redirect, never the page; other targets stay confined
	nr := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	anon, err := nr.Get(r.URL + "/auth/step-up/done")
	if err != nil {
		t.Fatal(err)
	}
	_ = anon.Body.Close()
	if anon.StatusCode != 303 || anon.Header.Get("Location") != "/auth/login" {
		t.Errorf("anonymous: %d %q", anon.StatusCode, anon.Header.Get("Location"))
	}
	for _, next := range []string{"/auth/step-up/done/", "/auth/step-up/done?x=1", "/auth/login", "/auth/step-up/done.js"} {
		f := url.Values{"password": {"correct horse battery staple"}, "next": {next}}.Encode()
		if resp := h.Do(t, "POST", "/auth/step-up", f, "Content-Type", "application/x-www-form-urlencoded"); resp.Header.Get("Location") != "/human/tokens" {
			t.Errorf("next %q -> %q", next, resp.Header.Get("Location"))
		}
	}

	// proxy mode has no /auth/ pages: the edge owns the second factor
	p := servertest.New(t, servertest.Options{})
	if resp := p.Human(t).Do(t, "GET", "/auth/step-up/done", nil); resp.Status != 404 {
		t.Errorf("proxy: %d", resp.Status)
	}
}

func stepUpUntil(t *testing.T, c *servertest.Client) any {
	t.Helper()
	who := c.OK(t, "GET", "/api/me", nil).(map[string]any)
	v, present := who["step_up_until"]
	if !present {
		t.Fatalf("whoami has no step_up_until key: %v", who)
	}
	return v
}

// whoami.step_up_until: the fresh deadline for a local human, a past time once stale, null where step-up does not apply.
func TestSecureUI_WhoamiStepUpUntil(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Mode: "local"})
	h := r.Human(t)
	got, ok := stepUpUntil(t, h).(string)
	if !ok {
		t.Fatalf("local human: %v", stepUpUntil(t, h))
	}
	until, err := time.Parse(time.RFC3339, got)
	if err != nil {
		t.Fatal(err)
	}
	if d := until.Sub(r.Clock.Now()); d != 10*time.Minute {
		t.Errorf("fresh step-up lasts %v from now, want 10m", d)
	}
	r.Clock.Advance(10*time.Minute + time.Second)
	got, _ = stepUpUntil(t, h).(string)
	if until, _ = time.Parse(time.RFC3339, got); !until.Before(r.Clock.Now()) {
		t.Errorf("stale step-up must be in the past: %s (now %s)", got, r.Clock.Now())
	}
	agent, _ := r.Agent(t, auth.ScopeRead)
	if v := stepUpUntil(t, agent); v != nil {
		t.Errorf("agent: %v", v)
	}

	p := servertest.New(t, servertest.Options{})
	if v := stepUpUntil(t, p.Human(t)); v != nil {
		t.Errorf("proxy human: %v", v)
	}
	// MCP parity: the same operation, the same field
	res, err := p.MCP(t, auth.ScopeRead).CallTool(context.Background(), &mcp.CallToolParams{Name: "whoami"})
	if err != nil || res.IsError {
		t.Fatalf("mcp whoami: %v %+v", err, res)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(raw), `"step_up_until":null`) {
		t.Errorf("mcp whoami lacks step_up_until: %s", raw)
	}
}
