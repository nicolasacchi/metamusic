// Package objstore is a small path-style S3 SigV4 client (Cloudflare R2, Backblaze B2 or any S3-compatible store). It
// imports only the standard library and internal/redact. Presigned URLs and credentials are never logged, returned in
// an error or stored: every error is built from a status, an S3 error code and a redacted transport message.
package objstore

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/redact"
)

// Config names one bucket. Endpoint is scheme and host only; addressing is path style (endpoint/bucket/key) always.
type Config struct{ Endpoint, Bucket, Region, AccessKeyID, SecretAccessKey string }

// String and GoString never reveal a credential.
func (c Config) String() string {
	return fmt.Sprintf("objstore.Config{endpoint:%s bucket:%s region:%s keys:%s}", setStr(c.Endpoint), setStr(c.Bucket), setStr(c.Region),
		setStr(c.AccessKeyID+c.SecretAccessKey))
}

// GoString is String, so %#v cannot leak either.
func (c Config) GoString() string { return c.String() }

func setStr(s string) string {
	if s == "" {
		return "unset"
	}
	return "set"
}

// ErrTooLarge is returned by Get when the object is (or turns out to be) larger than the bound.
var ErrTooLarge = errors.New("objstore: object larger than the allowed size")

// ErrNotFound is returned by Get for a missing object.
var ErrNotFound = errors.New("objstore: object not found")

// Error is a refusal answered by the store: the HTTP status and the S3 error code (when the body parses). It never
// carries the message, the request id, a key or a credential.
type Error struct {
	Op     string
	Status int
	Code   string
}

func (e *Error) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("objstore: %s: status %d", e.Op, e.Status)
	}
	return fmt.Sprintf("objstore: %s: status %d code %s", e.Op, e.Status, e.Code)
}

// transportError wraps a connection-level failure with its URL stripped and the message redacted. Unwrap keeps
// errors.Is(err, context.DeadlineExceeded) working.
type transportError struct {
	op    string
	msg   string
	cause error
}

func (e *transportError) Error() string { return "objstore: " + e.op + ": " + e.msg }
func (e *transportError) Unwrap() error { return e.cause }

// Client talks to one bucket. Safe for concurrent use.
type Client struct {
	cfg  Config
	base *url.URL
	h    *http.Client
	now  func() time.Time
}

// New validates c. h nil means a client with a 60 s timeout; now nil means time.Now.
func New(c Config, h *http.Client, now func() time.Time) (*Client, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("objstore: endpoint must be an http(s) URL with a host and no path")
	}
	switch {
	case c.Bucket == "":
		return nil, errors.New("objstore: bucket missing")
	case c.Region == "":
		return nil, errors.New("objstore: region missing")
	case c.AccessKeyID == "":
		return nil, errors.New("objstore: access key id missing")
	case c.SecretAccessKey == "":
		return nil, errors.New("objstore: secret access key missing")
	}
	if h == nil {
		h = &http.Client{Timeout: 60 * time.Second}
	}
	if now == nil {
		now = time.Now
	}
	return &Client{cfg: c, base: &url.URL{Scheme: u.Scheme, Host: u.Host}, h: h, now: now}, nil
}

func (c *Client) objectURL(key string) *url.URL {
	return &url.URL{Scheme: c.base.Scheme, Host: c.base.Host, Path: "/" + c.cfg.Bucket + "/" + key}
}

// PresignPut returns a presigned PUT URL for key valid for ttl (1 s to 7 d). The URL is a credential: do not log it.
func (c *Client) PresignPut(key string, ttl time.Duration) (string, error) {
	if key == "" {
		return "", errors.New("objstore: empty key")
	}
	r := &http.Request{Method: http.MethodPut, URL: c.objectURL(key), Header: http.Header{}}
	if err := SignQuery(r, c.cfg, c.now(), ttl); err != nil {
		return "", err
	}
	return r.URL.String(), nil
}

// Head reports an object's size; ok is false for a 404. HEAD has no body, so a 404 from a wrong bucket cannot be told
// from a missing key here; callers that must distinguish use Get.
func (c *Client) Head(ctx context.Context, key string) (size int64, ok bool, err error) {
	resp, err := c.do(ctx, "head", http.MethodHead, key, nil, -1)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return 0, false, nil
	case resp.StatusCode/100 == 2:
		if resp.ContentLength < 0 {
			return 0, false, &Error{Op: "head", Status: resp.StatusCode} // size unknown: never report -1 as a size
		}
		return resp.ContentLength, true, nil
	}
	return 0, false, c.refusal("head", resp)
}

