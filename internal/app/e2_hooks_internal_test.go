package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandlerTransport_RoutesMediaToTheMediaHandler: the fake live preflight reaches the upload route and, under ACE-Step, the
// media route in process; a path outside both goes to the upload handler as before (the M3 preflight is unchanged).
func TestHandlerTransport_RoutesMediaToTheMediaHandler(t *testing.T) {
	t.Parallel()
	mark := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("X-Served-By", name) })
	}
	for _, c := range []struct {
		name  string
		media http.Handler
		path  string
		want  string
	}{
		{"media route with a media handler", mark("media"), "/media/00.flac", "media"},
		{"upload route with a media handler", mark("media"), "/worker/v1/uploads/x", "uploads"},
		{"no media handler: everything is the upload handler", nil, "/media/00.flac", "uploads"},
	} {
		req := httptest.NewRequest(http.MethodGet, "http://mm.example.com"+c.path, http.NoBody)
		resp, err := handlerTransport{h: mark("uploads"), media: c.media}.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		if got := resp.Header.Get("X-Served-By"); got != c.want {
			t.Errorf("%s: served by %q, want %q", c.name, got, c.want)
		}
	}
}

// TestLateHandler: until the media route is bound it answers 503 (a fetch can never reach a half-built server), then serves.
func TestLateHandler(t *testing.T) {
	t.Parallel()
	var l lateHandler
	rec := httptest.NewRecorder()
	l.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/media/x", http.NoBody))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("unbound: %d", rec.Code)
	}
	l.set(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	rec = httptest.NewRecorder()
	l.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/media/x", http.NoBody))
	if rec.Code != http.StatusTeapot {
		t.Errorf("bound: %d", rec.Code)
	}
}
