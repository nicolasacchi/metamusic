package oidctest_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/nicolasacchi/metamusic/internal/oidc/oidctest"
)

var update = flag.Bool("update", false, "rewrite golden files")

const (
	resource = "https://mm.example.com/mcp"
	redirect = "https://claude.ai/cb"
	verifier = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ0123456789"
)

var (
	allScopes = []string{"openid", "offline_access", "metamusic"}
	noRedir   = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
)

func newStub(t *testing.T, mut func(*oidctest.Options)) *oidctest.Provider {
	t.Helper()
	o := oidctest.Options{
		Clients: []oidctest.Client{
			{ID: "pub", AuthMethod: "none", RedirectURIs: []string{redirect}, Audience: []string{resource}, Scopes: allScopes},
			{ID: "post", Secret: "s3cret-post", AuthMethod: "client_secret_post", RedirectURIs: []string{redirect}, Audience: []string{resource}, Scopes: allScopes},
			{ID: "basic", Secret: "s3cret-basic", AuthMethod: "client_secret_basic", RedirectURIs: []string{redirect}, Audience: []string{resource}, Scopes: allScopes},
		},
		Users: map[string]string{"e2e": "subject-1"}, LoginAs: "e2e", UserClaim: true, IssParam: true,
		Now: func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) },
	}
	if mut != nil {
		mut(&o)
	}
	return oidctest.New(t, o)
}

func challenge(v string) string {
	s := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(s[:])
}

