package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/connectors"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/listening"
	"github.com/nicolasacchi/metamusic/internal/media/cover"
	mediaserve "github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/media/tagged"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/oidc"
	"github.com/nicolasacchi/metamusic/internal/playlists"
	"github.com/nicolasacchi/metamusic/internal/prompts"
	"github.com/nicolasacchi/metamusic/internal/ratelimit"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/subsonic"
)

// M4aOptions are the test injection points of the M4a services (C114); both are nil in production.
type M4aOptions struct {
	// OIDCClient is the HTTP client of the OIDC verifier (discovery and JWKS); nil => 5 s timeout, no redirects,
	// a clone of http.DefaultTransport. Tests inject the stub provider's client.
	OIDCClient *http.Client
	// SubsonicSleep replaces the context-aware wait after a failed Subsonic authentication; tests inject a no-op.
	SubsonicSleep func(ctx context.Context, d time.Duration) error
}

// m4aServices is what buildM4a returns.
type m4aServices struct {
	OIDC            *oidc.Verifier // nil unless MM_OIDC_ISSUER is set
	Connectors      *connectors.Store
	ConnVerifier    auth.ConnectorVerifier // a nil interface (never a typed nil) unless MM_OIDC_ISSUER is set
	Subsonic        *auth.SubsonicStore
	Covers          *cover.Service
	Downloads       *tagged.Service
	PRM             http.Handler
	SubsonicHandler http.Handler
	Prompts         prompts.Sources
}

type m4aInputs struct {
	cfg       config.Config
	sec       config.Secrets
	st        *store.Store
	data      *os.Root
	lib       *library.Service
	judg      *judgments.Service
	pls       *playlists.Service
	listen    *listening.Service
	audit     *audit.Store
	limiter   *ratelimit.Limiter
	notifier  notify.Notifier
	now       func() time.Time
	newID     func() string
	rnd       io.Reader
	version   string
	covers    *cover.Service
	downloads *tagged.Service
}

// oidcHTTPClient is the production client of the OIDC verifier: 5 s timeout, redirects refused (C114).
func oidcHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	return &http.Client{Timeout: 5 * time.Second, Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// buildM4a wires the hosted-connector, Subsonic, cover, download and prompt services (C114). The grant store, the
// metadata handler and the Subsonic handler exist whether or not OIDC is configured; only the token verifier
// (and with it the connector principal) depends on MM_OIDC_ISSUER.
func buildM4a(in m4aInputs, o M4aOptions) *m4aServices {
	cs := connectors.NewStore(in.st, in.now, in.newID)
	m := &m4aServices{
		Connectors: cs,
		Subsonic:   auth.NewSubsonicStore(in.st, in.sec.Key, in.now, in.newID, in.rnd),
		Covers:     in.covers,
		Downloads:  in.downloads,
		PRM:        connectors.PRMHandler(in.cfg),
		Prompts:    promptSources{lib: in.lib},
	}
	if in.cfg.OIDCIssuer != "" {
		httpc := o.OIDCClient
		if httpc == nil {
			httpc = oidcHTTPClient()
		}
		labels := make(map[string]string, len(in.cfg.OIDCClients))
		for _, c := range in.cfg.OIDCClients {
			labels[c.ID] = c.Label
		}
		v, err := oidc.NewVerifier(oidc.Config{Issuer: in.cfg.OIDCIssuer, JWKSURL: in.cfg.OIDCJWKSURL,
			Resource: strings.TrimRight(in.cfg.PublicURL, "/") + "/mcp", Clients: labels}, httpc, in.now)
		if err == nil { // NewVerifier only validates its inputs; config.Load already did
			m.OIDC = v
			m.ConnVerifier = connectors.NewVerifier(connectors.VerifierOptions{OIDC: v, Store: cs, TrustedUsers: in.cfg.TrustedUsers,
				Labels: labels, Audit: in.audit, Notify: in.notifier, Now: in.now})
		}
	}
	m.SubsonicHandler = subsonic.NewHandler(subsonic.Options{
		Cfg: in.cfg, Version: in.version, Store: in.st, Data: in.data, Lib: in.lib, Judgments: in.judg, Playlists: in.pls,
		Listening: in.listen, Covers: in.covers, Downloads: in.downloads, Creds: m.Subsonic, Audit: in.audit, Limiter: in.limiter,
		Now: in.now, Sleep: o.SubsonicSleep,
	})
	return m
}

// m4aResolvers are the resolvers M4a appends to the media chain after library, exports and clips (C110.3).
func m4aResolvers(covers *cover.Service, downloads *tagged.Service) []mediaserve.Resolver {
	return []mediaserve.Resolver{covers, downloads}
}

// promptSources implements prompts.Sources over the services. STUB(M4a-A1): ContextJSON and Session are
// implemented by the agent lane (they need the context builder's REST bytes and listening.Service.Session).
type promptSources struct{ lib *library.Service }

func (promptSources) ContextJSON(context.Context, auth.Principal, string) ([]byte, error) {
	return nil, errors.ErrUnsupported // STUB(M4a-A1)
}

func (promptSources) Session(context.Context, time.Time, int) (listening.Session, error) {
	return listening.Session{}, errors.ErrUnsupported // STUB(M4a-A1)
}

func (p promptSources) Title(ctx context.Context, trackID string) (string, error) {
	t, err := p.lib.Track(ctx, trackID)
	if err != nil {
		return "", err
	}
	return t.Title, nil
}

func (p promptSources) RecipeKnown(ctx context.Context, trackID string) (bool, error) {
	return p.lib.RecipeKnown(ctx, trackID)
}
