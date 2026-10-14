package runpod

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchSpec(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	serve := func(h http.HandlerFunc) string {
		s := httptest.NewServer(h)
		t.Cleanup(s.Close)
		return s.URL
	}
	t.Run("ok, with the default client", func(t *testing.T) {
		t.Parallel()
		u := serve(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"a":1}`)) })
		b, err := FetchSpec(ctx, nil, u)
		if err != nil || string(b) != `{"a":1}` {
			t.Fatalf("%q %v", b, err)
		}
	})
	t.Run("invalid url", func(t *testing.T) {
		t.Parallel()
		if _, err := FetchSpec(ctx, nil, "http://bad host/\x7f"); err == nil || !strings.Contains(err.Error(), "invalid spec URL") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		t.Parallel()
		s := httptest.NewServer(http.NotFoundHandler())
		u := s.URL
		s.Close()
		if _, err := FetchSpec(ctx, nil, u); err == nil || !strings.Contains(err.Error(), "unreachable") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("cancelled context", func(t *testing.T) {
		t.Parallel()
		u := serve(func(http.ResponseWriter, *http.Request) {})
		c, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := FetchSpec(c, nil, u); err == nil || strings.Contains(err.Error(), "unreachable") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("non-200", func(t *testing.T) {
		t.Parallel()
		u := serve(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
		if _, err := FetchSpec(ctx, nil, u); err == nil || !strings.Contains(err.Error(), "418") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("too large", func(t *testing.T) {
		t.Parallel()
		u := serve(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(make([]byte, 4<<20+10)) })
		if _, err := FetchSpec(ctx, nil, u); err == nil || !strings.Contains(err.Error(), "could not be read") {
			t.Fatalf("%v", err)
		}
	})
}

func TestDiffEndpoint_EveryFieldDrifts(t *testing.T) {
	t.Parallel()
	idle := 5
	want := EndpointSpec{Type: "queue", Flashboot: "on", Timeout: 1000, Image: "img@sha256:abc", Env: map[string]string{"A": "b"}}
	want.Scaling.Type, want.Scaling.QueueDelay = "QUEUE_DELAY", 4
	want.Workers.Max, want.Workers.IdleTimeout = 2, &idle
	want.GPU.Count, want.GPU.Pools = 1, []string{"P"}
	diff, p, err := diffEndpoint(want, Endpoint{Type: "queue"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(diff, ","); got != "env,flashboot,gpu,image,scaling,timeout,workers" {
		t.Fatalf("diff %s", got)
	}
	if p.Scaling == nil || p.Workers == nil || p.GPU == nil || p.Flashboot == nil || p.Timeout == nil || p.Image == nil || p.Env == nil {
		t.Fatalf("patch %+v", p)
	}
	if _, _, err := diffEndpoint(want, Endpoint{Type: "lb"}, true); err == nil {
		t.Fatal("a different type cannot be patched")
	}
}
