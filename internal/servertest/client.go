package servertest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"testing"
)

// Response is one recorded HTTP exchange.
type Response struct {
	Method, Path string
	Status       int
	Header       http.Header
	Body         []byte
}

// JSON decodes the body (any shape); it fails the test on invalid JSON.
func (r *Response) JSON(tb testing.TB) any {
	tb.Helper()
	var v any
	if err := json.Unmarshal(r.Body, &v); err != nil {
		tb.Fatalf("%s %s: body is not JSON: %v: %s", r.Method, r.Path, err, r.Body)
	}
	return v
}

// Client is a REST client for one principal. It records every response so a
// test can scan them all for leaked secrets.
type Client struct {
	base    string
	headers http.Header
	hc      *http.Client

	mu        sync.Mutex
	responses []Response
}

func newClient(base string, headers http.Header, jar bool) *Client {
	c := &Client{base: base, headers: headers, hc: &http.Client{
		// Redirects are part of the contract under test; never follow them.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	if jar {
		c.hc.Jar, _ = cookiejar.New(nil)
	}
	return c
}

// Do sends one request. body is nil, a string/[]byte (sent as is) or any JSON
// value. extra is key, value pairs of additional headers.
func (c *Client) Do(tb testing.TB, method, path string, body any, extra ...string) *Response {
	tb.Helper()
	var rd io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			tb.Fatal(err)
		}
		rd, ct = bytes.NewReader(raw), "application/json"
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		tb.Fatal(err)
	}
	for k, vs := range c.headers {
		req.Header[k] = vs
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	for i := 0; i+1 < len(extra); i += 2 {
		req.Header.Set(extra[i], extra[i+1])
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		tb.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		tb.Fatalf("%s %s: reading body: %v", method, path, err)
	}
	r := Response{Method: method, Path: path, Status: resp.StatusCode, Header: resp.Header.Clone(), Body: raw}
	c.mu.Lock()
	c.responses = append(c.responses, r)
	c.mu.Unlock()
	return &r
}

// OK sends a request, requires a 2xx answer and returns the decoded JSON
// ("" body gives nil).
func (c *Client) OK(tb testing.TB, method, path string, body any, extra ...string) any {
	tb.Helper()
	r := c.Do(tb, method, path, body, extra...)
	if r.Status/100 != 2 {
		tb.Fatalf("%s %s: status %d: %s", method, path, r.Status, r.Body)
	}
	if len(bytes.TrimSpace(r.Body)) == 0 {
		return nil
	}
	return r.JSON(tb)
}

// Responses returns a copy of every recorded exchange.
func (c *Client) Responses() []Response {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Response(nil), c.responses...)
}

// Normalize returns v with every object value whose key is in masks replaced
// by "<masked>", at any depth. Rigs share ids and timestamps, so only values
// that legitimately differ per face (token ids, transport, client, source
// fields) are masked.
func Normalize(v any, masks ...string) any {
	skip := map[string]bool{}
	for _, m := range masks {
		skip[m] = true
	}
	var walk func(any) any
	walk = func(x any) any {
		switch t := x.(type) {
		case map[string]any:
			out := make(map[string]any, len(t))
			for k, e := range t {
				if skip[k] {
					out[k] = "<masked>"
				} else {
					out[k] = walk(e)
				}
			}
			return out
		case []any:
			out := make([]any, len(t))
			for i, e := range t {
				out[i] = walk(e)
			}
			return out
		}
		return x
	}
	return walk(v)
}
