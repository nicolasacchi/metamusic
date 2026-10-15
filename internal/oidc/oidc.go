// Package oidc is the resource-server side of OIDC (C105): discovery, the JWKS cache and Verifier.Verify for
// RFC 9068 JWT access tokens, RS256 only (D-M4a-3). It never reaches internal/connectors, internal/store or
// internal/spend (C116); mapping a verified token to a grant is connectors' job.
//
// The exported API is frozen by M4a-B2; the bodies are STUB(M4a-C1) until the connectors lane implements them.
package oidc

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Alg is the only JWS algorithm accepted (D-M4a-3); it is a constant, not configuration.
const Alg = "RS256"

// RequiredScope and UserClaim are the token scope and the custom claim naming the user (D-M4a-4).
const (
	RequiredScope = "metamusic"
	UserClaim     = "mm_user"
)

// Leeway is the clock skew tolerated on exp, iat and nbf.
const Leeway = 30 * time.Second

// Config is the verifier configuration (from C102).
type Config struct {
	Issuer, JWKSURL string            // from MM_OIDC_ISSUER / MM_OIDC_JWKS_URL
	Resource        string            // <PublicURL>/mcp, the only accepted audience
	Clients         map[string]string // client_id -> label
}

// Claims are the verified claims of an access token.
type Claims struct {
	Subject, ClientID, User, JTI string
	Scopes                       []string
	Audience                     []string
	IssuedAt, Expires            time.Time
}

// Verification failures (mapped to auth errors by connectors).
var (
	ErrInvalid       = errors.New("oidc: invalid token")
	ErrClientUnknown = errors.New("oidc: client not allowed")
	ErrUnavailable   = errors.New("oidc: keys unavailable")
	// ErrNotImplemented marks a STUB(M4a-C1) body.
	ErrNotImplemented = errors.New("oidc: not implemented")
)

// MissingScopeError means a valid token of an allow-listed client lacks the required scope.
type MissingScopeError struct{ Scope string }

func (e *MissingScopeError) Error() string { return "oidc: token lacks scope " + e.Scope }

// Status reports the JWKS cache state; LastError is a code, never a message.
type Status struct {
	Keys      int
	FetchedAt *time.Time
	LastError string
}

// Verifier verifies access tokens.
type Verifier struct {
	cfg   Config
	httpc *http.Client
	now   func() time.Time
}

// NewVerifier builds a verifier; it never fetches. A nil httpc or now means the default client and time.Now.
func NewVerifier(c Config, httpc *http.Client, now func() time.Time) (*Verifier, error) {
	if now == nil {
		now = time.Now
	}
	return &Verifier{cfg: c, httpc: httpc, now: now}, nil
}

// Prefetch fetches discovery and the JWKS once; run it in a goroutine at startup. It never fails startup.
// STUB(M4a-C1)
func (v *Verifier) Prefetch(context.Context) {}

// Verify checks token in the exact order of C105. STUB(M4a-C1): always ErrNotImplemented-wrapped ErrInvalid.
func (v *Verifier) Verify(context.Context, string) (Claims, error) {
	return Claims{}, errors.Join(ErrInvalid, ErrNotImplemented)
}

// Status returns the JWKS cache state. STUB(M4a-C1)
func (v *Verifier) Status() Status { return Status{} }
