package servertest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// NtfyRequest is one publish the fake ntfy server received.
type NtfyRequest struct {
	Path          string
	Authorization string
	Body          string
}

// FakeNtfy is a local ntfy publisher endpoint that records what it receives.
type FakeNtfy struct {
	URL string

	mu   sync.Mutex
	reqs []NtfyRequest
}

func newFakeNtfy(tb testing.TB) *FakeNtfy {
	f := &FakeNtfy{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		f.mu.Lock()
		f.reqs = append(f.reqs, NtfyRequest{Path: r.URL.Path, Authorization: r.Header.Get("Authorization"), Body: string(b)})
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	tb.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

// Requests returns a copy of the recorded publishes.
func (f *FakeNtfy) Requests() []NtfyRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]NtfyRequest(nil), f.reqs...)
}
