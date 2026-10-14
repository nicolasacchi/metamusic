package runpod

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/redact"
)

// Production hosts (C77.1).
const (
	DefaultMgmtBase = "https://api.runpod.io"
	DefaultJobsBase = "https://api.runpod.ai"
)

// Limits and timeouts (C77.1).
const (
	RunTimeout     = 60 * time.Second // short /run timeouts create ghosts under load
	BillingTimeout = 30 * time.Second
	OtherTimeout   = 15 * time.Second
	MaxRequestBody = 64 << 10 // every request except /run
	MaxResponse    = 1 << 20
	maxGETRetries  = 3
)

// Sentinel errors.
var (
	// ErrResponseTooLarge: a response exceeded MaxResponse.
	ErrResponseTooLarge = errors.New("runpod: response too large")
	// ErrRequestTooLarge: a request body exceeded its limit.
	ErrRequestTooLarge = errors.New("runpod: request body too large")
)

// KeyKind names the credential used for one call (C77.3).
type KeyKind string

// Key kinds.
const (
	KeyJobs  KeyKind = "jobs"
	KeyRead  KeyKind = "read"
	KeyWrite KeyKind = "write"
	KeyAdmin KeyKind = "admin"
)

// Exchange is one request/response pair handed to a Recorder (C77.7). Bodies are raw: the recorder
// redacts. No Authorization header is ever included.
type Exchange struct {
	Op, Method, PathTemplate string
	Status                   int
	Header                   http.Header // response headers
	Request, Response        []byte
}

// Recorder receives every client exchange (MM_RUNPOD_RECORD_DIR, C77.7).
type Recorder interface{ Record(Exchange) }

// Timeouts override the C77.1 timeouts (tests only; zero values mean the defaults).
type Timeouts struct{ Run, Billing, Other time.Duration }

// ClientOptions builds a Client.
type ClientOptions struct {
	MgmtBase, JobsBase string // tests point both at runpodtest
	HTTP               *http.Client
	UserAgent          string
	Keys               config.RunPodKeys
	Admin              *config.AdminKey // nil in serve mode: CreateEndpoint returns ErrNotPermitted
	Clock              clock.Clock
	Recorder           Recorder // nil => none
	Timeouts           Timeouts
}

// Client is the RunPod REST v2 client. It is safe for concurrent use.
type Client struct {
	o   ClientOptions
	hc  *http.Client
	red redact.Redactor

	mgmt, jobs limiter

	mu   sync.Mutex
	skew *time.Duration
}

// UserAgentFor renders the C77.1 User-Agent for a version.
func UserAgentFor(version string) string {
	return "metamusic/" + version + " (+https://github.com/nicolasacchi/metamusic)"
}

