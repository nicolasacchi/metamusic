package oidc_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/oidc"
	"github.com/nicolasacchi/metamusic/internal/oidc/oidctest"
)

const (
	testResource = "https://mm.example.com/mcp"
	testClient   = "test-claude"
	testUser     = "e2e"
	testSubject  = "subject-e2e-0001"
)

var fixedNow = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

// countingTransport counts requests per URL path.
type countingTransport struct {
	mu    sync.Mutex
	paths map[string]int
	base  http.RoundTripper
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.paths[r.URL.Path]++
	c.mu.Unlock()
	return c.base.RoundTrip(r)
}

func (c *countingTransport) count(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.paths[path]
}

type rig struct {
	p   *oidctest.Provider
	v   *oidc.Verifier
	ct  *countingTransport
	now *atomic.Int64 // unix seconds, the rig clock
}

func (r *rig) jwksFetches() int { return r.ct.count("/jwks.json") }

func (r *rig) setNow(t time.Time) { r.now.Store(t.Unix()) }

type rigOpts struct {
	mutate     func(header, claims map[string]any)
	scopeClaim string
	userClaim  *bool
	jwksURL    string // "" => the stub's
	discover   bool   // leave JWKSURL empty
}

func newRig(t *testing.T, o rigOpts) *rig {
	t.Helper()
	now := &atomic.Int64{}
	now.Store(fixedNow.Unix())
	clock := func() time.Time { return time.Unix(now.Load(), 0) }
	uc := true
	if o.userClaim != nil {
		uc = *o.userClaim
	}
	scopes := []string{"openid", "offline_access", "metamusic"}
	p := oidctest.New(t, oidctest.Options{
		Clients: []oidctest.Client{
			{ID: testClient, AuthMethod: "none", RedirectURIs: []string{"https://claude.ai/cb"}, Audience: []string{testResource}, Scopes: scopes},
			{ID: "other", AuthMethod: "none", RedirectURIs: []string{"https://x.example.com/cb"}, Audience: []string{testResource}, Scopes: scopes},
		},
		Users: map[string]string{testUser: testSubject}, LoginAs: testUser, Now: clock,
		UserClaim: uc, IssParam: true, ScopeClaim: o.scopeClaim, Mutate: o.mutate,
	})
	ct := &countingTransport{paths: map[string]int{}, base: http.DefaultTransport}
	jwks := p.URL + "/jwks.json"
	if o.jwksURL != "" {
		jwks = o.jwksURL
	}
	if o.discover {
		jwks = ""
	}
	v, err := oidc.NewVerifier(oidc.Config{Issuer: p.URL, JWKSURL: jwks, Resource: testResource,
		Clients: map[string]string{testClient: "Claude Test"}}, &http.Client{Transport: ct}, clock)
	if err != nil {
		t.Fatal(err)
	}
	return &rig{p: p, v: v, ct: ct, now: now}
}

func (r *rig) mint(t *testing.T) string {
	t.Helper()
	return r.p.Mint(t, testClient, testUser, []string{"openid", "offline_access", "metamusic"}, nil)
}

