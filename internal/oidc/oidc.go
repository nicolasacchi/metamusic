// Package oidc is the resource-server side of OIDC (C105): discovery, the JWKS cache and Verifier.Verify for
// RFC 9068 JWT access tokens, RS256 only (D-M4a-3). It never reaches internal/connectors, internal/store or
// internal/spend (C116); mapping a verified token to a grant is connectors' job.
package oidc

import (
	"errors"
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
