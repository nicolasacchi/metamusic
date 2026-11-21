// Package objstoretest is an in-memory S3-compatible server for tests. It verifies SigV4 (query and header) with the
// same signing functions the client uses, answers S3-style XML errors, and injects 5xx answers, slowness, connection
// resets and hidden content lengths. It never logs a URL.
package objstoretest

import (
	"crypto/subtle"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/objstore"
)

// Server is one fake bucket.
type Server struct {
	ts  *httptest.Server
	cfg objstore.Config

	mu         sync.Mutex
	objects    map[string][]byte
	failN      int
	failStatus int
	failBody   string
	resetN     int
	delay      time.Duration
	hideLen    bool
	requests   int
	now        func() time.Time
}

// New starts a server and registers its Close with t. Credentials are synthetic and assembled at run time.
func New(t testing.TB) *Server {
	t.Helper()
	return newServer(t, false)
}

// NewTLS is New over TLS: the endpoint is an https URL (what config.LoadUploadFallback accepts) and HTTPClient trusts its
// certificate.
func NewTLS(t testing.TB) *Server {
	t.Helper()
	return newServer(t, true)
}

func newServer(t testing.TB, tls bool) *Server {
	t.Helper()
	s := &Server{objects: map[string][]byte{}, now: time.Now}
	if tls {
		s.ts = httptest.NewTLSServer(http.HandlerFunc(s.serve))
	} else {
		s.ts = httptest.NewServer(http.HandlerFunc(s.serve))
	}
	t.Cleanup(s.Close)
	s.cfg = objstore.Config{
		Endpoint:        s.ts.URL,
		Bucket:          "mm-test",
		Region:          "auto",
		AccessKeyID:     "AKIA" + "MMTESTFIXTURE01",
		SecretAccessKey: "mmtestsecret" + "fixture0123456789abcdefghijklmnop",
	}
	return s
}

// UseClock makes the server judge expiry and skew by now instead of the wall clock, for a rig that runs on a virtual clock.
func (s *Server) UseClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

func (s *Server) clock() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now()
}

// Config is a client configuration that matches this server.
func (s *Server) Config() objstore.Config { return s.cfg }

// HTTPClient is the test server's client.
func (s *Server) HTTPClient() *http.Client { return s.ts.Client() }

// Close stops the server (idempotent).
func (s *Server) Close() { s.ts.Close() }

// Store puts an object directly.
func (s *Server) Store(key string, b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = append([]byte(nil), b...)
}

// Has reports whether key exists.
func (s *Server) Has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.objects[key]
	return ok
}

// Object returns a copy of the object.
func (s *Server) Object(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.objects[key]
	return append([]byte(nil), b...), ok
}

