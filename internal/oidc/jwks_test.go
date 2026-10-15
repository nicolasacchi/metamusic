package oidc_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/oidc"
)

// jwksProxy serves the stub's JWKS (or an override) with switchable faults.
type jwksProxy struct {
	*httptest.Server
	mode atomic.Value // func(w, r) or nil
	hits atomic.Int64
}

func newJWKSProxy(t *testing.T, upstream string) *jwksProxy {
	t.Helper()
	p := &jwksProxy{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.hits.Add(1)
		if m, _ := p.mode.Load().(func(http.ResponseWriter, *http.Request)); m != nil {
			m(w, r)
			return
		}
		resp, err := http.Get(upstream + "/jwks.json")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *jwksProxy) setMode(f func(http.ResponseWriter, *http.Request)) { p.mode.Store(f) }

func TestJWKS_RefreshRateLimited(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{mutate: func(h, _ map[string]any) { h["kid"] = "ghost" }})
	tok := r.mint(t)
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			if _, err := r.v.Verify(t.Context(), tok); !errors.Is(err, oidc.ErrInvalid) {
				t.Errorf("err = %v", err)
			}
		})
	}
	wg.Wait()
	if got := r.jwksFetches(); got != 1 {
		t.Fatalf("%d JWKS fetches for 100 concurrent unknown-kid verifies, want 1", got)
	}
}

func TestJWKS_RefreshAfterAnHour(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	if _, err := r.v.Verify(t.Context(), r.mint(t)); err != nil {
		t.Fatal(err)
	}
	r.setNow(fixedNow.Add(30 * time.Minute))
	if _, err := r.v.Verify(t.Context(), r.mint(t)); err != nil || r.jwksFetches() != 1 {
		t.Fatalf("err %v fetches %d", err, r.jwksFetches())
	}
	r.setNow(fixedNow.Add(61 * time.Minute))
	if _, err := r.v.Verify(t.Context(), r.mint(t)); err != nil || r.jwksFetches() != 2 {
		t.Fatalf("err %v fetches %d, want a refresh after 1 h", err, r.jwksFetches())
	}
}

