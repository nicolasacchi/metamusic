package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	mediaserve "github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/trackctx"
)

// Registration seams of the G1 program (foundations plan C324.4, C328, Revision 2). A track adds its wiring in its own
// internal/app/<track>.go with an init() that calls RegisterBuildHook (and, for media, RegisterResolvers); it never edits Build,
// httpserver.New or cmd_serve.go. Registration is init-time only and append-only.

// Loop is a background loop serve starts and drains (C324.4). Run returns when ctx ends. A panic is logged and the loop is
// restarted after a minute; it never ends serve.
type Loop struct {
	Name string
	Run  func(ctx context.Context)
}

// BuildEnv is what a build hook draws on. Services is the Services under construction: Deps is filled (a hook may set the
// fields its track added to api.Deps, which reach the HTTP server through Server.Deps) and Server is not built yet.
type BuildEnv struct {
	Ctx      context.Context
	Cfg      config.Config
	Sec      config.Secrets
	Store    *store.Store
	Data     *os.Root
	Now      func() time.Time
	NewID    func() string
	Notifier notify.Notifier
	Context  *trackctx.Builder
	Services *Services
	Options  Options
	// Subsonic is the built Subsonic handler (M5b C348): a hook may attach optional collaborators to it before it serves.
	Subsonic http.Handler

	resolvers []mediaserve.Resolver
	late      *m5Late
}

// AddLoop appends a background loop to Services.Loops.
func (e *BuildEnv) AddLoop(l Loop) { e.Services.Loops = append(e.Services.Loops, l) }

// AddResolver appends a media resolver; it is consulted after every built-in resolver, in registration order.
func (e *BuildEnv) AddResolver(r mediaserve.Resolver) { e.resolvers = append(e.resolvers, r) }

// AddTakeHook appends a take hook run inside the take transaction and after its commit, after the callbacks hook and any added
// before it (G1 C323.7). The first error rolls the take back.
func (e *BuildEnv) AddTakeHook(h jobs.TakeHook) {
	if h != nil && e.late != nil {
		e.late.hook.extra = append(e.late.hook.extra, h)
	}
}

// AddKindIngester registers the ingester of a job kind other than generate (G1 C323.6). The kind must be analyze or plan and may be
// registered once; a second registration is a wiring error that aborts Build.
func (e *BuildEnv) AddKindIngester(kind string, ki jobs.KindIngester) error {
	if kind != jobs.KindAnalyze && kind != jobs.KindPlan {
		return fmt.Errorf("app: no ingester can be registered for job kind %q", kind)
	}
	if ki == nil {
		return fmt.Errorf("app: nil ingester for job kind %q", kind)
	}
	if e.late == nil {
		return errors.New("app: build environment has no ingest wiring")
	}
	m := e.late.kindIngesters()
	if _, dup := m[kind]; dup {
		return fmt.Errorf("app: job kind %q already has an ingester", kind)
	}
	m[kind] = ki
	return nil
}

// BuildHook is one track's wiring step. An error aborts Build.
type BuildHook func(*BuildEnv) error

var (
	hookMu         sync.Mutex
	buildHooks     []BuildHook
	resolverGroups []func(*BuildEnv) []mediaserve.Resolver
)

// RegisterBuildHook appends a track's hook; hooks run in registration order at the end of Build, after Services.Deps is filled
// and before Server is built, then Options.Hooks (test injection) run last.
func RegisterBuildHook(h BuildHook) {
	hookMu.Lock()
	defer hookMu.Unlock()
	buildHooks = append(buildHooks, h)
}

// RegisterResolvers registers a track's media resolvers: f runs once per Build, after the hooks, and its resolvers are chained
// after the built-in ones.
func RegisterResolvers(f func(*BuildEnv) []mediaserve.Resolver) {
	hookMu.Lock()
	defer hookMu.Unlock()
	resolverGroups = append(resolverGroups, f)
}

// runHooks runs the registered hooks, the test hooks, then the resolver groups, and returns the extra resolvers.
func runHooks(env *BuildEnv) ([]mediaserve.Resolver, error) {
	hookMu.Lock()
	hs := append([]BuildHook(nil), buildHooks...)
	rs := append([]func(*BuildEnv) []mediaserve.Resolver(nil), resolverGroups...)
	hookMu.Unlock()
	for _, h := range append(hs, env.Options.Hooks...) {
		if err := h(env); err != nil {
			return nil, fmt.Errorf("app: build hook: %w", err)
		}
	}
	for _, f := range rs {
		env.resolvers = append(env.resolvers, f(env)...)
	}
	return env.resolvers, nil
}

// validateSystemOps surfaces a bad audit.AddSystemOps registration at startup, once per Build.
func validateSystemOps() error { return audit.ValidateSystemOps() }

// restartDelay is how long a panicked loop waits before it is started again.
const restartDelay = time.Minute

// StartLoops starts every loop in loops under wg and returns at once. Each loop runs until ctx ends; a panic is logged (the
// panic value only when it is an error or string, never a stack with secrets) and the loop restarts after restartDelay.
func StartLoops(ctx context.Context, loops []Loop, wg *sync.WaitGroup) {
	for _, l := range loops {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runLoop(ctx, l)
		}()
	}
}

func runLoop(ctx context.Context, l Loop) {
	for ctx.Err() == nil {
		if !runLoopOnce(ctx, l) {
			return
		}
		t := time.NewTimer(restartDelay)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// runLoopOnce reports true when the loop panicked (and should restart).
func runLoopOnce(ctx context.Context, l Loop) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			msg := "panic"
			switch v := r.(type) {
			case error:
				msg = redact.String(v.Error())
			case string:
				msg = redact.String(v)
			}
			slog.Error("background loop panicked; restarting in one minute", "loop", l.Name, "panic", msg)
		}
	}()
	l.Run(ctx)
	return false
}
