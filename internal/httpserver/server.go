// Package httpserver composes auth, the Huma API, MCP and the SPA into one handler.
package httpserver

import (
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
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
	inner := http.NewServeMux()
	a := api.New(inner, api.Deps{Store: o.Store, Tokens: o.Tokens, Cfg: o.Cfg, Version: o.Version, Commit: o.Commit, Started: started,
		Library: o.Library, Signer: o.Signer, Probe: o.Probe, Now: o.Now})
	if o.MCP == nil && o.MCPFactory != nil {
		m, err := o.MCPFactory(a, inner)
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
	var innerH http.Handler = inner
	if o.Cfg.E2E {
		innerH = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isSPARequest(r) {
				inner.ServeHTTP(w, r)
				return
			}
			_, pat := inner.Handler(r)
			sw := &statusWriter{ResponseWriter: w}
			inner.ServeHTTP(sw, r)
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
	if o.Cfg.E2E {
		mux.Handle("GET /__e2e/served", rec.handler())
	}
	mux.Handle("/", spaHandler(o.SPA))

	return auth.Middleware(o.Cfg, o.Sec, o.Tokens, o.Sessions)(mux), nil
}
