package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/nicolasacchi/metamusic/internal/oidc"
)

const stepClient = "metamusic-stepup"

// fakeIDP is a minimal OIDC provider: discovery, JWKS, an authorization endpoint that redirects straight back and a
// token endpoint that checks the code, the PKCE verifier and the client secret and returns a configurable ID token.
type fakeIDP struct {
	t      *testing.T
	srv    *httptest.Server
	key    *rsa.PrivateKey
	clock  *fakeClock
	secret string

	mu        sync.Mutex
	nonce     string
	challenge string
	authQuery url.Values

	// knobs
	authAge   time.Duration // now - auth_time
	amr       []string
	user      string
	nonceOver string // non-empty: put this nonce in the token instead of the request's
	aud       string
	badSig    bool
}

func newFakeIDP(t *testing.T, clk *fakeClock) *fakeIDP {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIDP{t: t, key: k, clock: clk, secret: "s3cret-value", amr: []string{"pwd", "otp", "mfa"}, user: "alice", aud: stepClient}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/authorize",
			"token_endpoint": f.srv.URL + "/token", "jwks_uri": f.srv.URL + "/jwks"})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		e := big.NewInt(int64(k.E)).Bytes()
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(k.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(e)}}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f.mu.Lock()
		f.nonce, f.challenge, f.authQuery = q.Get("nonce"), q.Get("code_challenge"), q
		f.mu.Unlock()
		back, _ := url.Parse(q.Get("redirect_uri"))
		bq := back.Query()
		bq.Set("code", "code-1")
		bq.Set("state", q.Get("state"))
		back.RawQuery = bq.Encode()
		http.Redirect(w, r, back.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id, pw, ok := r.BasicAuth()
		f.mu.Lock()
		nonce, challenge := f.nonce, f.challenge
		f.mu.Unlock()
		if r.PostForm.Get("code") != "code-1" || !ok || id != stepClient || pw != f.secret || oidc.PKCEChallenge(r.PostForm.Get("code_verifier")) != challenge {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		if f.nonceOver != "" {
			nonce = f.nonceOver
		}
		now := f.clock.Now()
		claims := jwt.MapClaims{"iss": f.srv.URL, "aud": f.aud, "sub": "uuid-1", "exp": now.Add(time.Hour).Unix(), "iat": now.Unix(),
			"nonce": nonce, "auth_time": now.Add(-f.authAge).Unix(), "preferred_username": f.user}
		if f.amr != nil {
			claims["amr"] = f.amr
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = "k1"
		signKey := f.key
		if f.badSig {
			signKey, _ = rsa.GenerateKey(rand.Reader, 2048)
		}
		s, err := tok.SignedString(signKey)
		if err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": s, "token_type": "Bearer"})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

type proxyStepRig struct {
	idp *fakeIDP
	clk *fakeClock
	su  *ProxyStepUp
	h   http.Handler
}

func newProxyStepRig(t *testing.T, enabled bool) *proxyStepRig {
	t.Helper()
	clk := newFakeClock()
	idp := newFakeIDP(t, clk)
	v, err := oidc.NewVerifier(oidc.Config{Issuer: idp.srv.URL}, nil, clk.Now)
	if err != nil {
		t.Fatal(err)
	}
	client := oidc.NewStepUpClient(v, oidc.StepUpConfig{ClientID: stepClient, ClientSecret: idp.secret, RedirectURL: "http://mm.example.com/auth/step-up/callback"})
	su := NewProxyStepUp(client, key, "http://mm.example.com", clk.Now)
	c, s := cfg("proxy")
	c.PublicURL = "http://mm.example.com"
	mux := http.NewServeMux()
	vf := Verifiers{Tokens: fakeTokens{}, Sessions: fakeSessions{}}
	if enabled {
		mux.Handle("/auth/", su.Handler())
		vf.StepUp = su
	} else {
		mux.HandleFunc("/auth/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	return &proxyStepRig{idp: idp, clk: clk, su: su, h: Middleware(c, s, vf)(mux)}
}

// do sends a request as the edge-authenticated user alice, carrying the given cookies.
func (p *proxyStepRig) do(path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	r.RemoteAddr = "10.0.0.1:1"
	r.Header.Set("Remote-User", "alice")
	r.Header.Set("X-MM-Edge", edge)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, r)
	return rec
}

func cookieOf(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name && c.MaxAge >= 0 && c.Value != "" {
			return c
		}
	}
	return nil
}

// login runs the whole browser flow and returns the callback response.
func (p *proxyStepRig) login(t *testing.T, next string) *httptest.ResponseRecorder {
	t.Helper()
	start := p.do("/auth/step-up?next=" + url.QueryEscape(next))
	if start.Code != http.StatusSeeOther {
		t.Fatalf("start: %d %s", start.Code, start.Body)
	}
	flow := cookieOf(start, flowCookieName)
	if flow == nil {
		t.Fatal("no flow cookie")
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Get(start.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	back, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return p.do(back.Path+"?"+back.RawQuery, flow)
}

func TestProxyStepUp_RefusedThenAllowed(t *testing.T) {
	t.Parallel()
	p := newProxyStepRig(t, true)

	rec := p.do("/human/api/devices")
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), `"STEP_UP_REQUIRED"`) {
		t.Fatalf("without step-up: %d %s", rec.Code, rec.Body)
	}
	if rec = p.do("/human/tokens"); rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/auth/step-up?next=") {
		t.Fatalf("page without step-up: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec = p.do("/api/ping"); rec.Code != 200 {
		t.Fatalf("non-human path must not be gated: %d", rec.Code)
	}

	cb := p.login(t, "/human/tokens")
	if cb.Code != http.StatusSeeOther || cb.Header().Get("Location") != "/human/tokens" {
		t.Fatalf("callback: %d %q %s", cb.Code, cb.Header().Get("Location"), cb.Body)
	}
	step := cookieOf(cb, p.su.cookieName())
	if step == nil || !step.HttpOnly {
		t.Fatalf("no step-up cookie: %v", cb.Result().Cookies())
	}
	// the authorization request carried the hardening parameters
	q := p.idp.authQuery
	for k, want := range map[string]string{"response_type": "code", "code_challenge_method": "S256", "prompt": "login", "client_id": stepClient, "max_age": "300"} {
		if q.Get(k) != want {
			t.Errorf("authorize %s = %q, want %q", k, q.Get(k), want)
		}
	}
	if q.Get("state") == "" || q.Get("nonce") == "" || q.Get("code_challenge") == "" || q.Get("state") == q.Get("nonce") {
		t.Errorf("state/nonce/challenge missing: %v", q)
	}
	if rec = p.do("/human/api/devices", step); rec.Code != 200 {
		t.Fatalf("with step-up: %d %s", rec.Code, rec.Body)
	}
	// whoami's step_up_until comes from the principal
	r := httptest.NewRequest("GET", "/api/me", nil)
	r.AddCookie(step)
	var seen Principal
	c, s := cfg("proxy")
	Middleware(c, s, Verifiers{Tokens: fakeTokens{}, Sessions: fakeSessions{}, StepUp: p.su})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = FromContext(r.Context()) })).
		ServeHTTP(httptest.NewRecorder(), withEdge(r))
	if u := StepUpUntil(seen); u == nil || !u.After(p.clk.Now()) {
		t.Fatalf("StepUpUntil = %v", u)
	}

	// the window closes
	p.clk.Advance(stepUpWindow + time.Second)
	if rec = p.do("/human/api/devices", step); rec.Code != 403 {
		t.Fatalf("after the window: %d", rec.Code)
	}
}

