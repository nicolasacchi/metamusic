package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const suClient = "stepup-client"

type suIDP struct {
	srv       *httptest.Server
	key       *rsa.PrivateKey
	discovery func(w http.ResponseWriter, base string)
	token     func(w http.ResponseWriter, r *http.Request)
	tokenHits atomic.Int32
}

func newSUIDP(t *testing.T) *suIDP {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &suIDP{key: k}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		if f.discovery != nil {
			f.discovery(w, f.srv.URL)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/authorize?x=1", "token_endpoint": f.srv.URL + "/token"})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(k.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes())}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		f.tokenHits.Add(1)
		f.token(w, r)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *suIDP) client(secret string, now time.Time) *StepUpClient {
	v, _ := NewVerifier(Config{Issuer: f.srv.URL, JWKSURL: f.srv.URL + "/jwks"}, nil, func() time.Time { return now })
	return NewStepUpClient(v, StepUpConfig{ClientID: suClient, ClientSecret: secret, RedirectURL: "https://mm.example.com/auth/step-up/callback"})
}

func (f *suIDP) sign(t *testing.T, claims jwt.MapClaims, hdr map[string]any, key *rsa.PrivateKey) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = "k1"
	for k, v := range hdr {
		if v == nil {
			delete(tok.Header, k)
		} else {
			tok.Header[k] = v
		}
	}
	if key == nil {
		key = f.key
	}
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func suClaims(f *suIDP, now time.Time) jwt.MapClaims {
	return jwt.MapClaims{"iss": f.srv.URL, "aud": suClient, "sub": "s", "exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "nonce": "n1",
		"auth_time": now.Unix(), "amr": []string{"pwd", "mfa"}, "preferred_username": "alice"}
}

func TestStepUpVerifyIDToken(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	f := newSUIDP(t)
	c := f.client("", now)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	mut := func(m func(jwt.MapClaims)) jwt.MapClaims {
		cl := suClaims(f, now)
		m(cl)
		return cl
	}
	ok, err := c.VerifyIDToken(context.Background(), f.sign(t, suClaims(f, now), nil, nil), "n1", "alice")
	if err != nil || ok.User != "alice" || ok.Subject != "s" || !ok.AuthTime.Equal(now) {
		t.Fatalf("valid token: %+v %v", ok, err)
	}
	// azp present and matching, and multi-audience with azp
	good := mut(func(m jwt.MapClaims) { m["azp"] = suClient; m["aud"] = []string{suClient, "other"} })
	if _, err := c.VerifyIDToken(context.Background(), f.sign(t, good, nil, nil), "n1", "alice"); err != nil {
		t.Fatalf("azp: %v", err)
	}
	for name, tc := range map[string]struct {
		tok  string
		code string
	}{
		"shape":       {"a.b", "shape"},
		"too long":    {strings.Repeat("a", maxTokenBytes+1), "shape"},
		"header junk": {"!!!.e30.e30", "header"},
		"alg": {func() string {
			s := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{})
			x, _ := s.SignedString([]byte("k"))
			return x
		}(), "alg"},
		"crit":           {f.sign(t, suClaims(f, now), map[string]any{"crit": []string{"x"}}, nil), "crit"},
		"no kid":         {f.sign(t, suClaims(f, now), map[string]any{"kid": nil}, nil), "kid"},
		"unknown kid":    {f.sign(t, suClaims(f, now), map[string]any{"kid": "zz"}, nil), "kid_unknown"},
		"signature":      {f.sign(t, suClaims(f, now), nil, other), "signature"},
		"iss":            {f.sign(t, mut(func(m jwt.MapClaims) { m["iss"] = "https://evil" }), nil, nil), "iss"},
		"aud":            {f.sign(t, mut(func(m jwt.MapClaims) { m["aud"] = "x" }), nil, nil), "aud"},
		"azp":            {f.sign(t, mut(func(m jwt.MapClaims) { m["azp"] = "x" }), nil, nil), "azp"},
		"multi aud azp":  {f.sign(t, mut(func(m jwt.MapClaims) { m["aud"] = []string{suClient, "o"} }), nil, nil), "azp"},
		"exp":            {f.sign(t, mut(func(m jwt.MapClaims) { m["exp"] = now.Add(-time.Hour).Unix() }), nil, nil), "exp"},
		"iat":            {f.sign(t, mut(func(m jwt.MapClaims) { m["iat"] = now.Add(time.Hour).Unix() }), nil, nil), "iat"},
		"nonce":          {f.sign(t, mut(func(m jwt.MapClaims) { m["nonce"] = "other" }), nil, nil), "nonce"},
		"auth_time none": {f.sign(t, mut(func(m jwt.MapClaims) { delete(m, "auth_time") }), nil, nil), "auth_time_missing"},
		"auth_time old":  {f.sign(t, mut(func(m jwt.MapClaims) { m["auth_time"] = now.Add(-time.Hour).Unix() }), nil, nil), "auth_time_stale"},
		"auth_time next": {f.sign(t, mut(func(m jwt.MapClaims) { m["auth_time"] = now.Add(time.Hour).Unix() }), nil, nil), "auth_time_stale"},
		"amr pwd only":   {f.sign(t, mut(func(m jwt.MapClaims) { m["amr"] = []string{"pwd"} }), nil, nil), "amr_no_second_factor"},
		"amr none":       {f.sign(t, mut(func(m jwt.MapClaims) { delete(m, "amr") }), nil, nil), "amr_no_second_factor"},
		"user":           {f.sign(t, mut(func(m jwt.MapClaims) { m["preferred_username"] = "bob" }), nil, nil), "user_mismatch"},
		"user missing":   {f.sign(t, mut(func(m jwt.MapClaims) { delete(m, "preferred_username") }), nil, nil), "user_mismatch"},
	} {
		_, err := c.VerifyIDToken(context.Background(), tc.tok, "n1", "alice")
		if !errors.Is(err, ErrStepUp) || !strings.HasSuffix(err.Error(), ": "+tc.code) {
			t.Errorf("%s: err = %v, want code %s", name, err, tc.code)
		}
	}
	if _, err := c.VerifyIDToken(context.Background(), f.sign(t, suClaims(f, now), nil, nil), "", "alice"); err == nil {
		t.Error("empty expected nonce accepted")
	}
	// no keys reachable
	v, _ := NewVerifier(Config{Issuer: f.srv.URL, JWKSURL: "http://127.0.0.1:1/jwks"}, nil, func() time.Time { return now })
	dead := NewStepUpClient(v, StepUpConfig{ClientID: suClient})
	if _, err := dead.VerifyIDToken(context.Background(), f.sign(t, suClaims(f, now), nil, nil), "n1", "alice"); err == nil || !strings.HasSuffix(err.Error(), "keys_unavailable") {
		t.Errorf("unreachable keys: %v", err)
	}
}