func TestVerify_Table(t *testing.T) {
	t.Parallel()
	nowU := fixedNow.Unix()
	claim := func(k string, v any) func(h, c map[string]any) {
		return func(_, c map[string]any) {
			if v == nil {
				delete(c, k)
			} else {
				c[k] = v
			}
		}
	}
	header := func(k string, v any) func(h, c map[string]any) {
		return func(h, _ map[string]any) {
			if v == nil {
				delete(h, k)
			} else {
				h[k] = v
			}
		}
	}
	type want struct {
		err      error
		missing  bool // *MissingScopeError
		noFetch  bool
		okClaims bool
	}
	rows := []struct {
		name   string
		mutate func(h, c map[string]any)
		scp    string
		noUser bool
		tweak  func(tok string) string
		want   want
	}{
		{name: "valid", want: want{okClaims: true}},
		{name: "alg none", mutate: header("alg", "none"), want: want{err: oidc.ErrInvalid, noFetch: true}},
		{name: "alg ES256", mutate: header("alg", "ES256"), want: want{err: oidc.ErrInvalid, noFetch: true}},
		{name: "alg PS256", mutate: header("alg", "PS256"), want: want{err: oidc.ErrInvalid, noFetch: true}},
		{name: "alg RS512", mutate: header("alg", "RS512"), want: want{err: oidc.ErrInvalid, noFetch: true}},
		{name: "alg lowercase", mutate: header("alg", "rs256"), want: want{err: oidc.ErrInvalid, noFetch: true}},
		{name: "typ missing", mutate: header("typ", nil), want: want{err: oidc.ErrInvalid, noFetch: true}},
		{name: "typ JWT", mutate: header("typ", "JWT"), want: want{err: oidc.ErrInvalid, noFetch: true}},
		{name: "typ AT+JWT", mutate: header("typ", "AT+JWT"), want: want{okClaims: true}},
		{name: "typ application/at+jwt", mutate: header("typ", "application/at+jwt"), want: want{okClaims: true}},
		{name: "kid empty", mutate: header("kid", ""), want: want{err: oidc.ErrInvalid, noFetch: true}},
		{name: "crit", mutate: header("crit", []string{"exp"}), want: want{err: oidc.ErrInvalid, noFetch: true}},
		{name: "flipped signature bit", tweak: func(tok string) string {
			i := strings.LastIndex(tok, ".") + 5
			b := []byte(tok)
			b[i] ^= 1
			return string(b)
		}, want: want{err: oidc.ErrInvalid}},
		{name: "non-canonical signature encoding", tweak: nonCanonicalSig, want: want{err: oidc.ErrInvalid}},
		{name: "iss trailing slash", mutate: func(_, c map[string]any) { c["iss"] = c["iss"].(string) + "/" }, want: want{err: oidc.ErrInvalid}},
		{name: "aud missing", mutate: claim("aud", nil), want: want{err: oidc.ErrInvalid}},
		{name: "aud trailing slash", mutate: claim("aud", []string{testResource + "/"}), want: want{err: oidc.ErrInvalid}},
		{name: "aud string", mutate: claim("aud", testResource), want: want{okClaims: true}},
		{name: "aud other first", mutate: claim("aud", []string{"other", testResource}), want: want{okClaims: true}},
		{name: "exp -31s", mutate: claim("exp", nowU-31), want: want{err: oidc.ErrInvalid}},
		{name: "exp -29s", mutate: claim("exp", nowU-29), want: want{okClaims: true}},
		{name: "exp missing", mutate: claim("exp", nil), want: want{err: oidc.ErrInvalid}},
		{name: "iat +31s", mutate: claim("iat", nowU+31), want: want{err: oidc.ErrInvalid}},
		{name: "iat +29s", mutate: claim("iat", nowU+29), want: want{okClaims: true}},
		{name: "iat missing", mutate: claim("iat", nil), want: want{err: oidc.ErrInvalid}},
		{name: "nbf +31s", mutate: claim("nbf", nowU+31), want: want{err: oidc.ErrInvalid}},
		{name: "nbf past", mutate: claim("nbf", nowU-5), want: want{okClaims: true}},
		{name: "sub empty", mutate: claim("sub", ""), want: want{err: oidc.ErrInvalid}},
		{name: "sub too long", mutate: claim("sub", strings.Repeat("s", 256)), want: want{err: oidc.ErrInvalid}},
		{name: "client_id missing", mutate: claim("client_id", nil), want: want{err: oidc.ErrInvalid}},
		{name: "client unknown", mutate: claim("client_id", "other"), want: want{err: oidc.ErrClientUnknown}},
		{name: "client unknown beats scope", mutate: func(_, c map[string]any) { c["client_id"] = "other"; c["scope"] = "openid" }, want: want{err: oidc.ErrClientUnknown}},
		{name: "scope lacks metamusic", mutate: claim("scope", "openid offline_access"), want: want{missing: true}},
		{name: "scp only", mutate: func(_, c map[string]any) { delete(c, "scope"); c["scp"] = []string{"metamusic"} }, want: want{okClaims: true}},
		{name: "scp plus scope deduplicated", scp: "both", want: want{okClaims: true}},
		{name: "no mm_user", noUser: true, want: want{okClaims: true}},
		{name: "oversize token", tweak: func(string) string { return strings.Repeat("a", 9000) }, want: want{err: oidc.ErrInvalid, noFetch: true}},
		{name: "four segments", tweak: func(tok string) string { return tok + ".AAAA" }, want: want{err: oidc.ErrInvalid, noFetch: true}},
		{name: "padded base64", tweak: func(tok string) string { return tok + "=" }, want: want{err: oidc.ErrInvalid, noFetch: true}},
		{name: "empty", tweak: func(string) string { return "" }, want: want{err: oidc.ErrInvalid, noFetch: true}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			uc := !row.noUser
			r := newRig(t, rigOpts{mutate: row.mutate, scopeClaim: row.scp, userClaim: &uc})
			tok := r.mint(t)
			if row.tweak != nil {
				tok = row.tweak(tok)
			}
			got, err := r.v.Verify(t.Context(), tok)
			switch {
			case row.want.okClaims:
				if err != nil {
					t.Fatalf("Verify: %v", err)
				}
				if got.Subject != testSubject || got.ClientID != testClient || !slices.Contains(got.Audience, testResource) {
					t.Fatalf("claims %+v", got)
				}
				wantUser := testUser
				if row.noUser {
					wantUser = ""
				}
				if got.User != wantUser {
					t.Fatalf("User = %q, want %q", got.User, wantUser)
				}
				if diff := cmp.Diff([]string{"openid", "offline_access", "metamusic"}, got.Scopes); diff != "" && row.mutate == nil {
					t.Fatalf("scopes (-want +got):\n%s", diff)
				}
				if got.Expires.IsZero() || got.IssuedAt.IsZero() || got.JTI == "" {
					t.Fatalf("claims %+v", got)
				}
			case row.want.missing:
				var ms *oidc.MissingScopeError
				if !errors.As(err, &ms) || ms.Scope != "metamusic" {
					t.Fatalf("err = %v, want MissingScopeError", err)
				}
			default:
				if !errors.Is(err, row.want.err) {
					t.Fatalf("err = %v, want %v", err, row.want.err)
				}
				var ms *oidc.MissingScopeError
				if errors.As(err, &ms) {
					t.Fatalf("unexpected MissingScopeError: %v", err)
				}
			}
			if row.want.noFetch && r.jwksFetches() != 0 {
				t.Fatalf("%d JWKS fetches, want 0", r.jwksFetches())
			}
		})
	}
}