func withEdge(r *http.Request) *http.Request {
	r.RemoteAddr = "10.0.0.1:1"
	r.Header.Set("Remote-User", "alice")
	r.Header.Set("X-MM-Edge", edge)
	return r
}

func TestProxyStepUp_IDTokenRefusals(t *testing.T) {
	t.Parallel()
	for name, mod := range map[string]func(*fakeIDP){
		"stale auth_time":      func(f *fakeIDP) { f.authAge = 20 * time.Minute },
		"no second factor amr": func(f *fakeIDP) { f.amr = []string{"pwd"} },
		"missing amr":          func(f *fakeIDP) { f.amr = nil },
		"wrong nonce":          func(f *fakeIDP) { f.nonceOver = "someone-elses-nonce" },
		"wrong user":           func(f *fakeIDP) { f.user = "mallory" },
		"wrong audience":       func(f *fakeIDP) { f.aud = "another-client" },
		"bad signature":        func(f *fakeIDP) { f.badSig = true },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := newProxyStepRig(t, true)
			mod(p.idp)
			cb := p.login(t, "/human/tokens")
			if cb.Code != http.StatusForbidden || cookieOf(cb, p.su.cookieName()) != nil {
				t.Fatalf("callback: %d cookie=%v", cb.Code, cookieOf(cb, p.su.cookieName()))
			}
			if rec := p.do("/human/api/devices"); rec.Code != 403 {
				t.Fatalf("still gated: %d", rec.Code)
			}
		})
	}
}