func TestStepUpAuthURLAndDiscovery(t *testing.T) {
	t.Parallel()
	now := time.Now()
	f := newSUIDP(t)
	c := f.client("", now)
	u, err := c.AuthURL(context.Background(), "st", "no", "ver")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"x=1", "response_type=code", "client_id=" + suClient, "code_challenge=" + PKCEChallenge("ver"), "code_challenge_method=S256", "prompt=login", "max_age=300", "scope=openid+profile", "state=st", "nonce=no"} {
		if !strings.Contains(u, want) {
			t.Errorf("auth URL lacks %q: %s", want, u)
		}
	}
	for name, doc := range map[string]func(http.ResponseWriter, string){
		"mismatch": func(w http.ResponseWriter, base string) {
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": "https://other", "authorization_endpoint": base + "/a", "token_endpoint": base + "/t"})
		},
		"parse": func(w http.ResponseWriter, base string) { _, _ = w.Write([]byte("{")) },
		"missing": func(w http.ResponseWriter, base string) {
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": base})
		},
		"refused": func(w http.ResponseWriter, base string) {
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": base, "authorization_endpoint": "http://evil.example.com/a", "token_endpoint": base + "/t"})
		},
		"5xx": func(w http.ResponseWriter, base string) { http.Error(w, "x", 500) },
	} {
		g := newSUIDP(t)
		g.discovery = doc
		if _, err := g.client("", now).AuthURL(context.Background(), "s", "n", "v"); !errors.Is(err, ErrStepUp) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// the discovery document is cached after the first success
	var hits atomic.Int32
	g := newSUIDP(t)
	g.discovery = func(w http.ResponseWriter, base string) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": base, "authorization_endpoint": base + "/a", "token_endpoint": base + "/t"})
	}
	gc := g.client("", now)
	_, _ = gc.AuthURL(context.Background(), "s", "n", "v")
	_, _ = gc.AuthURL(context.Background(), "s", "n", "v")
	if hits.Load() != 1 {
		t.Errorf("discovery fetched %d times", hits.Load())
	}
}

func TestStepUpExchange(t *testing.T) {
	t.Parallel()
	now := time.Now()
	f := newSUIDP(t)
	var gotID, gotPW, gotForm string
	var basic bool
	f.token = func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotID, gotPW, basic = r.BasicAuth()
		gotForm = r.PostForm.Get("client_id") + "|" + r.PostForm.Get("code") + "|" + r.PostForm.Get("code_verifier") + "|" + r.PostForm.Get("grant_type")
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": "the.id.token"})
	}
	// confidential: client_secret_basic, no client_id in the body
	tok, err := f.client("sec ret", now).Exchange(context.Background(), "c1", "v1")
	if err != nil || tok != "the.id.token" || !basic || gotID != suClient || gotPW != "sec+ret" && gotPW != "sec ret" || gotForm != "|c1|v1|authorization_code" {
		t.Fatalf("confidential: %q %v basic=%v id=%q pw=%q form=%q", tok, err, basic, gotID, gotPW, gotForm)
	}
	// public: client_id in the body, no Authorization
	if _, err = f.client("", now).Exchange(context.Background(), "c2", "v2"); err != nil || basic && gotID != "" || !strings.HasPrefix(gotForm, suClient+"|c2|v2") {
		t.Fatalf("public: %v basic=%v form=%q", err, basic, gotForm)
	}
	for name, h := range map[string]func(http.ResponseWriter, *http.Request){
		"400":      func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", 400) },
		"no token": func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"access_token":"x"}`)) },
		"junk":     func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`<html>`)) },
		"huge": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("a", maxFetchBytes+10)))
		},
	} {
		g := newSUIDP(t)
		g.token = h
		if _, err := g.client("s", now).Exchange(context.Background(), "c", "v"); !errors.Is(err, ErrStepUp) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// transport failure and discovery failure
	g := newSUIDP(t)
	g.token = func(http.ResponseWriter, *http.Request) {}
	c := g.client("s", now)
	if _, err := c.AuthURL(context.Background(), "s", "n", "v"); err != nil { // warm the endpoint cache
		t.Fatal(err)
	}
	g.srv.Close()
	if _, err := c.Exchange(context.Background(), "c", "v"); !errors.Is(err, ErrStepUp) {
		t.Errorf("transport: %v", err)
	}
	h := newSUIDP(t)
	h.discovery = func(w http.ResponseWriter, base string) { http.Error(w, "x", 500) }
	if _, err := h.client("s", now).Exchange(context.Background(), "c", "v"); !errors.Is(err, ErrStepUp) {
		t.Errorf("discovery failure: %v", err)
	}
}
