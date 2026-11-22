package httpserver_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/httpserver"
	"github.com/nicolasacchi/metamusic/internal/provider/runpod/pods"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func podsEnv(t *testing.T, h http.Handler) http.Handler {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Config{Addr: "127.0.0.1:8080", PublicURL: "http://mm.example.com", AuthMode: "proxy"}
	key := []byte("0123456789abcdef0123456789abcdef")
	o := httpserver.Options{Cfg: cfg, Sec: config.Secrets{Key: key, EdgeSecret: edge}, Store: st, Tokens: auth.NewTokenStore(st, key, time.Now),
		Sessions: auth.NewLocalAuth(st, key, cfg.PublicURL), Pods: h}
	srv, err := httpserver.New(o)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// TestHTTPServer_PodsPollMounted: with the pod backend off (no handler) the route answers 404 NOT_FOUND; with one mounted it is
// ClassMachine (no principal, no cookie): the signed link is the credential, so a link without a valid signature answers 403.
func TestHTTPServer_PodsPollMounted(t *testing.T) {
	t.Parallel()
	const path = "/worker/v1/pods/0190aaaa-0000-7000-8000-000000000001/poll"
	bare := podsEnv(t, nil)
	w := httptest.NewRecorder()
	bare.ServeHTTP(w, httptest.NewRequest("POST", path+"?exp=1&sig=x", strings.NewReader("{}")))
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"NOT_FOUND"`) {
		t.Fatalf("unwired: %d %s", w.Code, w.Body)
	}
	st, err := store.Open(context.Background(), t.TempDir()+"/p.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := podsEnv(t, pods.NewPoller(pods.PollOptions{Store: st, Signer: pods.NewSigner([]byte("k")), Queue: pods.NewQueue()}).Handler())
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", path+"?exp=1&sig="+strings.Repeat("A", 43), strings.NewReader("{}"))) // no principal, no credentials
	if w.Code != http.StatusForbidden || w.Header().Get("Content-Type") != "application/problem+json" || !strings.Contains(w.Body.String(), `"POD_LINK_INVALID"`) {
		t.Fatalf("mounted: %d %q %s", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
}
