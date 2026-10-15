package oidc_test

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/oidc"
)

func TestFetchURLPolicy(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ url, code string }{
		{"ftp://auth.example.com/jwks", "jwks_url_refused"},
		{"not a url", "jwks_url_refused"},
		{"http://auth.example.com/jwks", "jwks_url_refused"},
		{"http://10.0.0.1/jwks", "jwks_url_refused"},
		{"http://localhost:1/jwks", "jwks_transport"}, // loopback is allowed; nothing listens
		{"http://127.0.0.1:1/jwks", "jwks_transport"},
		{"http://[::1]:1/jwks", "jwks_transport"},
		{"http://localhost:1/jwks\x7f", "jwks_url_refused"},
	} {
		v, _ := oidc.NewVerifier(oidc.Config{Issuer: "https://auth.example.com", JWKSURL: c.url, Resource: testResource}, nil, nil)
		v.Prefetch(t.Context())
		if got := v.Status().LastError; got != c.code {
			t.Errorf("%q: LastError %q, want %q", c.url, got, c.code)
		}
	}
}

func TestDiscovery_Failures(t *testing.T) {
	t.Parallel()
	for name, h := range map[string]http.HandlerFunc{
		"404":         func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) },
		"not json":    func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("x")) },
		"no jwks_uri": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"issuer":"x"}`)) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(h)
			t.Cleanup(srv.Close)
			v, _ := oidc.NewVerifier(oidc.Config{Issuer: srv.URL + "/", Resource: testResource}, nil, nil)
			v.Prefetch(t.Context())
			if s := v.Status(); s.Keys != 0 || !strings.HasPrefix(s.LastError, "discovery_") {
				t.Fatalf("%+v", s)
			}
		})
	}
}

func TestVerify_MalformedParts(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	good := strings.Split(r.mint(t), ".")
	for name, tok := range map[string]string{
		"header not json":  enc("nope") + "." + good[1] + "." + good[2],
		"header json null": enc("null") + "." + good[1] + "." + good[2],
		"bad base64":       "!!!." + good[1] + "." + good[2],
		"empty segment":    good[0] + ".." + good[2],
		"typ not a string": enc(`{"alg":"RS256","typ":5,"kid":"k"}`) + "." + good[1] + "." + good[2],
		"kid not a string": enc(`{"alg":"RS256","typ":"at+jwt","kid":5}`) + "." + good[1] + "." + good[2],
		"alg not a string": enc(`{"alg":5,"typ":"at+jwt","kid":"k"}`) + "." + good[1] + "." + good[2],
	} {
		if _, err := r.v.Verify(t.Context(), tok); !errors.Is(err, oidc.ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if r.jwksFetches() != 0 {
		t.Fatalf("fetches = %d, want 0 for header failures", r.jwksFetches())
	}
	if _, err := r.v.Verify(t.Context(), good[0]+"."+enc("nope")+"."+good[2]); !errors.Is(err, oidc.ErrInvalid) {
		t.Fatalf("claims not json: %v", err)
	}
}

func TestVerify_ClaimTypeConfusion(t *testing.T) {
	t.Parallel()
	for name, mut := range map[string]func(h, c map[string]any){
		"aud number":       func(_, c map[string]any) { c["aud"] = 5 },
		"aud empty array":  func(_, c map[string]any) { c["aud"] = []string{} },
		"aud mixed array":  func(_, c map[string]any) { c["aud"] = []any{testResource, 5} },
		"aud empty string": func(_, c map[string]any) { c["aud"] = "" },
		"exp string":       func(_, c map[string]any) { c["exp"] = "9999999999" },
		"exp negative":     func(_, c map[string]any) { c["exp"] = -1 },
		"exp absurd":       func(_, c map[string]any) { c["exp"] = 1e18 },
		"nbf string":       func(_, c map[string]any) { c["nbf"] = "x" },
		"iss number":       func(_, c map[string]any) { c["iss"] = 7 },
		"sub number":       func(_, c map[string]any) { c["sub"] = 7 },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, rigOpts{mutate: mut})
			if _, err := r.v.Verify(t.Context(), r.mint(t)); !errors.Is(err, oidc.ErrInvalid) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestVerify_ScopeSpellings(t *testing.T) {
	t.Parallel()
	for name, mut := range map[string]func(h, c map[string]any){
		"scp string": func(_, c map[string]any) { delete(c, "scope"); c["scp"] = "openid metamusic" },
		"scp junk entries": func(_, c map[string]any) {
			delete(c, "scope")
			c["scp"] = []any{5, "", "metamusic"}
		},
	} {
		r := newRig(t, rigOpts{mutate: mut})
		got, err := r.v.Verify(t.Context(), r.mint(t))
		if err != nil || len(got.Scopes) == 0 {
			t.Errorf("%s: %v %+v", name, err, got)
		}
	}
	r := newRig(t, rigOpts{mutate: func(_, c map[string]any) { c["scope"] = 5; c["scp"] = 5 }})
	var ms *oidc.MissingScopeError
	_, err := r.v.Verify(t.Context(), r.mint(t))
	if !errors.As(err, &ms) || !strings.Contains(err.Error(), "metamusic") {
		t.Fatalf("err = %v", err)
	}
}

func TestVerifierDoesNotMutateInjectedClient(t *testing.T) {
	t.Parallel()
	c := &http.Client{}
	if _, err := oidc.NewVerifier(oidc.Config{}, c, nil); err != nil {
		t.Fatal(err)
	}
	if c.CheckRedirect != nil {
		t.Fatal("injected client was mutated")
	}
}
