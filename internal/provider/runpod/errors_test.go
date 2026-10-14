package runpod_test

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod"
)

// classify runs runpod.Classify on a synthetic response (status 0: no response, a transport error).
func classify(status int, hdr map[string]string, body string, err error) *runpod.Error {
	if status == 0 {
		return runpod.Classify("run", nil, []byte(body), err)
	}
	h := http.Header{}
	for k, v := range hdr {
		h.Set(k, v)
	}
	r := &http.Response{StatusCode: status, Header: h, Body: http.NoBody}
	defer r.Body.Close()
	return runpod.Classify("run", r, []byte(body), err)
}

func TestClassify_Table(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		status     int
		hdr        map[string]string
		body       string
		err        error
		code       runpod.Code
		retryable  bool
		ambiguous  bool
		access     bool
		retryAfter time.Duration
		errors     []string
	}{
		{name: "1010", status: 403, hdr: nil, body: "error code: 1010", code: runpod.EdgeBlocked, access: true},
		{name: "401", status: 401, hdr: nil, body: `{"title":"Unauthorized","status":401}`, code: runpod.Unauthorized, access: true},
		{name: "403", status: 403, hdr: nil, body: `{"title":"Forbidden","status":403}`, code: runpod.Forbidden, access: true},
		{name: "404", status: 404, hdr: nil, body: `{"title":"Not Found","status":404}`, code: runpod.NotFound},
		{name: "409 paused", status: 409, hdr: nil, body: `{"title":"Conflict","detail":"Endpoint is PAUSED","status":409}`, code: runpod.EndpointPaused, retryable: true},
		{name: "409 other", status: 409, hdr: nil, body: `{"title":"Conflict","status":409}`, code: runpod.BadRequest},
		{name: "429 retry-after", status: 429, hdr: map[string]string{"Retry-After": "7", "RateLimit": `"minute";r=0;t=3`}, code: runpod.RateLimited, retryable: true, retryAfter: 7 * time.Second},
		{name: "429 ratelimit only", status: 429, hdr: map[string]string{"RateLimit": `"hour";r=10;t=900, "minute";r=0;t=3`}, code: runpod.RateLimited, retryable: true, retryAfter: 3 * time.Second},
		{name: "429 neither", status: 429, hdr: nil, code: runpod.RateLimited, retryable: true, retryAfter: 5 * time.Second},
		{name: "422", status: 422, hdr: nil, body: `{"title":"Unprocessable","status":422,"detail":"bad","errors":["gpu.count: must be 1",{"pointer":"/workers","detail":"max too large"}]}`,
			code: runpod.BadRequest, errors: []string{"gpu.count: must be 1", "/workers: max too large"}},
		{name: "503", status: 503, hdr: nil, code: runpod.Upstream, retryable: true, ambiguous: true},
		{name: "dial", err: &url.Error{Op: "Post", URL: "https://api.runpod.ai/v2/secret-endpoint/run", Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}},
			code: runpod.Network, retryable: true, ambiguous: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := classify(c.status, c.hdr, c.body, c.err)
			if e == nil {
				t.Fatal("nil error")
			}
			if e.Code != c.code || e.Retryable() != c.retryable || e.Ambiguous() != c.ambiguous || e.Access() != c.access {
				t.Fatalf("got code=%s retryable=%v ambiguous=%v access=%v", e.Code, e.Retryable(), e.Ambiguous(), e.Access())
			}
			if e.RetryAfter != c.retryAfter {
				t.Fatalf("RetryAfter = %v, want %v", e.RetryAfter, c.retryAfter)
			}
			if c.errors != nil && strings.Join(e.Errors, "|") != strings.Join(c.errors, "|") {
				t.Fatalf("Errors = %q", e.Errors)
			}
			if c.access != errors.Is(e, provider.ErrAccess) {
				t.Fatalf("errors.Is(ErrAccess) = %v", !c.access)
			}
			if strings.Contains(e.Error(), "secret-endpoint") || strings.Contains(e.Error(), "https://") {
				t.Fatalf("error leaks the URL: %s", e.Error())
			}
		})
	}
}

func TestClassify_GarbageAndSuccess(t *testing.T) {
	t.Parallel()
	if e := classify(200, nil, "{}", nil); e != nil {
		t.Fatalf("2xx classified as %v", e)
	}
	long := strings.Repeat("a b ", 400)
	e := classify(400, nil, `{"title":"x","detail":"`+long+`","errors":["`+long+`"]}`, nil)
	n := len(e.Detail)
	for _, s := range e.Errors {
		n += len(s)
	}
	if n > 500 {
		t.Fatalf("detail+errors = %d bytes", n)
	}
	if len(e.Error()) > 300 {
		t.Fatalf("Error() = %d bytes", len(e.Error()))
	}
}
