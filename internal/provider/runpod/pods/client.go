package pods

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/redact"
)

// Timeouts override the C392.1 timeouts (tests only; zero values mean the defaults).
type Timeouts struct{ Create, Other, Logs time.Duration }

// Options builds a Client.
type Options struct {
	BaseURL   string // default DefaultBase; tests point it at podtest
	HTTP      *http.Client
	UserAgent string // default UserAgentFor("dev")
	// KeyDir is the secrets directory: the pod key is read from <KeyDir>/runpod-pod.env at every call, never cached.
	KeyDir   string
	Clock    clock.Clock
	Timeouts Timeouts
}

// Client is the RunPod v2 pod client. It is safe for concurrent use.
type Client struct {
	o   Options
	hc  *http.Client
	lim limiter
}

var _ API = (*Client)(nil)

// UserAgentFor renders the C392.1 User-Agent for a version.
func UserAgentFor(version string) string {
	return "metamusic/" + version + " (+https://github.com/nicolasacchi/metamusic)"
}

// NewClient builds the client. It never follows a redirect.
func NewClient(o Options) *Client {
	if o.BaseURL == "" {
		o.BaseURL = DefaultBase
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
	return &Client{o: o, hc: hc}
}

type call struct {
	op      string
	method  string
	path    string
	query   url.Values
	body    []byte
	timeout time.Duration
	retry   bool   // GETs only
	stream  bool   // server-sent events: a deadline while reading ends the call without an error
	accept  string // default application/json
}

// reply is an answered HTTP exchange of any status. detail is the redacted problem text of a non-2xx answer.
type reply struct {
	status int
	header http.Header
	body   []byte
	detail string
}

func (r reply) ok() bool { return r.status >= 200 && r.status < 300 }

// transportError is a call that got no answer (reset, timeout, cut body). It carries no URL.
type transportError struct {
	detail string
	cause  error
}

func (e *transportError) Error() string { return "pods: transport failure: " + e.detail }
func (e *transportError) Unwrap() error { return e.cause }

func (c *Client) timeoutFor(k call) time.Duration {
	switch {
	case k.stream && c.o.Timeouts.Logs > 0:
		return c.o.Timeouts.Logs
	case k.stream:
		return LogsReadTimeout
	case k.timeout == CreateTimeout && c.o.Timeouts.Create > 0:
		return c.o.Timeouts.Create
	case k.timeout == OtherTimeout && c.o.Timeouts.Other > 0:
		return c.o.Timeouts.Other
	}
	return k.timeout
}

// key reads the pod key now. An absent file is ErrNoKey; a refused file keeps config's message (file name, never a value).
func (c *Client) key() (string, error) {
	k, ok, err := config.LoadPodKey(c.o.KeyDir)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrNoKey
	}
	return k.Value(), nil
}

// do performs one logical call: GETs are retried up to three times on 5xx, 429 and transport failures; everything else is sent once.
// It returns any answered exchange, or a key error, a context error or a *transportError.
func (c *Client) do(ctx context.Context, k call) (reply, error) {
	if len(k.body) > MaxRequestBody {
		return reply{}, fmt.Errorf("%w: request body over %d bytes", ErrBadBody, MaxRequestBody)
	}
	for attempt := 0; ; attempt++ {
		rep, err := c.once(ctx, k)
		retryable := false
		var wait time.Duration
		switch {
		case err != nil:
			var te *transportError
			retryable = errors.As(err, &te)
		case rep.status == http.StatusTooManyRequests || rep.status >= 500:
			retryable, wait = true, retryAfter(rep.header)
		}
		if !k.retry || !retryable || attempt >= maxGETRetries {
			return rep, err
		}
		if wait <= 0 {
			wait = time.Second << attempt // 1 s, 2 s, 4 s
		}
		if err := sleep(ctx, c.o.Clock, wait); err != nil {
			return reply{}, err
		}
	}
}