func TestProxyStepUp_CallbackBinding(t *testing.T) {
	t.Parallel()
	p := newProxyStepRig(t, true)
	start := p.do("/auth/step-up?next=/human/tokens")
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, _ := noRedirect.Get(start.Header().Get("Location"))
	resp.Body.Close()
	back, _ := url.Parse(resp.Header.Get("Location"))
	flow := cookieOf(start, flowCookieName)

	if rec := p.do(back.Path + "?" + back.RawQuery); rec.Code != 403 { // no flow cookie: login CSRF
		t.Errorf("without flow cookie: %d", rec.Code)
	}
	if rec := p.do(back.Path+"?code=code-1&state=forged", flow); rec.Code != 403 {
		t.Errorf("forged state: %d", rec.Code)
	}
	if rec := p.do(back.Path+"?error=access_denied&state="+back.Query().Get("state"), flow); rec.Code != 403 {
		t.Errorf("provider error: %d", rec.Code)
	}
	// the flow is single use: even the right request is dead now
	if rec := p.do(back.Path+"?"+back.RawQuery, flow); rec.Code != 403 {
		t.Errorf("replay: %d", rec.Code)
	}
}

func TestProxyStepUp_OtherPrincipalsUnaffected(t *testing.T) {
	t.Parallel()
	p := newProxyStepRig(t, true)
	// agents are not Via "proxy": no gate, and they are refused /human/ elsewhere
	r := httptest.NewRequest("GET", "/api/ping", nil)
	r.Header.Set("Authorization", "Bearer mmp_good")
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, r)
	if rec.Code != 200 {
		t.Fatalf("agent: %d", rec.Code)
	}
	// a cookie for another user does not open the gate
	cb := p.login(t, "/human/tokens")
	step := cookieOf(cb, p.su.cookieName())
	if step == nil {
		t.Fatal("no step-up cookie")
	}
	forged := *step
	forged.Value = p.su.sign("bob", p.clk.Now().Unix())
	if rec := p.do("/human/api/devices", &forged); rec.Code != 403 {
		t.Fatalf("cookie for another user: %d", rec.Code)
	}
	tampered := *step
	tampered.Value = step.Value[:len(step.Value)-1] + "0"
	if tampered.Value == step.Value {
		tampered.Value = step.Value[:len(step.Value)-1] + "1"
	}
	if rec := p.do("/human/api/devices", &tampered); rec.Code != 403 {
		t.Fatalf("tampered cookie: %d", rec.Code)
	}
}

// With the feature off, proxy mode behaves as before: the edge alone gates /human/ and /auth/step-up does not exist.
func TestProxyStepUp_DisabledUnchanged(t *testing.T) {
	t.Parallel()
	p := newProxyStepRig(t, false)
	if rec := p.do("/human/api/devices"); rec.Code != 200 {
		t.Fatalf("human api without the feature: %d", rec.Code)
	}
	if rec := p.do("/auth/step-up"); rec.Code != 404 {
		t.Fatalf("/auth/step-up without the feature: %d", rec.Code)
	}
	r := withEdge(httptest.NewRequest("GET", "/api/me", nil))
	var seen Principal
	c, s := cfg("proxy")
	Middleware(c, s, Verifiers{Tokens: fakeTokens{}, Sessions: fakeSessions{}})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = FromContext(r.Context()) })).ServeHTTP(httptest.NewRecorder(), r)
	if StepUpUntil(seen) != nil {
		t.Fatal("step_up_until must stay null without the feature")
	}
}