func nonCanonicalSig(tok string) string {
	i := strings.LastIndex(tok, ".")
	sig := tok[i+1:]
	const alpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := strings.IndexByte(alpha, sig[len(sig)-1])
	// a 256-byte signature leaves 4 padding bits in the last character: set one
	return tok[:i+1] + sig[:len(sig)-1] + string(alpha[last|1])
}

func TestVerify_RS256SignatureStillChecked(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	tok := r.mint(t)
	// a token signed by another provider's key but with a kid of ours is refused
	other := newRig(t, rigOpts{})
	alien := other.mint(t)
	parts := strings.Split(alien, ".")
	ours := strings.Split(tok, ".")
	forged := strings.Join([]string{ours[0], parts[1], parts[2]}, ".")
	if _, err := r.v.Verify(t.Context(), forged); !errors.Is(err, oidc.ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}

// TestVerify_HS256WithPublicKey: the classic confusion attack (HMAC keyed with the RSA public key bytes).
func TestVerify_HS256WithPublicKey(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	// fetch the published key
	resp, err := http.Get(r.p.URL + "/jwks.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var set struct{ Keys []struct{ Kid, N string } }
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		t.Fatal(err)
	}
	hdr := b64(`{"alg":"HS256","typ":"at+jwt","kid":"` + set.Keys[0].Kid + `"}`)
	body := b64(`{"iss":"` + r.p.URL + `","sub":"s","aud":["` + testResource + `"],"client_id":"` + testClient + `","scope":"metamusic","iat":` + itoa(fixedNow.Unix()) + `,"exp":` + itoa(fixedNow.Unix()+600) + `}`)
	nb, _ := base64.RawURLEncoding.DecodeString(set.Keys[0].N)
	for _, secret := range [][]byte{nb, []byte(set.Keys[0].N), []byte("x")} {
		mac := hmac.New(sha256.New, secret)
		mac.Write([]byte(hdr + "." + body))
		tok := hdr + "." + body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
		if _, err := r.v.Verify(t.Context(), tok); !errors.Is(err, oidc.ErrInvalid) {
			t.Fatalf("err = %v", err)
		}
	}
	if r.jwksFetches() != 0 {
		t.Fatalf("fetches = %d", r.jwksFetches())
	}
}

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestVerify_Rotation(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	ctx := t.Context()
	if _, err := r.v.Verify(ctx, r.mint(t)); err != nil {
		t.Fatal(err)
	}
	if r.jwksFetches() != 1 {
		t.Fatalf("fetches = %d, want 1", r.jwksFetches())
	}
	r.p.RotateKey()
	if _, err := r.v.Verify(ctx, r.mint(t)); err != nil {
		t.Fatalf("rotated token: %v", err)
	}
	if r.jwksFetches() != 2 {
		t.Fatalf("fetches = %d, want exactly one refresh (2)", r.jwksFetches())
	}
	// old keys still published: old and new verify without more fetches
	if _, err := r.v.Verify(ctx, r.mint(t)); err != nil || r.jwksFetches() != 2 {
		t.Fatalf("err %v fetches %d", err, r.jwksFetches())
	}
}

func TestVerify_UnknownKidRateLimited(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{mutate: func(h, _ map[string]any) { h["kid"] = "ghost" }})
	ctx := t.Context()
	// warm the cache with a verifier-visible kid through a second provider-free path: prefetch
	r.v.Prefetch(ctx)
	base := r.jwksFetches()
	for range 3 {
		if _, err := r.v.Verify(ctx, r.mint(t)); !errors.Is(err, oidc.ErrInvalid) {
			t.Fatalf("err = %v", err)
		}
	}
	if got := r.jwksFetches() - base; got != 1 {
		t.Fatalf("%d refreshes for repeated unknown kid, want 1", got)
	}
	r.setNow(fixedNow.Add(61 * time.Second))
	if _, err := r.v.Verify(ctx, r.mint(t)); !errors.Is(err, oidc.ErrInvalid) {
		t.Fatal(err)
	}
	if got := r.jwksFetches() - base; got != 2 {
		t.Fatalf("%d refreshes after 61 s, want 2", got)
	}
}

