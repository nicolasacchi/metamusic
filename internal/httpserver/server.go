// Package httpserver composes auth, the Huma API, MCP and the SPA into one handler.
package httpserver

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/backup"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/drafts"
	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/exports"
	"github.com/nicolasacchi/metamusic/internal/gate"
	"github.com/nicolasacchi/metamusic/internal/idem"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/listening"
	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/playlists"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/ratelimit"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/trackctx"
)

// Options configures New.
type Options struct {
	Cfg      config.Config
	Sec      config.Secrets
	Store    *store.Store
	Tokens   *auth.TokenStore
	Sessions *auth.LocalAuth
	MCP      http.Handler // nil => /mcp is 404 (unless MCPFactory is set)
	// MCPFactory builds the MCP handler from the same huma.API and inner mux
	// that serve /api, so tools dispatch in-process into one registry. Used
	// only when MCP is nil.
	MCPFactory      func(a huma.API, inner http.Handler) (http.Handler, error)
	Started         time.Time // zero => time.Now() at New
	SPA             fs.FS
	Version, Commit string

	// M1 (C20). Library, Signer and Probe reach api.Deps; nil values make the
	// matching operations answer 503 UNAVAILABLE. Media serves /media/ (nil =>
	// 404 problem NOT_FOUND). Now fixes the clock of api.Deps (nil => time.Now).
	Library *library.Service
	Signer  *serve.Signer
	Probe   *serve.Object
	Media   http.Handler
	Now     func() time.Time

	// M2a (C39). The gate (audit, authorize, rate limit, idempotency) wraps the
	// inner mux. app.Build supplies one shared set; a nil value is replaced by a
	// default built from Store, Now and NewID so the gate is never skipped.
	Audit   *audit.Store
	Idem    *idem.Store
	Limiter *ratelimit.Limiter
	NewID   func() string // nil => UUIDv7

	// M2a services (C39); they reach api.Deps. A nil value makes the matching
	// operations answer 503 UNAVAILABLE (once their handlers exist).
	Judgments *judgments.Service
	Drafts    *drafts.Service
	Playlists *playlists.Service
	Exports   *exports.Service
	Backups   *backup.Service
	Context   *trackctx.Builder
	Listening *listening.Service
	// M2b services (C53); nil makes the matching operations answer 503 UNAVAILABLE.
	Duels *duels.Service
	Ears  *ears.Service
	Clips *clip.Service
	// Devices verifies the device cookie; a nil interface disables device auth.
	// DeviceStore backs the pairing pages and the device operations.
	Devices     auth.DeviceVerifier
	DeviceStore *auth.DeviceStore
	Notify      notify.Notifier
}

func writeProblem(w http.ResponseWriter, status int, code, msg string) {
	problem.Write(w, status, code, msg, nil)
}