func (c *Client) once(ctx context.Context, k call) (reply, error) {
	tok, err := c.key()
	if err != nil {
		return reply{}, err
	}
	if err := c.lim.wait(ctx, c.o.Clock); err != nil {
		return reply{}, err
	}
	target := strings.TrimRight(c.o.BaseURL, "/") + k.path
	if len(k.query) > 0 {
		target += "?" + k.query.Encode()
	}
	rctx, cancel := context.WithTimeout(ctx, c.timeoutFor(k))
	defer cancel()
	var rdr io.Reader
	if k.body != nil {
		rdr = io.NopCloser(bytes.NewReader(k.body)) // no automatic GetBody: a create must never be replayed
	}
	req, err := http.NewRequestWithContext(rctx, k.method, target, rdr)
	if err != nil {
		return reply{}, &transportError{detail: "invalid request"}
	}
	req.GetBody = nil
	if k.body != nil {
		req.ContentLength = int64(len(k.body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("User-Agent", c.o.UserAgent)
	accept := k.accept
	if accept == "" {
		accept = "application/json"
	}
	req.Header.Set("Accept", accept)

	resp, err := c.hc.Do(req)
	if err != nil {
		return reply{}, c.transport(ctx, err, tok)
	}
	defer resp.Body.Close()
	raw, rerr := io.ReadAll(io.LimitReader(resp.Body, MaxResponse+1))
	if rerr != nil && !(k.stream && ctx.Err() == nil && rctx.Err() != nil) {
		return reply{}, c.transport(ctx, rerr, tok)
	}
	c.lim.observe(c.o.Clock.Now(), resp.Header.Get("RateLimit"))
	if len(raw) > MaxResponse {
		if !k.stream {
			return reply{}, &APIError{Op: k.op, Status: resp.StatusCode, Detail: "response too large"}
		}
		raw = raw[:MaxResponse]
	}
	rep := reply{status: resp.StatusCode, header: resp.Header, body: raw}
	if !rep.ok() {
		rep.detail = problemDetail(raw, tok)
	}
	return rep, nil
}

// transport turns a failed exchange into a context error (the caller gave up) or a URL-free *transportError.
func (c *Client) transport(ctx context.Context, err error, tok string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err // drop the URL
	}
	te := &transportError{detail: maskKey(redact.Error(err), tok)}
	if errors.Is(err, context.DeadlineExceeded) {
		te.cause = context.DeadlineExceeded
	}
	return te
}

func maskKey(s, tok string) string {
	if len(tok) >= 4 {
		s = strings.ReplaceAll(s, tok, "<key>")
	}
	return s
}

const maxDetailBytes = 500

// problemDetail renders the problem+json of an error answer (title, detail, errors[]) redacted and cut to 500 bytes.
func problemDetail(body []byte, tok string) string {
	var p struct {
		Title  string            `json:"title"`
		Detail string            `json:"detail"`
		Errors []json.RawMessage `json:"errors"`
	}
	text := ""
	if json.Unmarshal(body, &p) == nil {
		parts := []string{p.Detail}
		if p.Detail == "" {
			parts = []string{p.Title}
		}
		for _, e := range p.Errors {
			var s string
			if json.Unmarshal(e, &s) != nil {
				var buf bytes.Buffer
				if json.Compact(&buf, e) == nil {
					s = buf.String()
				}
			}
			parts = append(parts, s)
		}
		text = strings.Join(parts, "; ")
	} else {
		text = strings.TrimSpace(string(body))
	}
	return cutUTF8(maskKey(redact.String(text), tok), maxDetailBytes)
}

func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

func retryAfter(h http.Header) time.Duration {
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	best := time.Duration(-1)
	for _, w := range parseRateLimit(h.Get("RateLimit")) {
		if w.remaining == 0 && w.reset >= 0 && (best < 0 || w.reset < best) {
			best = w.reset
		}
	}
	if best >= 0 {
		return best
	}
	return 5 * time.Second
}

// failure maps a non-2xx answer (or a failed exchange) of a plain call to its error; callers handle their special statuses first.
func failure(op string, rep reply, err error) error {
	if err != nil {
		return err
	}
	switch rep.status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &accessError{op: op, status: rep.status}
	case http.StatusTooManyRequests:
		return &ErrRateLimited{RetryAfter: retryAfter(rep.header)}
	}
	return &APIError{Op: op, Status: rep.status, Detail: rep.detail}
}

// ambiguous maps what a never-replayed call may return when the effect is unknown.
func ambiguous(op string, rep reply, err error) error {
	if err == nil {
		return &ErrAmbiguous{Op: op, Status: rep.status, Detail: rep.detail}
	}
	var te *transportError
	if errors.As(err, &te) {
		return &ErrAmbiguous{Op: op, Detail: te.detail, Cause: te.cause}
	}
	var ae *APIError
	if errors.As(err, &ae) { // an answer too large to read: the effect happened
		return &ErrAmbiguous{Op: op, Status: ae.Status, Detail: ae.Detail}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &ErrAmbiguous{Op: op, Cause: err} // the request may have been sent
	}
	return err // no key, bad body: nothing was sent
}

func checkID(id string) error {
	if !podIDRE.MatchString(id) {
		return errors.New("pods: invalid pod id")
	}
	return nil
}

// CreatePod creates one pod. It is sent once: 5xx, a timeout or a reset is *ErrAmbiguous and the caller sweeps by name.
func (c *Client) CreatePod(ctx context.Context, b CreateBody) (Pod, error) {
	body, err := json.Marshal(b)
	if err != nil {
		return Pod{}, err
	}
	rep, err := c.do(ctx, call{op: "create_pod", method: http.MethodPost, path: "/v2/pods", body: body, timeout: CreateTimeout})
	if err != nil {
		return Pod{}, ambiguous("create_pod", rep, err)
	}
	switch s := rep.status; {
	case s == http.StatusCreated:
		var w podWire
		if decodeJSON(rep.body, &w) != nil || w.ID == "" {
			return Pod{}, &ErrAmbiguous{Op: "create_pod", Status: s, Detail: "unreadable reply"} // the pod exists; the sweep finds it by name
		}
		p, perr := w.pod()
		if perr != nil {
			return Pod{ID: w.ID, Name: w.Name, Status: w.Status}, &ErrAmbiguous{Op: "create_pod", Status: s, Detail: perr.Error()}
		}
		return p, nil
	case s == http.StatusAccepted:
		return Pod{}, c.unexpectedQueued(ctx, rep)
	case s == http.StatusBadRequest:
		return Pod{}, &ErrRefused{Status: s, Capacity: true, Detail: rep.detail}
	case s == http.StatusForbidden || s == http.StatusConflict:
		return Pod{}, &ErrRefused{Status: s, Detail: rep.detail}
	case s == http.StatusPaymentRequired:
		return Pod{}, ErrBalance
	case s == http.StatusUnprocessableEntity:
		return Pod{}, fmt.Errorf("%w%s", ErrInvalid, detailSuffix(rep.detail))
	case s == http.StatusTooManyRequests:
		return Pod{}, &ErrRateLimited{RetryAfter: retryAfter(rep.header)}
	case s >= 500:
		return Pod{}, &ErrAmbiguous{Op: "create_pod", Status: s, Detail: rep.detail}
	}
	return Pod{}, failure("create_pod", rep, nil)
}

// unexpectedQueued handles a 202: the queue entry is cancelled, and so is the pod it may already have resolved to. The error is
// ErrQueued whatever the cleanup did; a failed cleanup is appended.
func (c *Client) unexpectedQueued(ctx context.Context, rep reply) error {
	var q struct {
		ID    string  `json:"id"`
		PodID *string `json:"podId"`
	}
	if decodeJSON(rep.body, &q) != nil || q.ID == "" {
		return fmt.Errorf("%w: reply unreadable, the name sweep must delete the pod", ErrQueued)
	}
	var problems []string
	if err := c.CancelQueued(ctx, q.ID); err != nil {
		problems = append(problems, "cancel: "+err.Error())
	}
	if q.PodID != nil && *q.PodID != "" {
		if err := c.DeletePod(ctx, *q.PodID); err != nil {
			problems = append(problems, "delete: "+err.Error())
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w; cleanup failed (%s)", ErrQueued, strings.Join(problems, "; "))
	}
	return ErrQueued
}

// GetPod reads one pod.
func (c *Client) GetPod(ctx context.Context, id string) (Pod, error) {
	if err := checkID(id); err != nil {
		return Pod{}, err
	}
	rep, err := c.do(ctx, call{op: "get_pod", method: http.MethodGet, path: "/v2/pods/" + url.PathEscape(id), timeout: OtherTimeout, retry: true})
	if err != nil || !rep.ok() {
		if err == nil && rep.status == http.StatusNotFound {
			return Pod{}, ErrPodGone
		}
		return Pod{}, failure("get_pod", rep, err)
	}
	var w podWire
	if decodeJSON(rep.body, &w) != nil {
		return Pod{}, &APIError{Op: "get_pod", Status: rep.status, Detail: "unreadable reply"}
	}
	p, perr := w.pod()
	if perr != nil {
		return Pod{}, &APIError{Op: "get_pod", Status: rep.status, Detail: perr.Error()}
	}
	if p.Status == StatusTerminated {
		return p, ErrPodGone
	}
	return p, nil
}

// ListPods lists every pod of the account (all pages, newest first). RunPod has no name filter: the caller keeps the mm- prefix.
func (c *Client) ListPods(ctx context.Context) ([]Pod, error) {
	var out []Pod
	cursor := ""
	for page := 0; ; page++ {
		if page >= maxListPages {
			return nil, &APIError{Op: "list_pods", Detail: "too many pages"}
		}
		q := url.Values{"limit": {"1000"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		rep, err := c.do(ctx, call{op: "list_pods", method: http.MethodGet, path: "/v2/pods", query: q, timeout: OtherTimeout, retry: true})
		if err != nil || !rep.ok() {
			return nil, failure("list_pods", rep, err)
		}
		var r struct {
			Pods       []podWire `json:"pods"`
			Pagination struct {
				HasNextPage bool    `json:"hasNextPage"`
				NextCursor  *string `json:"nextCursor"`
			} `json:"pagination"`
		}
		if decodeJSON(rep.body, &r) != nil {
			return nil, &APIError{Op: "list_pods", Status: rep.status, Detail: "unreadable reply"}
		}
		for _, w := range r.Pods {
			p, perr := w.pod()
			if perr != nil {
				return nil, &APIError{Op: "list_pods", Status: rep.status, Detail: perr.Error()}
			}
			out = append(out, p)
		}
		if !r.Pagination.HasNextPage {
			return out, nil
		}
		next := ""
		if r.Pagination.NextCursor != nil {
			next = *r.Pagination.NextCursor
		}
		if next == "" || next == cursor {
			return nil, &APIError{Op: "list_pods", Status: rep.status, Detail: "pagination does not advance"}
		}
		cursor = next
	}
}

// DeletePod terminates a pod: sent once, 204 and 404 are success, an unknown outcome is *ErrAmbiguous.
func (c *Client) DeletePod(ctx context.Context, id string) error {
	return c.deleteOnce(ctx, "delete_pod", "/v2/pods/", id)
}

// CancelQueued cancels a queue entry (only after an unexpected 202).
func (c *Client) CancelQueued(ctx context.Context, id string) error {
	return c.deleteOnce(ctx, "cancel_queued", "/v2/queued-pods/", id)
}

func (c *Client) deleteOnce(ctx context.Context, op, prefix, id string) error {
	if err := checkID(id); err != nil {
		return err
	}
	rep, err := c.do(ctx, call{op: op, method: http.MethodDelete, path: prefix + url.PathEscape(id), timeout: OtherTimeout})
	if err != nil {
		return ambiguous(op, rep, err)
	}
	switch {
	case rep.status == http.StatusNoContent || rep.status == http.StatusNotFound || rep.ok():
		return nil
	case rep.status >= 500:
		return &ErrAmbiguous{Op: op, Status: rep.status, Detail: rep.detail}
	}
	return failure(op, rep, nil)
}

// PodLogsTail reads the container log of a pod for at most LogsReadTimeout and returns its last n lines, redacted.
func (c *Client) PodLogsTail(ctx context.Context, id string, n int) ([]string, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	if n < 1 || n > MaxLogTail {
		return nil, fmt.Errorf("pods: log tail must be 1..%d", MaxLogTail)
	}
	q := url.Values{"source": {"container"}, "tail": {strconv.Itoa(n)}}
	rep, err := c.do(ctx, call{op: "pod_logs", method: http.MethodGet, path: "/v2/pods/" + url.PathEscape(id) + "/logs", query: q,
		stream: true, accept: "text/event-stream", retry: true})
	if err != nil || !rep.ok() {
		if err == nil && rep.status == http.StatusNotFound {
			return nil, ErrPodGone
		}
		return nil, failure("pod_logs", rep, err)
	}
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(rep.body))
	sc.Buffer(make([]byte, 0, 64<<10), MaxResponse)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		data = strings.TrimPrefix(data, " ")
		var ev struct {
			Line *string `json:"line"`
		}
		if json.Unmarshal([]byte(data), &ev) == nil && ev.Line != nil {
			data = *ev.Line
		}
		lines = append(lines, redact.String(data))
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

// CatalogSecurePrices reads price.secure of the requested GPU types (USD per hour, a JSON number parsed without a float).
func (c *Client) CatalogSecurePrices(ctx context.Context, typeIDs []string) (map[string]money.MicroUSD, error) {
	out := make(map[string]money.MicroUSD, len(typeIDs))
	if len(typeIDs) == 0 {
		return out, nil
	}
	q := url.Values{"cloud": {"SECURE"}, "product": {"POD"}}
	rep, err := c.do(ctx, call{op: "catalog", method: http.MethodGet, path: "/v2/catalog/gpus", query: q, timeout: OtherTimeout, retry: true})
	if err != nil || !rep.ok() {
		return nil, failure("catalog", rep, err)
	}
	var r struct {
		GPUs []struct {
			ID    string `json:"id"`
			Price struct {
				Secure *json.Number `json:"secure"`
			} `json:"price"`
		} `json:"gpus"`
	}
	if decodeJSON(rep.body, &r) != nil {
		return nil, &APIError{Op: "catalog", Status: rep.status, Detail: "unreadable reply"}
	}
	byID := map[string]money.MicroUSD{}
	for _, g := range r.GPUs {
		if g.Price.Secure == nil {
			continue
		}
		if m, perr := money.ParseUSD(g.Price.Secure.String()); perr == nil && m > 0 {
			byID[g.ID] = m
		}
	}
	for _, id := range typeIDs {
		m, ok := byID[id]
		if !ok {
			return nil, &PriceError{TypeID: id}
		}
		out[id] = m
	}
	return out, nil
}

// PodBilling reads hourly pod billing and sums the buckets of the given pods only: billing is account-wide (one request per own pod, with the `podId` filter), and a pod this
// process did not create is never counted. Compute is gpu plus cpu.
func (c *Client) PodBilling(ctx context.Context, from, to time.Time, podIDs []string) ([]provider.BillBucket, error) {
	if len(podIDs) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool, len(podIDs))
	byStart := map[time.Time]*provider.BillBucket{}
	// One request per own pod (the v2 `podId` filter) keeps every reply far below MaxResponse however busy the account is.
	for _, id := range podIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if err := c.podBillingOne(ctx, from, to, id, byStart); err != nil {
			return nil, err
		}
	}
	out := make([]provider.BillBucket, 0, len(byStart))
	for _, b := range byStart {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

// podBillingOne adds the hourly buckets of one pod to byStart.
func (c *Client) podBillingOne(ctx context.Context, from, to time.Time, podID string, byStart map[time.Time]*provider.BillBucket) error {
	q := url.Values{"bucketSize": {"hour"}, "startTime": {from.UTC().Format(time.RFC3339)}, "endTime": {to.UTC().Format(time.RFC3339)}, "podId": {podID}}
	rep, err := c.do(ctx, call{op: "pod_billing", method: http.MethodGet, path: "/v2/billing/pods", query: q, timeout: OtherTimeout, retry: true})
	if err != nil || !rep.ok() {
		return failure("pod_billing", rep, err)
	}
	var r struct {
		Records []struct {
			StartTime time.Time   `json:"startTime"`
			EndTime   time.Time   `json:"endTime"`
			PodID     string      `json:"podId"`
			Total     json.Number `json:"totalAmount"`
			GPU       json.Number `json:"gpuAmount"`
			CPU       json.Number `json:"cpuAmount"`
			Disk      json.Number `json:"diskAmount"`
		} `json:"records"`
	}
	if decodeJSON(rep.body, &r) != nil {
		return &APIError{Op: "pod_billing", Status: rep.status, Detail: "unreadable reply"}
	}
	for _, rec := range r.Records {
		if rec.PodID != podID {
			continue
		}
		var amt [4]money.MicroUSD
		for i, n := range []json.Number{rec.Total, rec.GPU, rec.CPU, rec.Disk} {
			if n == "" {
				continue
			}
			m, perr := money.ParseUSD(n.String())
			if perr != nil {
				return &APIError{Op: "pod_billing", Status: rep.status, Detail: "unreadable amount"}
			}
			amt[i] = m
		}
		b := byStart[rec.StartTime]
		if b == nil {
			b = &provider.BillBucket{Start: rec.StartTime, End: rec.EndTime}
			byStart[rec.StartTime] = b
		}
		b.Total += amt[0]
		b.Compute += amt[1] + amt[2]
		b.Disk += amt[3]
	}
	return nil
}