func TestVerify_HeaderKeyMaterialIgnored(t *testing.T) {
	t.Parallel()
	var jwkServed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/attacker" {
			jwkServed.Store(true)
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	r := newRig(t, rigOpts{mutate: func(h, _ map[string]any) {
		h["kid"] = "unknown"
		h["jku"] = srv.URL + "/attacker"
		h["x5u"] = srv.URL + "/attacker"
		h["jwk"] = map[string]any{"kty": "RSA", "kid": "unknown"}
		h["x5c"] = []string{"AAAA"}
	}})
	if _, err := r.v.Verify(t.Context(), r.mint(t)); !errors.Is(err, oidc.ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
	if jwkServed.Load() {
		t.Fatal("jku/x5u was fetched")
	}
}

func TestVerify_UnavailableWithoutKeys(t *testing.T) {
	t.Parallel()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", 500) }))
	t.Cleanup(bad.Close)
	r := newRig(t, rigOpts{jwksURL: bad.URL + "/jwks.json"})
	if _, err := r.v.Verify(t.Context(), r.mint(t)); !errors.Is(err, oidc.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestVerify_ErrorsCarryNoToken(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	tok := r.mint(t)
	bad := tok[:len(tok)-4] + "AAAA"
	_, err := r.v.Verify(t.Context(), bad)
	if err == nil || strings.Contains(err.Error(), bad) || strings.Contains(err.Error(), strings.Split(tok, ".")[1]) {
		t.Fatalf("error leaks token content: %v", err)
	}
}