// New builds the full handler chain.
func New(o Options) (http.Handler, error) {
	if o.Tokens == nil || o.Sessions == nil {
		return nil, errors.New("httpserver: Tokens and Sessions are required")
	}
	started := o.Started
	if started.IsZero() {
		started = time.Now()
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	inner := http.NewServeMux()
	a := api.New(inner, api.Deps{Store: o.Store, Tokens: o.Tokens, Cfg: o.Cfg, Version: o.Version, Commit: o.Commit, Started: started,
		Library: o.Library, Signer: o.Signer, Probe: o.Probe, Now: o.Now, NewID: o.NewID,
		Judgments: o.Judgments, Drafts: o.Drafts, Playlists: o.Playlists, Exports: o.Exports, Backups: o.Backups,
		Context: o.Context, Listening: o.Listening, Audit: o.Audit, Devices: o.DeviceStore, Notify: o.Notify,
		Duels: o.Duels, Ears: o.Ears, Clips: o.Clips})
	gated := http.Handler(inner)
	if o.Store != nil {
		newID := api.Deps{NewID: o.NewID}.NewIDFunc()
		if o.Audit == nil {
			o.Audit = audit.NewStore(o.Store, newID)
		}
		if o.Idem == nil {
			o.Idem = idem.NewStore(o.Store, now)
		}
		if o.Limiter == nil {
			o.Limiter = ratelimit.New(ratelimit.Default.Scaled(max(o.Cfg.RateScale, 1)), now)
		}
		gated = gate.New(gate.Options{API: a, Inner: inner, Audit: o.Audit, Idem: o.Idem, Limiter: o.Limiter, Now: now})
	}
	if o.MCP == nil && o.MCPFactory != nil {
		m, err := o.MCPFactory(a, gated)
		if err != nil {
			return nil, err
		}
		o.MCP = m
	}

	// pattern ("GET /api/ping") -> operation id, for served-operation recording.
	opByPattern := map[string]string{}
	for _, op := range api.Operations(a) {
		opByPattern[op.Method+" "+op.Path] = op.ID
	}
	rec := &servedRecorder{}
	innerH := gated
	if o.Cfg.E2E {
		innerH = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isSPARequest(r) {
				gated.ServeHTTP(w, r)
				return
			}
			_, pat := inner.Handler(r)
			sw := &statusWriter{ResponseWriter: w}
			gated.ServeHTTP(sw, r)
			if id, ok := opByPattern[pat]; ok && sw.status > 0 && sw.status < 400 {
				rec.add(id)
			}
		})
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(`{"status":"ok"}` + "\n"))
	})
	if o.DeviceStore != nil {
		pairing := auth.PairingHandler(auth.PairingOptions{
			Cfg: o.Cfg, Devices: o.DeviceStore, Sessions: o.Sessions,
			Allow: func() (bool, time.Duration) {
				if o.Limiter == nil {
					return true, 0
				}
				ok, retry, _ := o.Limiter.Allow("pair", ratelimit.Pair)
				return ok, retry
			},
			OnPaired: func(ctx context.Context, d auth.DeviceInfo) {
				if o.Audit != nil {
					if err := o.Audit.Insert(context.WithoutCancel(ctx), audit.Entry{At: now(),
						Principal: auth.Principal{Kind: auth.KindDevice, ID: d.ID, Name: d.Name, Via: "device"},
						Transport: reqinfo.Pair, Op: "auth:pair", Method: "POST", Status: http.StatusSeeOther}); err != nil {
						slog.Error("audit insert failed", "op", "auth:pair", "err", redact.Error(err))
					}
				}
				if o.Notify != nil {
					_ = o.Notify.Notify(context.WithoutCancel(ctx), notify.DevicePaired(d.Name))
				}
			},
		})
		mux.Handle("GET /start", pairing)
		mux.Handle("POST /start", pairing)
		mux.Handle("GET /pair", pairing)
		mux.Handle("GET /pair.js", pairing)
		mux.Handle("POST /auth/pair", pairing)
	}
	if o.Cfg.AuthMode == "local" {
		mux.Handle("/auth/", o.Sessions.Handler())
	} else {
		mux.HandleFunc("/auth/", func(w http.ResponseWriter, r *http.Request) {
			writeProblem(w, http.StatusNotFound, "NOT_FOUND", "no such resource")
		})
	}
	mux.Handle("/api/", innerH)
	mux.Handle("/human/api/", innerH)
	if o.MCP != nil {
		resource := strings.TrimRight(o.Cfg.PublicURL, "/") + "/.well-known/oauth-protected-resource/mcp"
		mcp := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if auth.FromContext(r.Context()).Kind != auth.KindAgent {
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+resource+`"`)
				writeProblem(w, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
				return
			}
			o.MCP.ServeHTTP(w, r)
		})
		mux.Handle("/mcp", mcp)
		mux.Handle("/mcp/", mcp)
	} else {
		notFound := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeProblem(w, http.StatusNotFound, "NOT_FOUND", "no such resource")
		})
		mux.Handle("/mcp", notFound)
		mux.Handle("/mcp/", notFound)
	}
	if o.Media != nil {
		mux.Handle("/media/", o.Media)
	} else {
		mux.HandleFunc("/media/", func(w http.ResponseWriter, r *http.Request) {
			writeProblem(w, http.StatusNotFound, "NOT_FOUND", "no such resource")
		})
	}
	var leaks *leakScanner
	if o.Cfg.E2E {
		leaks = newLeakScanner(o.Sec)
		mux.Handle("GET /__e2e/served", rec.handler())
		mux.Handle("GET /__e2e/leaks", leaks.handler())
	} else {
		mux.HandleFunc("/__e2e/", func(w http.ResponseWriter, r *http.Request) {
			writeProblem(w, http.StatusNotFound, "NOT_FOUND", "no such resource")
		})
	}
	mux.Handle("/", spaHandler(o.SPA))

	// A nil *DeviceStore must reach the middleware as a nil interface.
	devices := o.Devices
	if devices == nil && o.DeviceStore != nil {
		devices = o.DeviceStore
	}
	outer := auth.Middleware(o.Cfg, o.Sec, o.Tokens, o.Sessions, devices)(mux)
	if leaks != nil {
		outer = leaks.wrap(outer)
	}
	return outer, nil
}