// Keys lists stored keys, sorted.
func (s *Server) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.objects))
	for k := range s.objects {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Requests counts requests received (including injected failures).
func (s *Server) Requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

// FailNext answers the next n requests with status and an S3 error body (503 is SlowDown, 500 InternalError).
func (s *Server) FailNext(n, status int) {
	code := "InternalError"
	if status == 503 {
		code = "SlowDown"
	}
	s.FailNextBody(n, status, fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>injected</Message></Error>`, code))
}

// FailNextBody is FailNext with a chosen body.
func (s *Server) FailNextBody(n, status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failN, s.failStatus, s.failBody = n, status, body
}

// ResetNext closes the connection of the next n requests without answering.
func (s *Server) ResetNext(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetN = n
}

// SetDelay makes every request wait d before it is handled.
func (s *Server) SetDelay(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delay = d
}

// HideLength makes GET answers chunked, without a Content-Length.
func (s *Server) HideLength(b bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hideLen = b
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests++
	delay, hide := s.delay, s.hideLen
	reset := s.resetN > 0
	if reset {
		s.resetN--
	}
	fail := s.failN > 0
	status, body := s.failStatus, s.failBody
	if fail {
		s.failN--
	}
	s.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	if reset {
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				_ = c.Close()
			}
		}
		return
	}
	if fail {
		writeXML(w, status, body)
		return
	}
	if code, msg := s.verify(r); code != "" {
		writeErr(w, 403, code, msg)
		return
	}
	prefix := "/" + s.cfg.Bucket + "/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeErr(w, 404, "NoSuchBucket", "no such bucket")
		return
	}
	key := strings.TrimPrefix(r.URL.Path, prefix)
	switch r.Method {
	case http.MethodPut:
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		s.Store(key, b)
		w.WriteHeader(200)
	case http.MethodGet, http.MethodHead:
		b, ok := s.Object(key)
		if !ok {
			if r.Method == http.MethodHead {
				w.WriteHeader(404)
				return
			}
			writeErr(w, 404, "NoSuchKey", "no such key")
			return
		}
		if !(hide && r.Method == http.MethodGet) {
			w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		}
		w.WriteHeader(200)
		if r.Method == http.MethodGet {
			for len(b) > 0 { // small writes so a chunked answer is really streamed
				n := min(len(b), 100)
				_, _ = w.Write(b[:n])
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				b = b[n:]
			}
		}
	case http.MethodDelete:
		s.mu.Lock()
		delete(s.objects, key)
		s.mu.Unlock()
		w.WriteHeader(204)
	default:
		writeErr(w, 405, "MethodNotAllowed", "method not allowed")
	}
}

// verify checks the signature with the client's own signing functions. It returns an S3 error code, "" when valid.
func (s *Server) verify(r *http.Request) (code, msg string) {
	u := *r.URL
	u.Host = r.Host
	if r.URL.Query().Get("X-Amz-Signature") != "" {
		return s.verifyQuery(r, u)
	}
	return s.verifyHeader(r, u)
}

func (s *Server) verifyQuery(r *http.Request, u url.URL) (string, string) {
	q := r.URL.Query()
	got := q.Get("X-Amz-Signature")
	t, err := time.Parse("20060102T150405Z", q.Get("X-Amz-Date"))
	if err != nil {
		return "AuthorizationQueryParametersError", "bad X-Amz-Date"
	}
	exp, err := strconv.ParseInt(q.Get("X-Amz-Expires"), 10, 64)
	if err != nil || exp < 1 || exp > 7*24*3600 {
		return "AuthorizationQueryParametersError", "bad X-Amz-Expires"
	}
	if q.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" {
		return "AuthorizationQueryParametersError", "bad X-Amz-Algorithm"
	}
	if q.Get("X-Amz-SignedHeaders") != "host" {
		return "AuthorizationQueryParametersError", "bad X-Amz-SignedHeaders"
	}
	id, scope, _ := strings.Cut(q.Get("X-Amz-Credential"), "/")
	if id != s.cfg.AccessKeyID {
		return "InvalidAccessKeyId", "unknown access key"
	}
	if scope != t.UTC().Format("20060102")+"/"+s.cfg.Region+"/s3/aws4_request" {
		return "AuthorizationQueryParametersError", "bad credential scope"
	}
	if s.clock().After(t.Add(time.Duration(exp) * time.Second)) {
		return "AccessDenied", "request has expired"
	}
	if s.clock().Before(t.Add(-15 * time.Minute)) {
		return "AccessDenied", "request is not yet valid"
	}
	req := &http.Request{Method: r.Method, URL: &u, Header: http.Header{}}
	q.Del("X-Amz-Signature")
	req.URL.RawQuery = q.Encode()
	if err := objstore.SignQuery(req, s.cfg, t, time.Duration(exp)*time.Second); err != nil {
		return "AuthorizationQueryParametersError", "cannot verify"
	}
	want := req.URL.Query().Get("X-Amz-Signature")
	if subtle.ConstantTimeCompare([]byte(want), []byte(got)) != 1 {
		return "SignatureDoesNotMatch", "signature mismatch"
	}
	return "", ""
}

func (s *Server) verifyHeader(r *http.Request, u url.URL) (string, string) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return "AccessDenied", "anonymous access is not allowed"
	}
	rest, ok := strings.CutPrefix(auth, "AWS4-HMAC-SHA256 ")
	if !ok {
		return "AuthorizationHeaderMalformed", "unsupported algorithm"
	}
	fields := map[string]string{}
	for _, part := range strings.Split(rest, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		fields[k] = v
	}
	if !strings.HasPrefix(fields["Credential"], s.cfg.AccessKeyID+"/") {
		return "InvalidAccessKeyId", "unknown access key"
	}
	t, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil {
		return "AuthorizationHeaderMalformed", "bad x-amz-date"
	}
	if d := s.clock().Sub(t); d > 15*time.Minute || d < -15*time.Minute {
		return "RequestTimeTooSkewed", "clock skew"
	}
	req := &http.Request{Method: r.Method, URL: &u, Header: http.Header{}}
	for _, name := range strings.Split(fields["SignedHeaders"], ";") {
		if name != "host" {
			req.Header[http.CanonicalHeaderKey(name)] = r.Header.Values(name)
		}
	}
	if err := objstore.SignHeader(req, s.cfg, t, r.Header.Get("X-Amz-Content-Sha256")); err != nil {
		return "AuthorizationHeaderMalformed", "cannot verify"
	}
	if subtle.ConstantTimeCompare([]byte(req.Header.Get("Authorization")), []byte(auth)) != 1 {
		return "SignatureDoesNotMatch", "signature mismatch"
	}
	return "", ""
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(msg))
	writeXML(w, status, fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`, code, b.String()))
}

func writeXML(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body) //nolint:gosec // test server; the body is test-supplied XML
}
