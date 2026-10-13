// Package gate is the request gate wrapped around the Huma mux (C29, D-M2a-2):
// audit recorder, authorize, rate limit, idempotency, then the handler.
package gate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/idem"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/ratelimit"
	"github.com/nicolasacchi/metamusic/internal/redact"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
)

// Options configures New.
type Options struct {
	API     huma.API
	Inner   http.Handler // must be the *http.ServeMux the API was registered on (pattern lookup)
	Audit   *audit.Store
	Idem    *idem.Store
	Limiter *ratelimit.Limiter
	Now     func() time.Time
	// Ticker drives idempotency lease refresh; nil means time.NewTicker. Tests tick by hand.
	Ticker func(d time.Duration) (c <-chan time.Time, stop func())
	// MaxBody bounds what the gate buffers (default 8 MiB).
	MaxBody int64
}

const (
	defaultMaxBody = 8 << 20
	maxStoredBody  = 1 << 20
	headBytes      = 4 << 10
	auditTimeout   = 2 * time.Second
)

type patternLookup interface {
	Handler(*http.Request) (http.Handler, string)
}

type gate struct {
	Options
	mux patternLookup
	ops map[string]api.OpInfo
}

// New wraps o.Inner. It panics when Inner is not a pattern-aware mux, which
// is a wiring error caught by any test.
func New(o Options) http.Handler {
	mux, ok := o.Inner.(patternLookup)
	if !ok {
		panic("gate: Inner must be an *http.ServeMux")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.MaxBody <= 0 {
		o.MaxBody = defaultMaxBody
	}
	if o.Ticker == nil {
		o.Ticker = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	ops := map[string]api.OpInfo{}
	for _, op := range api.Operations(o.API) {
		ops[op.Method+" "+op.Path] = op
	}
	return &gate{Options: o, mux: mux, ops: ops}
}

// recorder captures status, the head of the body (for error codes) and, when
// capture is on, the whole body up to maxStoredBody.
type recorder struct {
	http.ResponseWriter
	status      int
	contentType string
	head        bytes.Buffer
	capture     bool
	body        bytes.Buffer
	overflow    bool
}

func (w *recorder) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
		w.contentType = w.Header().Get("Content-Type")
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *recorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if room := headBytes - w.head.Len(); room > 0 {
		w.head.Write(b[:min(room, len(b))])
	}
	if w.capture && !w.overflow {
		if w.body.Len()+len(b) > maxStoredBody {
			w.overflow = true
			w.body.Reset()
		} else {
			w.body.Write(b)
		}
	}
	return w.ResponseWriter.Write(b)
}

func (w *recorder) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *recorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (g *gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := g.Now()
	ctx := r.Context()
	p := auth.FromContext(ctx)

	_, pattern := g.mux.Handler(r)
	info, matched := g.ops[pattern]
	opID := "-"
	if matched {
		opID = info.ID
	}

	tr, ok := reqinfo.TransportFrom(ctx)
	if !ok {
		tr = reqinfo.REST
		if reqinfo.IsSPARequest(r) && (p.Kind == auth.KindHuman || p.Kind == auth.KindDevice) {
			tr = reqinfo.SPA
		}
	}
	client := reqinfo.Client(ctx)
	if client == "" {
		client = reqinfo.SanitizeClient(r.UserAgent())
	}
	ctx = audit.WithCollector(reqinfo.WithClient(reqinfo.WithTransport(ctx, tr), client))

	audited := p.Kind == auth.KindAgent ||
		(matched && (info.Meta.HumanOnly || info.Meta.Scope != auth.ScopeRead))
	create := matched && info.Meta.Idempotency == "create"

	rec := &recorder{ResponseWriter: w}
	entry := audit.Entry{At: start, Principal: p, Transport: tr, Client: client, Op: opID, Method: r.Method,
		Idempotency: audit.IdemNone}

	defer func() {
		rv := recover()
		status := rec.status
		if rv != nil && status == 0 {
			status = http.StatusInternalServerError
		}
		if status == 0 {
			status = http.StatusOK
		}
		entry.Status = status
		entry.Duration = g.Now().Sub(start)
		if status >= 400 {
			entry.ErrorCode = problemCode(rec.head.Bytes())
		}
		entry.CreatedIDs = audit.Created(ctx)
		if audited && g.Audit != nil {
			actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditTimeout)
			if err := g.Audit.Insert(actx, entry); err != nil {
				slog.Error("audit insert failed", "op", opID, "err", redact.Error(err))
			}
			cancel()
		}
		slog.Info("request", "method", r.Method, "path", r.URL.Path, "op", opID, "status", status,
			"principal", string(p.Kind), "duration_ms", entry.Duration.Milliseconds())
		if rv != nil {
			panic(rv)
		}
	}()

	// 2. Rate limit: charged before the body is read, the unmatched pass-through and Authorize,
	// so unknown paths and refused calls cannot grow the audit log without bound.
	if g.Limiter != nil && p.Kind != auth.KindAnonymous {
		classes := []ratelimit.Class{ratelimit.Human}
		heavy := ratelimit.HumanHeavy
		if p.Kind == auth.KindAgent {
			classes, heavy = []ratelimit.Class{ratelimit.Agent}, ratelimit.AgentHeavy
		}
		if matched && info.Meta.Rate == "heavy" {
			classes = append(classes, heavy)
		}
		if ok, retry, bucket := g.Limiter.Allow(p.Key(), classes...); !ok {
			secs := int(retry / time.Second)
			rec.Header().Set("Retry-After", strconv.Itoa(secs))
			problem.Write(rec, http.StatusTooManyRequests, "RATE_LIMITED", "rate limit exceeded",
				map[string]any{"retry_after_s": secs, "bucket": string(bucket)})
			return
		}
	}

	var body []byte
	if (audited || create) && r.Body != nil && r.Body != http.NoBody {
		r.Body = http.MaxBytesReader(rec, r.Body, g.MaxBody)
		b, err := io.ReadAll(r.Body)
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				problem.Write(rec, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "request body too large",
					map[string]any{"limit_bytes": g.MaxBody})
				return
			}
			problem.Write(rec, http.StatusBadRequest, "BAD_REQUEST", "could not read request body", nil)
			return
		}
		body = b
		r.Body = io.NopCloser(bytes.NewReader(b))
	}
	r = r.WithContext(ctx)
	if audited || create {
		sha, _ := idem.RequestSHA(opID, r.Method, r.URL.EscapedPath(), r.URL.Query(), body)
		entry.ArgsSHA256 = sha
	}

	// 3. No operation matched: the mux's own not-found handler answers.
	if !matched {
		g.Inner.ServeHTTP(rec, r)
		return
	}

	// 4. Authorize.
	if err := api.Authorize(ctx, info.Meta); err != nil {
		var pr *api.Problem
		if !errors.As(err, &pr) {
			pr = &api.Problem{Status: 500, Code: "INTERNAL", Message: "internal error"}
		}
		problem.Write(rec, pr.Status, pr.Code, pr.Message, pr.Details)
		return
	}

	// 6. Idempotency.
	if !create || g.Idem == nil {
		g.Inner.ServeHTTP(rec, r)
		return
	}
	g.serveIdempotent(rec, r, info, p, &entry, body)
}