func authzURL(p *oidctest.Provider, client string, extra url.Values) string {
	q := url.Values{"response_type": {"code"}, "client_id": {client}, "redirect_uri": {redirect}, "scope": {"openid offline_access metamusic"},
		"state": {"st-1"}, "code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}, "resource": {resource}}
	for k, v := range extra {
		if len(v) == 0 {
			q.Del(k)
		} else {
			q[k] = v
		}
	}
	return p.URL + "/api/oidc/authorization?" + q.Encode()
}

// authorize returns the redirect Location query.
func authorize(t *testing.T, p *oidctest.Provider, client string, extra url.Values) (int, url.Values) {
	t.Helper()
	resp, err := noRedir.Get(authzURL(p, client, extra))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if loc == nil {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, loc.Query()
}

type tokenResp struct {
	Status int
	Body   map[string]any
}

func postToken(t *testing.T, p *oidctest.Provider, form url.Values, basicUser, basicPass string) tokenResp {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, p.URL+"/api/oidc/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if basicUser != "" {
		req.SetBasicAuth(basicUser, basicPass)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return tokenResp{resp.StatusCode, body}
}

func codeFor(t *testing.T, p *oidctest.Provider, client string, extra url.Values) string {
	t.Helper()
	st, q := authorize(t, p, client, extra)
	if st != http.StatusFound || q.Get("code") == "" {
		t.Fatalf("authorize: %d %v", st, q)
	}
	return q.Get("code")
}

func codeForm(client, code string) url.Values {
	return url.Values{"grant_type": {"authorization_code"}, "client_id": {client}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}}
}

func claimsOf(t *testing.T, tok string) (map[string]any, map[string]any) {
	t.Helper()
	parts := strings.Split(tok, ".")
	dec := func(s string) map[string]any {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return m
	}
	return dec(parts[0]), dec(parts[1])
}

func TestStub_MetadataGolden(t *testing.T) {
	t.Parallel()
	p := newStub(t, nil)
	var docs [2]string
	for i, path := range []string{"/.well-known/openid-configuration", "/.well-known/oauth-authorization-server"} {
		resp, err := http.Get(p.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		out, _ := json.MarshalIndent(m, "", "  ")
		docs[i] = strings.ReplaceAll(string(out), p.URL, "ISSUER") + "\n"
	}
	if docs[0] != docs[1] {
		t.Fatal("the two well-known documents differ")
	}
	golden := "testdata/metadata.golden.json"
	if *update {
		if err := os.WriteFile(golden, []byte(docs[0]), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != docs[0] {
		t.Fatalf("metadata differs from golden (run -update):\n%s", docs[0])
	}
}

func TestStub_PKCE(t *testing.T) {
	t.Parallel()
	p := newStub(t, nil)
	t.Run("required", func(t *testing.T) {
		_, q := authorize(t, p, "pub", url.Values{"code_challenge": nil, "code_challenge_method": nil})
		if q.Get("error") != "invalid_request" || q.Get("code") != "" {
			t.Fatalf("%v", q)
		}
	})
	t.Run("plain refused", func(t *testing.T) {
		_, q := authorize(t, p, "pub", url.Values{"code_challenge_method": {"plain"}})
		if q.Get("error") != "invalid_request" {
			t.Fatalf("%v", q)
		}
	})
	t.Run("verified", func(t *testing.T) {
		code := codeFor(t, p, "pub", nil)
		f := codeForm("pub", code)
		f.Set("code_verifier", strings.Repeat("x", 50))
		if r := postToken(t, p, f, "", ""); r.Status != 400 || r.Body["error"] != "invalid_grant" {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("missing verifier", func(t *testing.T) {
		code := codeFor(t, p, "pub", nil)
		f := codeForm("pub", code)
		f.Del("code_verifier")
		if r := postToken(t, p, f, "", ""); r.Body["error"] != "invalid_grant" {
			t.Fatalf("%+v", r)
		}
	})
}

func TestStub_RedirectExact(t *testing.T) {
	t.Parallel()
	p := newStub(t, nil)
	for _, bad := range []string{redirect + "/", redirect + "?x=1", "https://claude.ai/cb2", "http://claude.ai/cb"} {
		st, q := authorize(t, p, "pub", url.Values{"redirect_uri": {bad}})
		if st != http.StatusBadRequest || q != nil && q.Get("code") != "" {
			t.Fatalf("redirect %q: status %d %v (must not redirect to an unregistered URI)", bad, st, q)
		}
	}
	// token request must repeat the exact redirect_uri
	code := codeFor(t, p, "pub", nil)
	f := codeForm("pub", code)
	f.Set("redirect_uri", redirect+"/")
	if r := postToken(t, p, f, "", ""); r.Body["error"] != "invalid_grant" {
		t.Fatalf("%+v", r)
	}
}

func TestStub_ResourceOutsideAudience(t *testing.T) {
	t.Parallel()
	p := newStub(t, nil)
	_, q := authorize(t, p, "pub", url.Values{"resource": {"https://evil.example.com/mcp"}})
	if q.Get("error") != "invalid_target" || q.Get("state") != "st-1" {
		t.Fatalf("%v", q)
	}
	f := codeForm("pub", codeFor(t, p, "pub", nil))
	f.Set("resource", "https://evil.example.com/mcp")
	if r := postToken(t, p, f, "", ""); r.Body["error"] != "invalid_target" {
		t.Fatalf("%+v", r)
	}
	// equal and path-below are accepted; a resource-less request is accepted
	for _, ok := range []string{resource, resource + "/sub"} {
		if _, q := authorize(t, p, "pub", url.Values{"resource": {ok}}); q.Get("code") == "" {
			t.Fatalf("resource %q refused: %v", ok, q)
		}
	}
	if _, q := authorize(t, p, "pub", url.Values{"resource": nil}); q.Get("code") == "" {
		t.Fatalf("%v", q)
	}
}

func TestStub_ScopeSubset(t *testing.T) {
	t.Parallel()
	p := newStub(t, nil)
	_, q := authorize(t, p, "pub", url.Values{"scope": {"openid admin"}})
	if q.Get("error") != "invalid_scope" {
		t.Fatalf("%v", q)
	}
}

func TestStub_ClientAuthMethodEnforced(t *testing.T) {
	t.Parallel()
	p := newStub(t, nil)
	cases := []struct {
		name   string
		client string
		mut    func(url.Values)
		basic  [2]string
		ok     bool
	}{
		{"none ok", "pub", nil, [2]string{}, true},
		{"none with secret", "pub", func(f url.Values) { f.Set("client_secret", "x") }, [2]string{}, false},
		{"none via basic", "pub", nil, [2]string{"pub", "x"}, false},
		{"post ok", "post", func(f url.Values) { f.Set("client_secret", "s3cret-post") }, [2]string{}, true},
		{"post wrong secret", "post", func(f url.Values) { f.Set("client_secret", "nope") }, [2]string{}, false},
		{"post missing secret", "post", nil, [2]string{}, false},
		{"post via basic", "post", nil, [2]string{"post", "s3cret-post"}, false},
		{"basic ok", "basic", func(f url.Values) { f.Del("client_id") }, [2]string{"basic", "s3cret-basic"}, true},
		{"basic secret in body", "basic", func(f url.Values) { f.Set("client_secret", "s3cret-basic") }, [2]string{}, false},
		{"basic wrong secret", "basic", nil, [2]string{"basic", "nope"}, false},
		{"unknown client", "ghost", nil, [2]string{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var code string
			if c.client != "ghost" {
				code = codeFor(t, p, c.client, nil)
			}
			f := codeForm(c.client, code)
			if c.mut != nil {
				c.mut(f)
			}
			r := postToken(t, p, f, c.basic[0], c.basic[1])
			if c.ok != (r.Status == 200) {
				t.Fatalf("%+v, want ok=%v", r, c.ok)
			}
			if !c.ok && r.Body["error"] != "invalid_client" {
				t.Fatalf("error = %v, want invalid_client", r.Body["error"])
			}
		})
	}
}

func TestStub_TokenEndpointFormOnly(t *testing.T) {
	t.Parallel()
	p := newStub(t, nil)
	code := codeFor(t, p, "pub", nil)
	b, _ := json.Marshal(map[string]string{"grant_type": "authorization_code", "client_id": "pub", "code": code})
	resp, err := http.Post(p.URL+"/api/oidc/token", "application/json", strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestStub_CodeSingleUse(t *testing.T) {
	t.Parallel()
	p := newStub(t, nil)
	code := codeFor(t, p, "pub", nil)
	if r := postToken(t, p, codeForm("pub", code), "", ""); r.Status != 200 {
		t.Fatalf("%+v", r)
	}
	if r := postToken(t, p, codeForm("pub", code), "", ""); r.Body["error"] != "invalid_grant" {
		t.Fatalf("replay: %+v", r)
	}
	// a code is bound to its client
	other := codeFor(t, p, "pub", nil)
	f := codeForm("post", other)
	f.Set("client_secret", "s3cret-post")
	if r := postToken(t, p, f, "", ""); r.Body["error"] != "invalid_grant" {
		t.Fatalf("cross-client: %+v", r)
	}
}

func TestStub_RefreshOnlyWithOfflineAccess(t *testing.T) {
	t.Parallel()
	p := newStub(t, nil)
	with := postToken(t, p, codeForm("pub", codeFor(t, p, "pub", nil)), "", "")
	if with.Body["refresh_token"] == nil {
		t.Fatalf("no refresh token with offline_access: %+v", with)
	}
	without := postToken(t, p, codeForm("pub", codeFor(t, p, "pub", url.Values{"scope": {"openid metamusic"}})), "", "")
	if without.Status != 200 || without.Body["refresh_token"] != nil {
		t.Fatalf("refresh token without offline_access: %+v", without)
	}
}

func TestStub_RefreshRotationAndReuseDetection(t *testing.T) {
	t.Parallel()
	p := newStub(t, nil)
	first := postToken(t, p, codeForm("pub", codeFor(t, p, "pub", nil)), "", "")
	rt1 := first.Body["refresh_token"].(string)
	refresh := func(rt string) tokenResp {
		return postToken(t, p, url.Values{"grant_type": {"refresh_token"}, "client_id": {"pub"}, "refresh_token": {rt}}, "", "")
	}
	second := refresh(rt1)
	if second.Status != 200 {
		t.Fatalf("%+v", second)
	}
	rt2 := second.Body["refresh_token"].(string)
	if rt2 == rt1 {
		t.Fatal("refresh token not rotated")
	}
	// replay of the rotated-out token: invalid_grant, and the whole family (rt2) dies
	if r := refresh(rt1); r.Body["error"] != "invalid_grant" {
		t.Fatalf("reuse: %+v", r)
	}
	if r := refresh(rt2); r.Body["error"] != "invalid_grant" {
		t.Fatalf("family survived reuse: %+v", r)
	}
	// another family is untouched
	other := postToken(t, p, codeForm("pub", codeFor(t, p, "pub", nil)), "", "")
	if r := refresh(other.Body["refresh_token"].(string)); r.Status != 200 {
		t.Fatalf("%+v", r)
	}
	// wrong client
	f := url.Values{"grant_type": {"refresh_token"}, "client_id": {"post"}, "client_secret": {"s3cret-post"}, "refresh_token": {other.Body["refresh_token"].(string)}}
	if r := postToken(t, p, f, "", ""); r.Body["error"] != "invalid_grant" {
		t.Fatalf("cross-client refresh: %+v", r)
	}
}

func TestStub_RefreshExpires(t *testing.T) {
	t.Parallel()
	var now atomic.Int64
	now.Store(time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC).Unix())
	p := newStub(t, func(o *oidctest.Options) {
		o.Now = func() time.Time { return time.Unix(now.Load(), 0) }
		o.RefreshTTL = time.Hour
	})
	first := postToken(t, p, codeForm("pub", codeFor(t, p, "pub", nil)), "", "")
	now.Add(7200)
	r := postToken(t, p, url.Values{"grant_type": {"refresh_token"}, "client_id": {"pub"}, "refresh_token": {first.Body["refresh_token"].(string)}}, "", "")
	if r.Body["error"] != "invalid_grant" {
		t.Fatalf("%+v", r)
	}
}

func TestStub_IssOnRedirect(t *testing.T) {
	t.Parallel()
	p := newStub(t, nil)
	_, q := authorize(t, p, "pub", nil)
	if q.Get("iss") != p.URL || q.Get("state") != "st-1" || q.Get("code") == "" {
		t.Fatalf("%v", q)
	}
	_, q = authorize(t, p, "pub", url.Values{"scope": {"bogus"}})
	if q.Get("iss") != p.URL {
		t.Fatalf("error redirect lacks iss: %v", q)
	}
	off := newStub(t, func(o *oidctest.Options) { o.IssParam = false })
	_, q = authorize(t, off, "pub", nil)
	if q.Has("iss") {
		t.Fatalf("iss emitted with IssParam=false: %v", q)
	}
}

func TestStub_AccessTokenShape(t *testing.T) {
	t.Parallel()
	p := newStub(t, nil)
	r := postToken(t, p, codeForm("pub", codeFor(t, p, "pub", nil)), "", "")
	h, c := claimsOf(t, r.Body["access_token"].(string))
	if h["alg"] != "RS256" || h["typ"] != "at+jwt" || h["kid"] == "" {
		t.Fatalf("header %v", h)
	}
	aud, _ := c["aud"].([]any)
	if c["iss"] != p.URL || c["sub"] != "subject-1" || len(aud) != 1 || aud[0] != resource || c["client_id"] != "pub" ||
		c["scope"] != "openid offline_access metamusic" || c["jti"] == "" || c["mm_user"] != "e2e" {
		t.Fatalf("claims %v", c)
	}
	if c["exp"].(float64)-c["iat"].(float64) != 3600 {
		t.Fatalf("ttl %v", c)
	}
}

func TestStub_MMUserOnlyWithMetamusic(t *testing.T) {
	t.Parallel()
	p := newStub(t, nil)
	r := postToken(t, p, codeForm("pub", codeFor(t, p, "pub", url.Values{"scope": {"openid"}})), "", "")
	if _, c := claimsOf(t, r.Body["access_token"].(string)); c["mm_user"] != nil {
		t.Fatalf("mm_user without metamusic: %v", c)
	}
	off := newStub(t, func(o *oidctest.Options) { o.UserClaim = false })
	r = postToken(t, off, codeForm("pub", codeFor(t, off, "pub", nil)), "", "")
	if _, c := claimsOf(t, r.Body["access_token"].(string)); c["mm_user"] != nil {
		t.Fatalf("mm_user with UserClaim=false: %v", c)
	}
}

func TestStub_ScopeClaimVariants(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"scope", "scp", "both"} {
		p := newStub(t, func(o *oidctest.Options) { o.ScopeClaim = mode })
		_, c := claimsOf(t, p.Mint(t, "pub", "e2e", allScopes, nil))
		_, hasScope := c["scope"]
		_, hasScp := c["scp"]
		if hasScope != (mode != "scp") || hasScp != (mode != "scope") {
			t.Fatalf("%s: %v", mode, c)
		}
	}
}

func TestStub_Mutate(t *testing.T) {
	t.Parallel()
	p := newStub(t, func(o *oidctest.Options) {
		o.Mutate = func(h, c map[string]any) { h["typ"] = "JWT"; c["extra"] = "x" }
	})
	h, c := claimsOf(t, p.Mint(t, "pub", "e2e", allScopes, nil))
	if h["typ"] != "JWT" || c["extra"] != "x" {
		t.Fatal("Mutate not applied")
	}
}

func TestStub_KeyRotation(t *testing.T) {
	t.Parallel()
	p := newStub(t, nil)
	kids := func() []string {
		resp, err := http.Get(p.URL + "/jwks.json")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var set struct {
			Keys []struct{ Kid, Kty, Alg string }
		}
		_ = json.NewDecoder(resp.Body).Decode(&set)
		var out []string
		for _, k := range set.Keys {
			if k.Kty != "RSA" || k.Alg != "RS256" {
				t.Fatalf("key %+v", k)
			}
			out = append(out, k.Kid)
		}
		return out
	}
	h0, _ := claimsOf(t, p.Mint(t, "pub", "e2e", allScopes, nil))
	if k := kids(); len(k) != 1 || k[0] != h0["kid"] {
		t.Fatalf("%v %v", k, h0)
	}
	p.RotateKey()
	h1, _ := claimsOf(t, p.Mint(t, "pub", "e2e", allScopes, nil))
	if h1["kid"] == h0["kid"] || len(kids()) != 2 {
		t.Fatalf("not rotated: %v %v", h1, kids())
	}
	p.DropOldKeys()
	if k := kids(); len(k) != 1 || k[0] != h1["kid"] {
		t.Fatalf("%v", k)
	}
}

func TestStub_RequestsMasksSecrets(t *testing.T) {
	t.Parallel()
	p := newStub(t, nil)
	code := codeFor(t, p, "post", nil)
	f := codeForm("post", code)
	f.Set("client_secret", "s3cret-post")
	first := postToken(t, p, f, "", "")
	postToken(t, p, url.Values{"grant_type": {"refresh_token"}, "client_id": {"post"}, "client_secret": {"s3cret-post"},
		"refresh_token": {first.Body["refresh_token"].(string)}}, "", "")
	reqs := p.Requests()
	if len(reqs) != 3 {
		t.Fatalf("%d requests: %+v", len(reqs), reqs)
	}
	if reqs[0].Endpoint != "authorize" || reqs[0].ClientID != "post" || reqs[0].Resource != resource || !strings.Contains(reqs[0].Scope, "offline_access") {
		t.Fatalf("%+v", reqs[0])
	}
	if reqs[1].Endpoint != "token" || reqs[1].GrantType != "authorization_code" || reqs[2].GrantType != "refresh_token" {
		t.Fatalf("%+v", reqs)
	}
	b, _ := json.Marshal(reqs)
	for _, secret := range []string{"s3cret-post", code, first.Body["refresh_token"].(string), verifier} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("Requests() leaks a secret: %s", b)
		}
	}
	for _, r := range reqs {
		if r.At.IsZero() {
			t.Fatal("At unset")
		}
	}
}

func TestStub_FetcherDrivesAuthorizationFlow(t *testing.T) {
	t.Parallel()
	p := newStub(t, nil)
	fetch := p.Fetcher(t)
	res, err := fetch(t.Context(), &auth.AuthorizationArgs{URL: authzURL(p, "pub", nil)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Code == "" || res.State != "st-1" || res.Iss != p.URL {
		t.Fatalf("%+v", res)
	}
	if _, err := fetch(t.Context(), &auth.AuthorizationArgs{URL: authzURL(p, "pub", url.Values{"scope": {"bogus"}})}); err == nil {
		t.Fatal("authorization error not surfaced")
	}
}

func TestStub_MintDefaultsAudience(t *testing.T) {
	t.Parallel()
	p := newStub(t, nil)
	_, c := claimsOf(t, p.Mint(t, "pub", "e2e", allScopes, nil))
	if aud, _ := c["aud"].([]any); len(aud) != 1 || aud[0] != resource {
		t.Fatalf("%v", c)
	}
	_, c = claimsOf(t, p.Mint(t, "pub", "e2e", allScopes, []string{"x"}))
	if aud, _ := c["aud"].([]any); len(aud) != 1 || aud[0] != "x" {
		t.Fatalf("%v", c)
	}
}
