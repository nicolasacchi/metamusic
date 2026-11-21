package fake

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
)

// The upload-fallback half of the simulated worker (BULK-R4, C399.7, C399.13). It mirrors worker/src/mmworker/upload.py: a transport
// failure of the primary route (a connection error, a timeout, 5xx, 408 or 429 after the worker's own retries) makes the artifact
// and every later one of the attempt go to the fallback URL of its upload target, when it has one and the worker image has the
// channel. An answered refusal (403, 409, 410, 411, 413, 422) stays final and never reaches the bucket. The real worker waits
// fallback_after_s of failures first; the simulation switches on the first exhausted retry, because the fake backend keeps its own
// virtual time and a wait would add nothing a test could observe.

// FallbackPutter puts one artifact to a presigned fallback URL. As with Uploader, URLs carry signatures: implementations must never
// log them or put them in errors.
type FallbackPutter interface {
	PutFallback(ctx context.Context, url string, body []byte) (status int, err error)
}

// HTTPFallback puts to the fallback URL over HTTP, as the worker does: Content-Length, an octet-stream type and no digest header.
type HTTPFallback struct{ Client *http.Client }

// PutFallback implements FallbackPutter.
func (f HTTPFallback) PutFallback(ctx context.Context, target string, body []byte) (int, error) {
	c := f.Client
	if c == nil {
		c = &http.Client{Timeout: 5 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, bytes.NewReader(body))
	if err != nil {
		return 0, errors.New("fake: bad fallback URL")
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("User-Agent", "mmworker-sim")
	resp, err := c.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL carries the signature
		}
		return 0, fmt.Errorf("fake: fallback PUT: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode, nil
}

// transportFailure reports the failures that may switch the worker to its fallback.
func transportFailure(code int, err error) bool {
	return err != nil || code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500
}

// fallbackFor reports whether t has a fallback the worker can use.
func (s *Session) fallbackFor(t provider.UploadTarget) bool {
	return s.w.o.Fallback != nil && t.Fallback != nil && t.Fallback.URL != ""
}

// putFallback puts body to t's fallback with the same retry shape as the primary.
func (s *Session) putFallback(ctx context.Context, t provider.UploadTarget, body []byte) (int, error) {
	var code int
	var err error
	for i := 0; i < putTries; i++ {
		code, err = s.w.o.Fallback.PutFallback(ctx, t.Fallback.URL, body)
		if err == nil && code/100 == 2 {
			return 200, nil
		}
		if !transportFailure(code, err) {
			break
		}
	}
	return code, err
}
