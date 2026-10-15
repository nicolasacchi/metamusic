// Package oidctest is a test-only stub OIDC provider mimicking the Authelia 4.39 endpoints (C105). It is listed
// in TestNoTestHelpersInBinary and must never be linked into the binary.
//
// The exported API is frozen by M4a-B2; the bodies are STUB(M4a-C1) until the connectors lane implements them.
// Until then New skips the calling test, so rigs that ask for an OIDC provider skip by name.
package oidctest

import (
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
	UserClaim  bool              // emit mm_user when scope metamusic is granted (default true)
	IssParam   bool              // RFC 9207 iss on the authorization response (default true)
	Mutate     func(header, claims map[string]any)
}

// Recorded is one authorize or token request as the stub saw it (secrets masked).
type Recorded struct {
	Endpoint, GrantType, ClientID, Scope, Resource string // Endpoint: "authorize" | "token"
	At                                             time.Time
}

// Provider is a running stub; URL is its issuer.
type Provider struct{ URL string }

// New starts the stub (an httptest server closed by tb.Cleanup). STUB(M4a-C1): skips the test.
func New(tb testing.TB, _ Options) *Provider {
	tb.Helper()
	tb.Skip("M4a-C1: oidctest provider not implemented yet")
	return nil
}

// RotateKey publishes a new signing key (new kid); the old one stays published until DropOldKeys. STUB(M4a-C1)
func (p *Provider) RotateKey() {}

// DropOldKeys unpublishes every key but the current one. STUB(M4a-C1)
func (p *Provider) DropOldKeys() {}

// Mint signs an access token directly for unit tests. STUB(M4a-C1)
func (p *Provider) Mint(tb testing.TB, _, _ string, _, _ []string) string {
	tb.Helper()
	tb.Skip("M4a-C1: oidctest provider not implemented yet")
	return ""
}

// Requests lists the recorded authorize and token requests. STUB(M4a-C1)
func (p *Provider) Requests() []Recorded { return nil }

// Fetcher is the go-sdk authorization-code fetcher for this stub: GET the URL without following redirects and
// parse code, state and iss. STUB(M4a-C1)
func (p *Provider) Fetcher(tb testing.TB) auth.AuthorizationCodeFetcher {
	tb.Helper()
	tb.Skip("M4a-C1: oidctest provider not implemented yet")
	return nil
}
