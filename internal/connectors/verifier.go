package connectors

import (
	"context"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/oidc"
)

// VerifierOptions are the collaborators of the connector verifier (C106).
type VerifierOptions struct {
	OIDC         *oidc.Verifier
	Store        *Store
	TrustedUsers []string
	Labels       map[string]string // client_id -> label (C102)
	Audit        *audit.Store
	Notify       notify.Notifier
	Now          func() time.Time
}

// Verifier maps a verified OAuth access token to a connector principal; it implements auth.ConnectorVerifier.
type Verifier struct{ o VerifierOptions }

var _ auth.ConnectorVerifier = (*Verifier)(nil)

// NewVerifier builds the verifier.
func NewVerifier(o VerifierOptions) *Verifier { return &Verifier{o: o} }

// VerifyConnector runs the six steps of C106. STUB(M4a-C2): refuses every token (fail closed).
func (v *Verifier) VerifyConnector(context.Context, string) (auth.Principal, error) {
	return auth.Principal{}, auth.ErrInvalidToken
}
