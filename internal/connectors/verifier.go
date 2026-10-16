package connectors

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/canonjson"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/oidc"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
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
	Logger       *slog.Logger // nil => slog.Default()
}

// Verifier maps a verified OAuth access token to a connector principal; it implements auth.ConnectorVerifier.
type Verifier struct{ o VerifierOptions }

var _ auth.ConnectorVerifier = (*Verifier)(nil)

// NewVerifier builds the verifier.
func NewVerifier(o VerifierOptions) *Verifier {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Verifier{o: o}
}

// touchBudget bounds the best-effort last_used_at write, like M0's token touch.
const touchBudget = 250 * time.Millisecond

func (v *Verifier) log() *slog.Logger {
	if v.o.Logger != nil {
		return v.o.Logger
	}
	return slog.Default()
}

// refuse logs the refusal code (never a value of the token) and returns err.
func (v *Verifier) refuse(ctx context.Context, code string, err error) error {
	v.log().LogAttrs(ctx, slog.LevelInfo, "connector token refused", slog.String("code", code))
	return err
}

// VerifyConnector runs the six steps of C106, in this order: oidc.Verify, the trusted user, Store.Ensure (with the
// one-time audit row and push of a created grant), the revoked check, the last_used_at touch, the principal.
func (v *Verifier) VerifyConnector(ctx context.Context, bearer string) (auth.Principal, error) {
	if v.o.OIDC == nil || v.o.Store == nil {
		return auth.Principal{}, auth.ErrUnavailable
	}
	// 1. the token
	cl, err := v.o.OIDC.Verify(ctx, bearer)
	if err != nil {
		var ms *oidc.MissingScopeError
		switch {
		case errors.As(err, &ms):
			return auth.Principal{}, &auth.ScopeError{Scope: ms.Scope}
		case errors.Is(err, oidc.ErrClientUnknown):
			return auth.Principal{}, v.refuse(ctx, "client_not_allowed", auth.ErrConnectorForbidden)
		case errors.Is(err, oidc.ErrUnavailable):
			return auth.Principal{}, v.refuse(ctx, "keys_unavailable", auth.ErrUnavailable)
		case errors.Is(err, oidc.ErrInvalid):
			return auth.Principal{}, auth.ErrInvalidToken
		}
		return auth.Principal{}, err
	}
	// 2. the user claim names the operator
	switch {
	case cl.User == "":
		return auth.Principal{}, v.refuse(ctx, "user_claim_missing", auth.ErrConnectorForbidden)
	case !slices.Contains(v.o.TrustedUsers, cl.User):
		return auth.Principal{}, v.refuse(ctx, "user_not_trusted", auth.ErrConnectorForbidden)
	}
	// 3. the grant
	label := v.o.Labels[cl.ClientID]
	if label == "" {
		label = cl.ClientID
	}
	g, created, err := v.o.Store.Ensure(ctx, cl.ClientID, cl.Subject, cl.User, label)
	if err != nil {
		return auth.Principal{}, errors.New("connectors: " + redact.Error(err))
	}
	p := principalOf(g, v.o.Labels)
	if created {
		v.announce(ctx, p, g)
	}
	// 4. revoked
	if g.RevokedAt != nil {
		return auth.Principal{}, v.refuse(ctx, "grant_revoked", auth.ErrConnectorRevoked)
	}
	// 5. touch (best effort)
	tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), touchBudget)
	if err := v.o.Store.Touch(tctx, g.ID); err != nil {
		v.log().LogAttrs(ctx, slog.LevelWarn, "connector touch failed", slog.String("error", redact.Error(err)))
	}
	cancel()
	// 6. the principal
	return p, nil
}

func principalOf(g Grant, labels map[string]string) auth.Principal {
	name := labels[g.ClientID]
	if name == "" {
		name = g.Label
	}
	return auth.Principal{Kind: auth.KindAgent, ID: g.ID, Name: name, Scopes: slices.Clone(g.Scopes), Via: "oauth", Client: g.ClientID}
}

// announce writes the one audit row and sends the one push of a newly created grant. Neither carries the subject
// or the username. Failures are logged and never refuse the request.
func (v *Verifier) announce(ctx context.Context, p auth.Principal, g Grant) {
	if v.o.Audit != nil {
		raw, _ := json.Marshal(map[string]string{"client_id": g.ClientID})
		canon, _ := canonjson.Canonical(raw)
		sum := sha256.Sum256(canon)
		err := v.o.Audit.Insert(context.WithoutCancel(ctx), audit.Entry{
			At: v.o.Now(), Principal: p, Transport: reqinfo.OAuth, Client: reqinfo.SanitizeClient("oauth:" + g.ClientID),
			Op: "auth:connector_grant", Method: audit.SystemMethod, ArgsSHA256: hex.EncodeToString(sum[:]), Status: 201,
			Idempotency: audit.IdemNone, CreatedIDs: []string{g.ID},
		})
		if err != nil {
			audit.NoteFailure()
			v.log().LogAttrs(ctx, slog.LevelError, "connector grant audit failed", slog.String("error", redact.Error(err)))
		}
	}
	if v.o.Notify != nil {
		err := v.o.Notify.Notify(context.WithoutCancel(ctx), notify.Message{
			Title: "Connector connected", Body: p.Name + " can now read and comment (generate is off).", Priority: 3, Tags: []string{"link"},
		})
		if err != nil {
			v.log().LogAttrs(ctx, slog.LevelWarn, "connector push failed", slog.String("error", redact.Error(err)))
		}
	}
}