// NewClient builds the client.
func NewClient(o ClientOptions) *Client {
	if o.MgmtBase == "" {
		o.MgmtBase = DefaultMgmtBase
	}
	if o.JobsBase == "" {
		o.JobsBase = DefaultJobsBase
	}
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	if o.UserAgent == "" {
		o.UserAgent = UserAgentFor("dev")
	}
	hc := &http.Client{}
	if o.HTTP != nil {
		cp := *o.HTTP
		hc = &cp
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{o: o, hc: hc, red: redact.WithEndpoint(o.Keys.EndpointID)}
}

// Skew returns the latest measured offset of RunPod's Date header against the clock (server - local).
func (c *Client) Skew() (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.skew == nil {
		return 0, false
	}
	return *c.skew, true
}

// Redactor returns the client's redactor (endpoint id aware).
func (c *Client) Redactor() redact.Redactor { return c.red }

func (c *Client) token(k KeyKind) (string, error) {
	var t string
	switch k {
	case KeyJobs:
		t = c.o.Keys.Jobs
	case KeyRead:
		t = c.o.Keys.Read
	case KeyWrite:
		t = c.o.Keys.Write
	case KeyAdmin:
		if c.o.Admin != nil {
			t = c.o.Admin.Value()
		}
	}
	if t == "" {
		return "", provider.ErrNotPermitted
	}
	return t, nil
}

func (c *Client) endpointPath(rest string) string {
	return "/v2/" + url.PathEscape(c.o.Keys.EndpointID) + rest
}

type call struct {
	op       string
	method   string
	jobsHost bool
	path     string
	template string // recorder path template; defaults to path
	query    url.Values
	key      KeyKind
	body     []byte
	timeout  time.Duration
	maxBody  int
	retry    bool // GETs only
}

func (c *Client) timeoutFor(k call) time.Duration {
	t := k.timeout
	switch {
	case k.op == "run" && c.o.Timeouts.Run > 0:
		return c.o.Timeouts.Run
	case k.timeout == BillingTimeout && c.o.Timeouts.Billing > 0:
		return c.o.Timeouts.Billing
	case k.timeout == OtherTimeout && c.o.Timeouts.Other > 0:
		return c.o.Timeouts.Other
	}
	return t
}

// do performs one logical call (with GET retries) and returns the 2xx body.
func (c *Client) do(ctx context.Context, k call) ([]byte, error) {
	tok, err := c.token(k.key)
	if err != nil {
		return nil, err
	}
	if len(k.body) > k.maxBody {
		return nil, ErrRequestTooLarge
	}
	lim, base := &c.mgmt, c.o.MgmtBase
	if k.jobsHost {
		lim, base = &c.jobs, c.o.JobsBase
	}
	target := strings.TrimRight(base, "/") + k.path
	if len(k.query) > 0 {
		target += "?" + k.query.Encode()
	}
	for attempt := 0; ; attempt++ {
		body, e := c.once(ctx, lim, k, target, tok)
		if e == nil {
			return body, nil
		}
		var re *Error
		if !errors.As(e, &re) {
			return nil, e // context cancellation, never wrapped with a URL
		}
		if !k.retry || !re.Retryable() || attempt >= maxGETRetries {
			return nil, re
		}
		d := re.RetryAfter
		if d <= 0 {
			d = time.Second << attempt // 1 s, 2 s, 4 s
		}
		if err := sleep(ctx, c.o.Clock, d); err != nil {
			return nil, err
		}
	}
}

func (c *Client) once(ctx context.Context, lim *limiter, k call, target, tok string) ([]byte, error) {
	if err := lim.wait(ctx, c.o.Clock); err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, c.timeoutFor(k))
	defer cancel()
	var rdr io.Reader
	if k.body != nil {
		rdr = io.NopCloser(bytes.NewReader(k.body)) // no automatic GetBody: a /run must never be replayed
	}
	req, err := http.NewRequestWithContext(rctx, k.method, target, rdr)
	if err != nil {
		return nil, &Error{Op: k.op, Code: Network, Detail: "invalid request"}
	}
	req.GetBody = nil
	if k.body != nil {
		req.ContentLength = int64(len(k.body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("User-Agent", c.o.UserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, c.scrubKeys(classify(c.red, k.op, nil, nil, err))
	}
	defer resp.Body.Close()
	raw, rerr := io.ReadAll(io.LimitReader(resp.Body, MaxResponse+1))
	if rerr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, c.scrubKeys(classify(c.red, k.op, nil, nil, rerr))
	}
	now := c.o.Clock.Now()
	c.observeDate(resp, now)
	lim.observe(now, resp.Header)
	if len(raw) > MaxResponse {
		return nil, decodeError(k.op, resp.StatusCode, ErrResponseTooLarge)
	}
	if c.o.Recorder != nil {
		tpl := k.template
		if tpl == "" {
			tpl = k.path
		}
		c.o.Recorder.Record(Exchange{Op: k.op, Method: k.method, PathTemplate: tpl, Status: resp.StatusCode,
			Header: resp.Header.Clone(), Request: k.body, Response: raw})
	}
	if e := classify(c.red, k.op, resp, raw, nil); e != nil {
		return nil, c.scrubKeys(e)
	}
	return raw, nil
}

// scrubKeys masks the configured key values in an error, in case a server or a proxy echoes one back.
func (c *Client) scrubKeys(e *Error) *Error {
	if e == nil {
		return nil
	}
	mask := func(s string) string {
		for _, k := range []string{c.o.Keys.Jobs, c.o.Keys.Read, c.o.Keys.Write, c.adminValue()} {
			if len(k) >= 4 {
				s = strings.ReplaceAll(s, k, "<key>")
			}
		}
		return s
	}
	e.Title, e.Detail = mask(e.Title), mask(e.Detail)
	for i := range e.Errors {
		e.Errors[i] = mask(e.Errors[i])
	}
	return e
}

func (c *Client) adminValue() string {
	if c.o.Admin == nil {
		return ""
	}
	return c.o.Admin.Value()
}

func (c *Client) observeDate(resp *http.Response, now time.Time) {
	d := resp.Header.Get("Date")
	if d == "" {
		return
	}
	t, err := http.ParseTime(d)
	if err != nil {
		return
	}
	s := t.Sub(now)
	c.mu.Lock()
	c.skew = &s
	c.mu.Unlock()
}

// decode parses a 2xx body with numbers kept as json.Number; failure is a DECODE error.
func decode(op string, body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return decodeError(op, 200, nil)
	}
	return nil
}
