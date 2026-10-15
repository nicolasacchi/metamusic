// Package subsonic is the OpenSubsonic protocol adapter over the library services (C108, C109). It is not part
// of the registry (its shape is mandated by the protocol); its writes go through service-level audit.
//
// The exported API is frozen by M4a-B2. S1 implements the protocol layer (routing, envelopes, the authentication
// flow, rate limiting, audit) and the system endpoints; later tasks add endpoints through server.add.
package subsonic

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/listening"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/media/tagged"
	"github.com/nicolasacchi/metamusic/internal/playlists"
	"github.com/nicolasacchi/metamusic/internal/ratelimit"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// CoverSource renders covers; *cover.Service in production, fakes in lane tests.
type CoverSource interface {
	EnsureTrack(ctx context.Context, trackID string, size int) (serve.Object, string, error)
	EnsureAlbum(ctx context.Context, rootID string, size int) (serve.Object, string, error)
}

// DownloadSource builds tagged downloads; *tagged.Service in production, fakes in lane tests.
type DownloadSource interface {
	Ensure(ctx context.Context, trackID string) (tagged.Download, error)
}

// Options are the handler's collaborators (C108).
type Options struct {
	Cfg       config.Config
	Version   string       // serverVersion
	Store     *store.Store // read pool for catalog SQL (C109.1)
	Data      *os.Root
	Lib       *library.Service
	Judgments *judgments.Service
	Playlists *playlists.Service
	Listening *listening.Service
	Covers    CoverSource
	Downloads DownloadSource
	Creds     *auth.SubsonicStore
	Audit     *audit.Store
	Limiter   *ratelimit.Limiter
	Now       func() time.Time
	Rand      func() uint64 // getRandomSongs / getAlbumList type=random; tests inject a fixed sequence
	FailDelay time.Duration // default 1 s
	Sleep     func(ctx context.Context, d time.Duration) error
}

// limiter is the part of *ratelimit.Limiter the handler uses (an interface so tests can spy on the call order).
type limiter interface {
	Peek(key string, class ratelimit.Class) (bool, time.Duration)
	Allow(key string, classes ...ratelimit.Class) (bool, time.Duration, ratelimit.Class)
}

// verifier is the part of *auth.SubsonicStore the authentication flow uses.
type verifier interface {
	VerifyAPIKey(ctx context.Context, key, clientParam string) (auth.Principal, error)
	VerifyToken(ctx context.Context, username, token, salt, clientParam string, window time.Duration) (auth.Principal, error)
}

// endpoint is one registered protocol endpoint. write endpoints need the client's write scope and insert an audit row.
type endpoint struct {
	write bool
	fn    func(c *call) error // returns nil, *apiError or any other error (answered as error 0 "internal error")
}

// maxFailSleepers bounds the failing requests that sleep FailDelay at once (C108.3).
const maxFailSleepers = 16

type server struct {
	o       Options
	routes  map[string]endpoint
	lim     limiter  // nil => no rate limiting
	creds   verifier // nil => every authenticated endpoint answers error 0 "service unavailable"
	log     *slog.Logger
	sleep   func(ctx context.Context, d time.Duration) error
	delay   time.Duration
	now     func() time.Time
	sleeper chan struct{}
	cat     *catalog // nil => catalog endpoints answer error 0 "service unavailable"
	rand    func() uint64
	// S3 collaborators: interfaces over the Options services so lane tests can inject fakes; nil => the endpoints
	// that need them answer error 0 "service unavailable".
	judg    judger
	listen  listener
	plist   playlister
	resolve mediaResolver
	covers  CoverSource
	dls     DownloadSource
	play    *playState
}

// NewHandler returns the handler mounted by httpserver at /rest/.
func NewHandler(o Options) http.Handler { return newServer(o) }

func newServer(o Options) *server {
	s := &server{o: o, routes: map[string]endpoint{}, sleeper: make(chan struct{}, maxFailSleepers)}
	if o.Limiter != nil {
		s.lim = o.Limiter
	}
	if o.Creds != nil {
		s.creds = o.Creds
	}
	s.now = o.Now
	if s.now == nil {
		s.now = time.Now
	}
	s.sleep = o.Sleep
	if s.sleep == nil {
		s.sleep = timerSleep
	}
	s.delay = o.FailDelay
	if s.delay <= 0 {
		s.delay = time.Second
	}
	if o.Store != nil {
		s.cat = &catalog{db: o.Store.R}
	}
	s.rand = o.Rand
	if s.rand == nil {
		s.rand = rand.Uint64
	}
	s.play = newPlayState()
	if o.Judgments != nil {
		s.judg = o.Judgments
	}
	if o.Listening != nil {
		s.listen = o.Listening
	}
	if o.Playlists != nil {
		s.plist = o.Playlists
	}
	if o.Lib != nil {
		s.resolve = o.Lib
	}
	if o.Covers != nil {
		s.covers = o.Covers
	}
	if o.Downloads != nil {
		s.dls = o.Downloads
	}
	s.registerSystem()
	s.registerBrowse()
	s.registerLists()
	s.registerSearch()
	s.registerMedia()
	s.registerAnnotate()
	s.registerListens()
	s.registerBookmarks()
	s.registerPlayQueue()
	s.registerPlaylists()
	s.registerLyrics()
	s.registerStubs()
	return s
}

