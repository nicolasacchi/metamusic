// Package app is the one constructor of services, gate dependencies and
// api.Deps (C39). serve, dev, mcp-stdio and the test rig all build through
// Build, so the faces cannot drift.
package app

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/gate"
	"github.com/nicolasacchi/metamusic/internal/httpserver"
	"github.com/nicolasacchi/metamusic/internal/idem"
	"github.com/nicolasacchi/metamusic/internal/library"
	mediaserve "github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/ratelimit"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Options tunes Build; every field is optional.
type Options struct {
	Now    func() time.Time   // nil => time.Now; servertest passes Rig.Clock.Now
	NewID  func() string      // nil => UUIDv7
	Limits ratelimit.Config   // nil => ratelimit.Default.Scaled(cfg.RateScale)
	Probe  *mediaserve.Object // what cmd's ensureProbe returns for serve/dev; nil => probe ops answer 503
	// Version and Commit are reported by the system operations.
	Version, Commit string
}

// Services is everything Build wires.
type Services struct {
	// Deps is the api.Deps every registered operation receives.
	Deps api.Deps
	// Server carries the shared services and gate dependencies for httpserver.New;
	// the caller adds SPA and MCPFactory.
	Server httpserver.Options

	Tokens  *auth.TokenStore
	Local   *auth.LocalAuth
	Idem    *idem.Store
	Audit   *audit.Store
	Limiter *ratelimit.Limiter
}

// Build constructs the services over st and data. Every store and service
// receives o.Now, which lets rig tests advance one clock.
func Build(ctx context.Context, cfg config.Config, sec config.Secrets, st *store.Store, data *os.Root, o Options) (*Services, error) {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	newID := api.Deps{NewID: o.NewID}.NewIDFunc()
	limits := o.Limits
	if limits == nil {
		limits = ratelimit.Default.Scaled(max(cfg.RateScale, 1))
	}

	tokens := auth.NewTokenStore(st, sec.Key, now)
	local := auth.NewLocalAuth(st, sec.Key, cfg.PublicURL)
	local.UseClock(now)

	lib := library.New(st, data, now)
	signer := mediaserve.NewSigner(sec.Key, now)

	s := &Services{
		Tokens:  tokens,
		Local:   local,
		Idem:    idem.NewStore(st, now),
		Audit:   audit.NewStore(st, newID),
		Limiter: ratelimit.New(limits, now),
	}
	s.Deps = api.Deps{
		Store: st, Tokens: tokens, Cfg: cfg, Version: o.Version, Commit: o.Commit, Started: now(),
		Library: lib, Signer: signer, Probe: o.Probe, Now: now, NewID: o.NewID,
	}
	s.Server = httpserver.Options{
		Cfg: cfg, Sec: sec, Store: st, Tokens: tokens, Sessions: local,
		Version: o.Version, Commit: o.Commit, Started: s.Deps.Started,
		Library: lib, Signer: signer, Probe: o.Probe, Media: mediaserve.Handler(signer, data, lib, o.Probe), Now: now,
		Audit: s.Audit, Idem: s.Idem, Limiter: s.Limiter, NewID: o.NewID,
	}
	return s, nil
}

// Gated builds the Huma API and the gate around it without an HTTP listener,
// from the same dependencies and gate stores as Server. mcp-stdio uses it.
func (s *Services) Gated() (huma.API, http.Handler) {
	mux := http.NewServeMux()
	a := api.New(mux, s.Deps)
	return a, gate.New(gate.Options{API: a, Inner: mux, Audit: s.Audit, Idem: s.Idem, Limiter: s.Limiter, Now: s.Deps.Now})
}

// Close releases background work started by Build (none yet; M2a services
// that own goroutines add their drain here).
func (s *Services) Close(ctx context.Context) error { return nil }