// Get returns the object body and its declared size. A declared size over maxBytes is ErrTooLarge before any body is
// read; a body that outgrows maxBytes (no or wrong length) fails the read with ErrTooLarge.
func (c *Client) Get(ctx context.Context, key string, maxBytes int64) (io.ReadCloser, int64, error) {
	resp, err := c.do(ctx, "get", http.MethodGet, key, nil, -1)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode == http.StatusNotFound {
		defer resp.Body.Close()
		if err := c.notFound("get", resp); err != nil {
			return nil, 0, err
		}
		return nil, 0, ErrNotFound
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, 0, c.refusal("get", resp)
	}
	if resp.ContentLength > maxBytes {
		resp.Body.Close()
		return nil, 0, ErrTooLarge
	}
	return &boundedBody{rc: resp.Body, left: maxBytes}, resp.ContentLength, nil
}

type boundedBody struct {
	rc   io.ReadCloser
	left int64 // bytes still allowed
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if b.left < 0 {
		return 0, ErrTooLarge
	}
	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1] // read one byte past the bound to detect overflow
	}
	n, err := b.rc.Read(p)
	if int64(n) > b.left {
		n = int(b.left)
		b.left = -1
		return n, ErrTooLarge
	}
	b.left -= int64(n)
	return n, err
}

func (b *boundedBody) Close() error { return b.rc.Close() }

// Put uploads size bytes. It exists for the self-check (`upload fallback check`); workers use presigned URLs.
func (c *Client) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	resp, err := c.do(ctx, "put", http.MethodPut, key, body, size)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return c.refusal("put", resp)
	}
	return nil
}

// Delete removes an object; 204 and 404 are success.
func (c *Client) Delete(ctx context.Context, key string) error {
	resp, err := c.do(ctx, "delete", http.MethodDelete, key, nil, -1)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		return nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return c.notFound("delete", resp)
	}
	return c.refusal("delete", resp)
}

func (c *Client) do(ctx context.Context, op, method, key string, body io.Reader, size int64) (*http.Response, error) {
	if key == "" {
		return nil, errors.New("objstore: empty key")
	}
	if body == nil {
		body = http.NoBody
	}
	req, err := http.NewRequestWithContext(ctx, method, c.objectURL(key).String(), body)
	if err != nil {
		return nil, c.transport(op, err)
	}
	if size >= 0 {
		req.ContentLength = size
	}
	if err := SignHeader(req, c.cfg, c.now(), ""); err != nil {
		return nil, err
	}
	resp, err := c.h.Do(req)
	if err != nil {
		return nil, c.transport(op, err)
	}
	return resp, nil
}

// transport drops the URL from a *url.Error (it names the key) and redacts what is left, including the credentials.
func (c *Client) transport(op string, err error) error {
	cause := err
	var ue *url.Error
	if errors.As(err, &ue) {
		cause = ue.Err
	}
	return &transportError{op: op, msg: c.scrub(cause.Error()), cause: cause}
}

func (c *Client) scrub(s string) string {
	for _, lit := range []string{c.cfg.SecretAccessKey, c.cfg.AccessKeyID} {
		if len(lit) >= 4 {
			s = strings.ReplaceAll(s, lit, "<redacted>")
		}
	}
	return redact.String(s)
}

type s3Error struct {
	Code string `xml:"Code"`
}

// notFound classifies a 404: nil when the key is missing (NoSuchKey, or no parseable code), an *Error for anything
// else such as NoSuchBucket, so a wrong bucket or endpoint is never mistaken for a missing object.
func (c *Client) notFound(op string, resp *http.Response) error {
	e := c.refusal(op, resp)
	var oe *Error
	if errors.As(e, &oe) && oe.Code != "" && oe.Code != "NoSuchKey" {
		return e
	}
	return nil
}

// refusal builds an *Error from an answered non-success response. Only the status and a well-formed code are kept.
func (c *Client) refusal(op string, resp *http.Response) error {
	e := &Error{Op: op, Status: resp.StatusCode}
	if resp.Request != nil && resp.Request.Method == http.MethodHead {
		return e
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var x s3Error
	if xml.NewDecoder(bytes.NewReader(raw)).Decode(&x) == nil && validCode(x.Code) {
		e.Code = x.Code
	}
	return e
}

func validCode(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