func (g *gate) serveIdempotent(rec *recorder, r *http.Request, info api.OpInfo, p auth.Principal, entry *audit.Entry, body []byte) {
	ctx := r.Context()
	key := r.Header.Get("Idempotency-Key")
	derivedKey, hasDerived := idem.DerivedFrom(ctx)
	derived := false
	switch {
	case key == "" && hasDerived:
		key, derived = derivedKey, true
	case key == "":
		entry.Idempotency = audit.IdemMissing
		problem.Write(rec, http.StatusPreconditionRequired, "IDEMPOTENCY_KEY_REQUIRED",
			"this operation requires an Idempotency-Key header", nil)
		return
	case hasDerived && key == derivedKey:
		derived = true
	}
	if !validKey(key) {
		problem.Write(rec, http.StatusUnprocessableEntity, "VALIDATION_FAILED",
			"Idempotency-Key must be 1-255 printable ASCII characters without spaces",
			map[string]any{"field": "Idempotency-Key"})
		return
	}

	pk := p.Key()
	begun, err := g.Idem.Begin(ctx, pk, key, info.ID, entry.ArgsSHA256, derived)
	if err != nil {
		slog.Error("idempotency begin failed", "op", info.ID, "err", redact.Error(err))
		problem.Write(rec, http.StatusInternalServerError, "INTERNAL", "internal error", nil)
		return
	}
	switch begun.Outcome {
	case idem.Replay:
		entry.Idempotency = audit.IdemReplayed
		if begun.ContentType != "" {
			rec.Header().Set("Content-Type", begun.ContentType)
		}
		rec.Header().Set("Idempotent-Replayed", "true")
		rec.WriteHeader(begun.Status)
		_, _ = rec.Write(begun.Body)
		return
	case idem.Conflict:
		entry.Idempotency = audit.IdemConflict
		problem.Write(rec, http.StatusConflict, "IDEMPOTENCY_CONFLICT",
			"this Idempotency-Key was used with a different request", map[string]any{"op": info.ID})
		return
	case idem.InProgress:
		entry.Idempotency = audit.IdemInProgress
		rec.Header().Set("Retry-After", "1")
		problem.Write(rec, http.StatusConflict, "IDEMPOTENCY_IN_PROGRESS",
			"a request with this Idempotency-Key is still running", nil)
		return
	}

	// Proceed: keep the lease alive while the handler runs.
	bg := context.WithoutCancel(ctx)
	ticks, stopTicker := g.Ticker(idem.Lease / 2)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ticks:
				if err := g.Idem.Extend(bg, pk, key); err != nil {
					slog.Error("idempotency extend failed", "err", redact.Error(err))
				}
			case <-stop:
				return
			}
		}
	}()
	finished := false
	defer func() {
		close(stop)
		<-done
		stopTicker()
		if !finished { // panic
			_ = g.Idem.Release(bg, pk, key)
		}
	}()

	rec.capture = true
	g.Inner.ServeHTTP(rec, r)
	finished = true

	status := rec.status
	if status == 0 {
		status = http.StatusOK
	}
	if status >= 200 && status < 300 && !rec.overflow {
		stored := idem.Redact(rec.body.Bytes(), info.Meta.SecretFields)
		err := g.Idem.Complete(bg, pk, key, status, rec.contentType, stored)
		if err == nil {
			entry.Idempotency = audit.IdemStored
			return
		}
		slog.Error("idempotency complete failed", "op", info.ID, "err", redact.Error(err))
	}
	if err := g.Idem.Release(bg, pk, key); err != nil {
		slog.Error("idempotency release failed", "op", info.ID, "err", redact.Error(err))
	}
}

func validKey(k string) bool {
	if len(k) < 1 || len(k) > 255 {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] < '!' || k[i] > '~' {
			return false
		}
	}
	return true
}

func problemCode(head []byte) string {
	var v struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(head, &v) != nil {
		return ""
	}
	return v.Code
}
