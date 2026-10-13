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
	"github.com/nicolasacchi/metamusic/internal/backup"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/drafts"
	"github.com/nicolasacchi/metamusic/internal/exports"
	"github.com/nicolasacchi/metamusic/internal/gate"
	"github.com/nicolasacchi/metamusic/internal/httpserver"
	"github.com/nicolasacchi/metamusic/internal/idem"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/listening"
	mediaserve "github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/playlists"
	"github.com/nicolasacchi/metamusic/internal/ratelimit"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/trackctx"
)

// Options tunes Build; every field is optional.
type Options struct {
	Now    func() time.Time   // nil => time.Now; servertest passes Rig.Clock.Now
	NewID  func() string      // nil => UUIDv7
	Limits ratelimit.Config   // nil => ratelimit.Default.Scaled(cfg.RateScale)
	Probe  *mediaserve.Object // what cmd's ensureProbe returns for serve/dev; nil => probe ops answer 503
	Notify notify.Notifier    // nil => ntfy (MM_NTFY_URL) or Nop, wrapped in notify.Async
	HTTP   *http.Client       // ntfy client; nil => 10 s timeout client
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
	Devices *auth.DeviceStore
	Backups *backup.Service
	Exports *exports.Service
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
	devices := auth.NewDeviceStore(st, sec.Key, now, newID)

	notifier := o.Notify
	if notifier == nil {
		if cfg.NtfyURL != "" {
			hc := o.HTTP
			if hc == nil {
				hc = &http.Client{Timeout: 10 * time.Second}
			}
			notifier = notify.NewNtfy(cfg.NtfyURL, sec.NtfyToken, hc)
		} else {
			notifier = notify.Nop{}
		}
		notifier = notify.Async(notifier, 10*time.Second)
	}

	judg := judgments.New(st, lib, now, newID)
	listen := listening.New(st, now)
	pls := playlists.New(st, lib, now, newID)
	drf := drafts.New(st, lib, now, newID)
	exp := exports.New(exports.Options{Store: st, Data: data, Lib: lib, Playlists: pls, Ratings: judg, Signer: signer, Now: now, NewID: newID})
	bak := backup.New(backup.Options{Store: st, DataDir: cfg.DataDir, BackupDir: cfg.BackupDir, Exports: exports.NewDBSource(st, lib, pls),
		Now: now, NewID: newID, Version: o.Version, Notify: notifier})
	ctxb := trackctx.New(lib, judg, listen, signer, cfg.PublicURL, now)

	s := &Services{
		Tokens:  tokens,
		Local:   local,
		Devices: devices,
		Backups: bak,
		Exports: exp,
		Idem:    idem.NewStore(st, now),
		Audit:   audit.NewStore(st, newID),
		Limiter: ratelimit.New(limits, now),
	}
	s.Deps = api.Deps{
		Store: st, Tokens: tokens, Cfg: cfg, Version: o.Version, Commit: o.Commit, Started: now(),
		Library: lib, Signer: signer, Probe: o.Probe, Now: now, NewID: o.NewID,
		Judgments: judg, Drafts: drf, Playlists: pls, Exports: exp, Backups: bak, Context: ctxb, Listening: listen,
		Audit: s.Audit, Devices: devices,
	}
	s.Server = httpserver.Options{
		Cfg: cfg, Sec: sec, Store: st, Tokens: tokens, Sessions: local,
		Version: o.Version, Commit: o.Commit, Started: s.Deps.Started,
		Library: lib, Signer: signer, Probe: o.Probe, Media: mediaserve.Handler(signer, data, mediaserve.Chain(lib, exp), o.Probe), Now: now,
		Audit: s.Audit, Idem: s.Idem, Limiter: s.Limiter, NewID: o.NewID,
		Judgments: judg, Drafts: drf, Playlists: pls, Exports: exp, Backups: bak, Context: ctxb, Listening: listen,
		Devices: devices, DeviceStore: devices, Notify: notifier,
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

// Close releases background work started by Build: it refuses new backups and
// waits for running ones until ctx ends.
func (s *Services) Close(ctx context.Context) error { return s.Backups.Close(ctx) }
