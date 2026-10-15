// Package oidctest is a test-only stub OIDC provider mimicking the Authelia 4.39 endpoints (C105). It is listed
// in TestNoTestHelpersInBinary and must never be linked into the binary.
//
// It is deliberately strict (PKCE, exact redirect, one auth method per client, single-use codes, refresh
// rotation with reuse detection) so acceptance tests cannot pass against a lenient stub; stub_test.go pins each rule.
package oidctest

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
)

// Client is one pre-registered OAuth client of the stub.
type Client struct {
	ID           string
	Secret       string   // "" => public client (token_endpoint_auth_method none)
	AuthMethod   string   // "none" | "client_secret_post" | "client_secret_basic", enforced like Authelia
	RedirectURIs []string // exact match
	Audience     []string // allowed resources/audiences (requested_audience_mode implicit)
	Scopes       []string // allowed scopes
}

// Options configure New.
type Options struct {
	Clients    []Client
	Users      map[string]string // username -> opaque subject
	LoginAs    string            // the user the authorization endpoint authenticates (no UI); keys are RSA-2048, RS256
	AccessTTL  time.Duration     // default 1h
	RefreshTTL time.Duration     // default 720h
	Now        func() time.Time  // the rig clock
	ScopeClaim string            // "scope" (default) | "scp" | "both"
	UserClaim  bool              // emit mm_user when scope metamusic is granted
	IssParam   bool              // RFC 9207 iss on the authorization response
	Mutate     func(header, claims map[string]any)
}

// Recorded is one authorize or token request as the stub saw it (secrets are never recorded).
type Recorded struct {
	Endpoint, GrantType, ClientID, Scope, Resource string // Endpoint: "authorize" | "token"
	At                                             time.Time
}

// Provider is a running stub; URL is its issuer.
type Provider struct {
	URL string

	opts Options
	srv  *httptest.Server

	mu       sync.Mutex
	keys     []signingKey // published keys; the last one signs
	codes    map[string]*authCode
	refresh  map[string]*refreshTok
	revoked  map[string]bool // revoked families
	nkeys    int
	requests []Recorded
}

// New starts the stub (an httptest server closed by tb.Cleanup).
func New(tb testing.TB, o Options) *Provider {
	tb.Helper()
	if o.AccessTTL == 0 {
		o.AccessTTL = time.Hour
	}
	if o.RefreshTTL == 0 {
		o.RefreshTTL = 720 * time.Hour
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.ScopeClaim == "" {
		o.ScopeClaim = "scope"
	}
	p := &Provider{opts: o, codes: map[string]*authCode{}, refresh: map[string]*refreshTok{}, revoked: map[string]bool{}}
	p.keys = []signingKey{newKey(0)}
	p.nkeys = 1
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.metadata)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", p.metadata)
	mux.HandleFunc("GET /jwks.json", p.jwks)
	mux.HandleFunc("GET /api/oidc/authorization", p.authorize)
	mux.HandleFunc("POST /api/oidc/token", p.token)
	p.srv = httptest.NewServer(mux)
	tb.Cleanup(p.srv.Close)
	p.URL = p.srv.URL
	return p
}

func (p *Provider) client(id string) *Client {
	for i := range p.opts.Clients {
		if p.opts.Clients[i].ID == id {
			return &p.opts.Clients[i]
		}
	}
	return nil
}

func (p *Provider) record(r Recorded) {
	r.At = p.opts.Now()
	p.mu.Lock()
	p.requests = append(p.requests, r)
	p.mu.Unlock()
}

// Requests lists the recorded authorize and token requests.
func (p *Provider) Requests() []Recorded {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Recorded(nil), p.requests...)
}

func (p *Provider) metadata(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                                         p.URL,
		"authorization_endpoint":                         p.URL + "/api/oidc/authorization",
		"token_endpoint":                                 p.URL + "/api/oidc/token",
		"jwks_uri":                                       p.URL + "/jwks.json",
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"client_secret_basic", "client_secret_post", "none"},
		"scopes_supported":                               []string{"openid", "offline_access", "profile", "metamusic"},
		"authorization_response_iss_parameter_supported": true,
	})
}

func randID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Fetcher is the go-sdk authorization-code fetcher for this stub: GET the URL without following redirects and
// parse code, state and iss.
func (p *Provider) Fetcher(tb testing.TB) auth.AuthorizationCodeFetcher {
	tb.Helper()
	return p.fetcher
}