func TestProxyStepUp_Pages(t *testing.T) {
	t.Parallel()
	p := newProxyStepRig(t, true)
	if rec := p.do("/auth/step-up/done"); rec.Code != 403 || !strings.Contains(rec.Body.String(), "not confirmed") {
		t.Fatalf("done without step-up: %d %s", rec.Code, rec.Body)
	}
	cb := p.login(t, "/auth/step-up/done")
	if cb.Code != 303 || cb.Header().Get("Location") != "/auth/step-up/done" {
		t.Fatalf("callback to done: %d %q", cb.Code, cb.Header().Get("Location"))
	}
	step := cookieOf(cb, p.su.cookieName())
	if rec := p.do("/auth/step-up/done", step); rec.Code != 200 || !strings.Contains(rec.Body.String(), "done.js") {
		t.Fatalf("done with step-up: %d %s", rec.Code, rec.Body)
	}
	if rec := p.do("/auth/step-up/done.js"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "mm-stepup") {
		t.Fatalf("done.js: %d", rec.Code)
	}
	if rec := p.do("/auth/nope"); rec.Code != 404 {
		t.Fatalf("unknown /auth/ path: %d", rec.Code)
	}
	// only the operator behind the edge may use the pages
	for _, path := range []string{"/auth/step-up", "/auth/step-up/callback?code=x&state=y", "/auth/step-up/done"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer mmp_good")
		rec := httptest.NewRecorder()
		p.h.ServeHTTP(rec, r)
		if rec.Code != 403 {
			t.Errorf("agent on %s: %d", path, rec.Code)
		}
	}
}

func TestProxyStepUp_ProviderDown(t *testing.T) {
	t.Parallel()
	p := newProxyStepRig(t, true)
	p.idp.srv.Close()
	rec := p.do("/auth/step-up?next=/human/tokens")
	if rec.Code != http.StatusBadGateway || cookieOf(rec, flowCookieName) != nil {
		t.Fatalf("provider down: %d", rec.Code)
	}
}

func TestProxyStepUp_CookieParsing(t *testing.T) {
	t.Parallel()
	p := newProxyStepRig(t, true)
	now := p.clk.Now().Unix()
	for name, v := range map[string]string{
		"two parts":   "a.b",
		"bad number":  "YWxpY2U.x." + strings.Repeat("0", 64),
		"negative":    "YWxpY2U.-5." + strings.Repeat("0", 64),
		"forged mac":  p.su.sign("alice", now)[:len(p.su.sign("alice", now))-4] + "0000",
		"future":      p.su.sign("alice", now+3600),
		"stale":       p.su.sign("alice", now-int64(stepUpWindow.Seconds())-60),
		"empty value": "x",
	} {
		if _, fresh := p.su.Check(withCookie(p.su.cookieName(), v), "alice"); fresh {
			t.Errorf("%s accepted", name)
		}
	}
	if _, fresh := p.su.Check(withCookie(p.su.cookieName(), p.su.sign("alice", now)), "alice"); !fresh {
		t.Error("good cookie refused")
	}
	// https deployments use the __Host- prefix
	if got := NewProxyStepUp(nil, key, "https://mm.example.com", nil).cookieName(); got != "__Host-mm_stepup" {
		t.Errorf("secure cookie name = %q", got)
	}
}

func withCookie(name, value string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: name, Value: value})
	return r
}

func TestProxyStepUp_FlowStoreBounded(t *testing.T) {
	t.Parallel()
	p := newProxyStepRig(t, true)
	for i := 0; i < maxFlows+10; i++ {
		p.su.putFlow(strings.Repeat("s", i+1), stepFlow{expires: p.clk.Now().Add(time.Duration(i+1) * time.Minute)})
	}
	if n := len(p.su.flows); n > maxFlows {
		t.Fatalf("%d flows kept", n)
	}
	if _, ok := p.su.takeFlow("s"); ok {
		t.Error("the oldest flow should have been dropped")
	}
	p.clk.Advance(time.Hour)
	p.su.putFlow("fresh", stepFlow{expires: p.clk.Now().Add(time.Minute)})
	if len(p.su.flows) != 1 {
		t.Fatalf("expired flows not purged: %d", len(p.su.flows))
	}
}