func TestJWKS_StaleKeysUpTo24h(t *testing.T) {
	t.Parallel()
	base := newRig(t, rigOpts{})
	proxy := newJWKSProxy(t, base.p.URL)
	r := newRig(t, rigOpts{jwksURL: proxy.URL + "/jwks.json"})
	// the proxy fronts a different provider; use the base provider for tokens instead
	_ = r
	ct := &countingTransport{paths: map[string]int{}, base: http.DefaultTransport}
	v, err := oidc.NewVerifier(oidc.Config{Issuer: base.p.URL, JWKSURL: proxy.URL + "/jwks.json", Resource: testResource,
		Clients: map[string]string{testClient: "x"}}, &http.Client{Transport: ct}, func() time.Time { return time.Unix(base.now.Load(), 0) })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(t.Context(), base.mint(t)); err != nil {
		t.Fatal(err)
	}
	proxy.setMode(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", 500) })
	base.setNow(fixedNow.Add(2 * time.Hour))
	if _, err := v.Verify(t.Context(), base.mint(t)); err != nil {
		t.Fatalf("stale keys must still verify at +2h: %v", err)
	}
	if got := v.Status().LastError; got != "jwks_http_5xx" {
		t.Fatalf("LastError = %q", got)
	}
	base.setNow(fixedNow.Add(23 * time.Hour))
	if _, err := v.Verify(t.Context(), base.mint(t)); err != nil {
		t.Fatalf("stale keys must still verify at +23h: %v", err)
	}
	base.setNow(fixedNow.Add(25 * time.Hour))
	if _, err := v.Verify(t.Context(), base.mint(t)); !errors.Is(err, oidc.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable past 24 h", err)
	}
	proxy.setMode(nil)
	base.setNow(fixedNow.Add(26 * time.Hour))
	if _, err := v.Verify(t.Context(), base.mint(t)); err != nil {
		t.Fatalf("recovery: %v", err)
	}
}

func TestJWKS_Limits(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		mode func(http.ResponseWriter, *http.Request)
		code string
	}{
		{"oversize", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"keys":[],"pad":"` + strings.Repeat("a", 65*1024) + `"}`))
		}, "jwks_too_large"},
		{"redirect", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/elsewhere", http.StatusFound) }, "jwks_http_302"},
		{"500", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "x", 500) }, "jwks_http_5xx"},
		{"not json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("nope")) }, "jwks_parse"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			base := newRig(t, rigOpts{})
			proxy := newJWKSProxy(t, base.p.URL)
			proxy.setMode(c.mode)
			v, _ := oidc.NewVerifier(oidc.Config{Issuer: base.p.URL, JWKSURL: proxy.URL + "/jwks.json", Resource: testResource}, nil, nil)
			v.Prefetch(t.Context())
			if s := v.Status(); s.Keys != 0 || s.LastError != c.code {
				t.Fatalf("status %+v, want 0 keys and %q", s, c.code)
			}
		})
	}
	t.Run("http non-loopback refused", func(t *testing.T) {
		t.Parallel()
		ct := &countingTransport{paths: map[string]int{}, base: http.DefaultTransport}
		v, _ := oidc.NewVerifier(oidc.Config{Issuer: "http://auth.example.com", JWKSURL: "http://auth.example.com/jwks.json", Resource: testResource},
			&http.Client{Transport: ct}, nil)
		v.Prefetch(t.Context())
		if s := v.Status(); s.Keys != 0 || s.LastError != "jwks_url_refused" {
			t.Fatalf("status %+v", s)
		}
		if ct.count("/jwks.json") != 0 {
			t.Fatal("a request was made to a non-loopback http URL")
		}
	})
}

func TestJWKS_WeakKeysSkipped(t *testing.T) {
	t.Parallel()
	base := newRig(t, rigOpts{})
	var good struct{ Keys []map[string]string }
	resp, err := http.Get(base.p.URL + "/jwks.json")
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewDecoder(resp.Body).Decode(&good)
	_ = resp.Body.Close()
	k1024, _ := rsa.GenerateKey(rand.Reader, 1024)
	enc := func(n *big.Int) string { return base64.RawURLEncoding.EncodeToString(n.Bytes()) }
	weak := map[string]string{"kty": "RSA", "kid": "weak1024", "n": enc(k1024.N), "e": "AQAB"}
	e3 := map[string]string{"kty": "RSA", "kid": "e3", "n": good.Keys[0]["n"], "e": enc(big.NewInt(3))}
	ec := map[string]string{"kty": "EC", "kid": "ec1", "crv": "P-256", "x": "AAAA", "y": "AAAA"}
	bad := map[string]string{"kty": "RSA", "kid": "enc", "use": "enc", "n": good.Keys[0]["n"], "e": "AQAB"}
	proxy := newJWKSProxy(t, base.p.URL)
	proxy.setMode(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{weak, e3, ec, bad, good.Keys[0]}})
	})
	v, _ := oidc.NewVerifier(oidc.Config{Issuer: base.p.URL, JWKSURL: proxy.URL, Resource: testResource}, nil, nil)
	v.Prefetch(t.Context())
	if s := v.Status(); s.Keys != 1 || s.LastError != "" {
		t.Fatalf("status %+v, want exactly the good key and no error", s)
	}
}

func TestVerifier_PrefetchStatus(t *testing.T) {
	t.Parallel()
	base := newRig(t, rigOpts{})
	proxy := newJWKSProxy(t, base.p.URL)
	v, _ := oidc.NewVerifier(oidc.Config{Issuer: base.p.URL, JWKSURL: proxy.URL, Resource: testResource,
		Clients: map[string]string{testClient: "x"}}, nil, func() time.Time { return time.Unix(base.now.Load(), 0) })
	if s := v.Status(); s.Keys != 0 || s.FetchedAt != nil {
		t.Fatalf("before prefetch: %+v", s)
	}
	v.Prefetch(t.Context())
	if s := v.Status(); s.Keys < 1 || s.FetchedAt == nil || s.LastError != "" {
		t.Fatalf("after prefetch: %+v", s)
	}
	if proxy.hits.Load() != 1 {
		t.Fatalf("hits = %d", proxy.hits.Load())
	}

	// a failing JWKS: zero keys, a code, no panic; a later Verify retries
	failing := newJWKSProxy(t, base.p.URL)
	failing.setMode(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) })
	v2, _ := oidc.NewVerifier(oidc.Config{Issuer: base.p.URL, JWKSURL: failing.URL, Resource: testResource,
		Clients: map[string]string{testClient: "x"}}, nil, func() time.Time { return time.Unix(base.now.Load(), 0) })
	v2.Prefetch(t.Context())
	if s := v2.Status(); s.Keys != 0 || s.LastError != "jwks_http_5xx" {
		t.Fatalf("failing: %+v", s)
	}
	failing.setMode(nil)
	if _, err := v2.Verify(t.Context(), base.mint(t)); err != nil {
		t.Fatalf("Verify after recovery: %v", err)
	}
	if s := v2.Status(); s.Keys < 1 || s.LastError != "" {
		t.Fatalf("recovered: %+v", s)
	}
}

func TestDiscovery_ResolvesJWKSURL(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{discover: true})
	for range 3 {
		if _, err := r.v.Verify(t.Context(), r.mint(t)); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.ct.count("/.well-known/openid-configuration"); got != 1 {
		t.Fatalf("discovery fetched %d times, want once", got)
	}
}

func TestDiscovery_IssuerMismatch(t *testing.T) {
	t.Parallel()
	base := newRig(t, rigOpts{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": "https://evil.example.com", "jwks_uri": base.p.URL + "/jwks.json"})
	}))
	t.Cleanup(srv.Close)
	v, _ := oidc.NewVerifier(oidc.Config{Issuer: srv.URL, Resource: testResource, Clients: map[string]string{testClient: "x"}}, nil,
		func() time.Time { return time.Unix(base.now.Load(), 0) })
	if _, err := v.Verify(t.Context(), base.mint(t)); !errors.Is(err, oidc.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if s := v.Status(); s.LastError != "issuer_mismatch" || s.Keys != 0 {
		t.Fatalf("status %+v", s)
	}
}