// add registers an endpoint; later tasks call it from their register functions.
func (s *server) add(name string, write bool, fn func(c *call) error) {
	s.routes[name] = endpoint{write: write, fn: fn}
}

func (s *server) logger() *slog.Logger {
	if s.log != nil {
		return s.log
	}
	return slog.Default()
}

// timerSleep waits d or until ctx ends.
func timerSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// statusWriter records the HTTP status for the request log.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// ServeHTTP implements C108.1 to C108.4 and C108.7.
func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	sw := &statusWriter{ResponseWriter: w}
	res := s.serve(sw, r)
	// One line per request: never the query, the body or a header value (C108.7).
	s.logger().LogAttrs(r.Context(), slog.LevelInfo, "subsonic request",
		slog.String("endpoint", res.endpoint), slog.String("status", res.status), slog.Int("code", res.code),
		slog.Int("http", sw.status), slog.String("principal", res.kind), slog.Duration("duration", time.Since(start)))
}

type result struct {
	endpoint, status, kind string
	code                   int
}

func (s *server) serve(w http.ResponseWriter, r *http.Request) (res result) {
	res = result{endpoint: "-", status: "failed"}
	failWith := func(f format, e *apiError) result {
		res.code = e.code
		s.writeError(w, f, e)
		return res
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, HEAD, POST")
		return failWith(queryFormat(r), &apiError{code: errGeneric, msg: "method not allowed", status: http.StatusMethodNotAllowed})
	}
	name, ok := endpointName(r.URL.Path)
	if !ok {
		return failWith(queryFormat(r), fail(errGeneric, "unsupported endpoint"))
	}
	params, perr := parseParams(w, r)
	if perr != nil {
		return failWith(queryFormat(r), perr)
	}
	f, ferr := paramFormat(params)
	if ferr != nil {
		return failWith(f, ferr)
	}
	ep, known := s.routes[name]
	if !known {
		return failWith(f, fail(errGeneric, "unsupported endpoint"))
	}
	res.endpoint = name
	if name != "getOpenSubsonicExtensions" {
		if params.Get("c") == "" {
			return failWith(f, fail(errMissingParam, "required parameter is missing: c"))
		}
		if e := checkVersion(params.Get("v")); e != nil {
			return failWith(f, e)
		}
	}
	c := &call{s: s, w: w, r: r, ctx: r.Context(), name: name, params: params, format: f, resp: s.newResponse()}
	if name != "getOpenSubsonicExtensions" {
		p, aerr := s.authenticate(c)
		if aerr != nil {
			return failWith(f, aerr)
		}
		c.principal = p
		res.kind = string(p.Kind)
		if ep.write && !p.Has(auth.ScopeWrite) {
			return failWith(f, fail(errNotAuthorized, "the client is not authorized to change data"))
		}
		if s.lim != nil {
			if ok, retry, _ := s.lim.Allow(p.Key(), ratelimit.Subsonic); !ok {
				return failWith(f, tooMany("rate limit exceeded", retry))
			}
		}
		c.ctx = auth.WithPrincipal(c.ctx, p)
		c.ctx = reqinfo.WithTransport(c.ctx, reqinfo.Subsonic)
		c.ctx = reqinfo.WithClient(c.ctx, "subsonic:"+p.Client)
	}
	if ep.write {
		c.ctx = audit.WithCollector(c.ctx)
	}
	err := ep.fn(c)
	var apiErr *apiError
	switch {
	case err == nil:
	case asAPIError(err, &apiErr):
	default:
		s.logger().LogAttrs(r.Context(), slog.LevelError, "subsonic internal error", slog.String("endpoint", name),
			slog.String("err", redact.Error(err)))
		apiErr = fail(errGeneric, "internal error")
	}
	if ep.write {
		s.auditWrite(c, apiErr)
	}
	if apiErr != nil {
		return failWith(f, apiErr)
	}
	res.status = "ok"
	if !c.binary {
		s.writeEnvelope(w, f, http.StatusOK, c.resp)
	}
	return res
}

func asAPIError(err error, target **apiError) bool { return errors.As(err, target) }

// call is one request in flight.
type call struct {
	s         *server
	w         http.ResponseWriter
	r         *http.Request
	ctx       context.Context
	name      string
	params    url.Values
	format    format
	principal auth.Principal
	resp      Response // handlers fill their payload field
	binary    bool     // set by media handlers that wrote the response themselves
}

// str returns the first value of name ("" if absent).
func (c *call) str(name string) string { return c.params.Get(name) }

// all returns every value of name (query values first, then form values).
func (c *call) all(name string) []string { return c.params[name] }
