package runpod

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/redact"
)

// Code is the class of a failed RunPod call (C77.4). The values equal fake.Code.
type Code string

// Error codes.
const (
	EdgeBlocked    Code = "EDGE_BLOCKED"
	Unauthorized   Code = "UNAUTHORIZED"
	Forbidden      Code = "FORBIDDEN"
	NotFound       Code = "NOT_FOUND"
	EndpointPaused Code = "ENDPOINT_PAUSED"
	RateLimited    Code = "RATE_LIMITED"
	BadRequest     Code = "BAD_REQUEST"
	Upstream       Code = "UPSTREAM"
	Network        Code = "NETWORK"
	Decode         Code = "DECODE"
)

const (
	defaultRetryAfter = 5 * time.Second
	maxDetailBytes    = 500 // detail plus errors[] in total
	maxMessageDetail  = 200 // what Error() prints
)

// Error is a classified RunPod failure. It carries no URL and no key: Detail and Errors passed through
// the redactor and are cut to 500 bytes in total.
type Error struct {
	Op         string
	Status     int
	Code       Code
	Title      string
	Detail     string
	Errors     []string
	RetryAfter time.Duration
	// Timeout marks a client-side timeout (NETWORK). On Run it is ambiguous, like any NETWORK error.
	Timeout bool
	cause   error
}

// Error implements error: "runpod: <op>: <code> (<status>)[: <detail>]".
func (e *Error) Error() string {
	s := "runpod: " + e.Op + ": " + string(e.Code) + " (" + strconv.Itoa(e.Status) + ")"
	if e.Detail != "" {
		d := e.Detail
		if len(d) > maxMessageDetail {
			d = cutUTF8(d, maxMessageDetail)
		}
		s += ": " + d
	}
	return s
}

// Unwrap exposes the sentinel cause (ErrResponseTooLarge, context errors), never a URL-carrying error.
func (e *Error) Unwrap() error { return e.cause }

// Is makes access errors match provider.ErrAccess, so every layer can test errors.Is(err, ErrAccess).
func (e *Error) Is(target error) bool { return target == provider.ErrAccess && e.Access() }

// Retryable reports whether the same call may succeed later.
func (e *Error) Retryable() bool {
	switch e.Code {
	case EndpointPaused, RateLimited, Upstream, Network:
		return true
	}
	return false
}

// Ambiguous reports that the job may exist anyway (D-M3-4): Upstream, Network or a client timeout on Run.
func (e *Error) Ambiguous() bool { return e.Code == Upstream || e.Code == Network }

// Access reports a refused credential or an edge block.
func (e *Error) Access() bool {
	return e.Code == EdgeBlocked || e.Code == Unauthorized || e.Code == Forbidden
}

func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

type problem struct {
	Title  string            `json:"title"`
	Status json.RawMessage   `json:"status"`
	Detail string            `json:"detail"`
	Errors []json.RawMessage `json:"errors"`
}

// Classify turns a response (or a transport error) into an *Error; nil when the response is a 2xx and
// err is nil. r is the redactor of the calling client (a zero Redactor still masks keys and query strings).
func Classify(op string, resp *http.Response, body []byte, err error) *Error {
	return classify(redact.Redactor{}, op, resp, body, err)
}

func classify(r redact.Redactor, op string, resp *http.Response, body []byte, err error) *Error {
	if err != nil {
		return transportError(r, op, err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	e := &Error{Op: op, Status: resp.StatusCode}
	var p problem
	parsed := json.Unmarshal(body, &p) == nil
	if parsed {
		e.Title = r.String(p.Title)
		e.Detail, e.Errors = budget(r, p.Detail, p.Errors)
	} else if len(bytes.TrimSpace(body)) > 0 {
		e.Detail, _ = budget(r, string(body), nil)
	}
	lower := strings.ToLower(e.Title + " " + e.Detail)
	switch s := resp.StatusCode; {
	case s == 403 && bytes.Contains(body, []byte("error code: 1010")):
		e.Code = EdgeBlocked
	case s == 401:
		e.Code = Unauthorized
	case s == 403:
		e.Code = Forbidden
	case s == 404:
		e.Code = NotFound
	case s == 409 && strings.Contains(lower, "paused"):
		e.Code = EndpointPaused
	case s == 429:
		e.Code = RateLimited
		e.RetryAfter = retryAfter(resp.Header)
	case s >= 400 && s < 500:
		e.Code = BadRequest
	case s >= 500:
		e.Code = Upstream
	default:
		e.Code = BadRequest // unexpected 1xx/3xx: not a success
	}
	return e
}

func transportError(r redact.Redactor, op string, err error) *Error {
	e := &Error{Op: op, Code: Network}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err // drop the URL
	}
	if errors.Is(err, context.DeadlineExceeded) {
		e.Timeout = true
		e.cause = context.DeadlineExceeded
	}
	var to interface{ Timeout() bool }
	if errors.As(err, &to) && to.Timeout() {
		e.Timeout = true
	}
	e.Detail, _ = budget(r, r.Error(err), nil)
	return e
}

func decodeError(op string, status int, cause error) *Error {
	return &Error{Op: op, Status: status, Code: Decode, cause: cause}
}

// budget redacts detail and errors[] and cuts them to maxDetailBytes in total.
func budget(r redact.Redactor, detail string, raw []json.RawMessage) (string, []string) {
	left := maxDetailBytes
	detail = cutUTF8(r.String(detail), left)
	left -= len(detail)
	var errs []string
	for _, m := range raw {
		if left <= 0 {
			break
		}
		s := cutUTF8(r.String(errorEntry(m)), left)
		left -= len(s)
		errs = append(errs, s)
	}
	return detail, errs
}

// errorEntry renders one errors[] element: a string as is, an object as "pointer: detail".
func errorEntry(m json.RawMessage) string {
	var s string
	if json.Unmarshal(m, &s) == nil {
		return s
	}
	var o map[string]any
	if json.Unmarshal(m, &o) == nil {
		str := func(k string) string { v, _ := o[k].(string); return v }
		field := str("pointer")
		if field == "" {
			field = str("field")
		}
		msg := str("detail")
		if msg == "" {
			msg = str("message")
		}
		switch {
		case field != "" && msg != "":
			return field + ": " + msg
		case msg != "":
			return msg
		}
	}
	var buf bytes.Buffer
	if json.Compact(&buf, m) == nil {
		return buf.String()
	}
	return ""
}

func retryAfter(h http.Header) time.Duration {
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	best := time.Duration(-1)
	for _, w := range parseRateLimit(h.Get("RateLimit")) {
		if w.Remaining == 0 && w.Reset >= 0 && (best < 0 || w.Reset < best) {
			best = w.Reset
		}
	}
	if best >= 0 {
		return best
	}
	return defaultRetryAfter
}
