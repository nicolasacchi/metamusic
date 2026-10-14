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
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

// noBase is "no BaseContext": the server then attaches no shutdown signal.
var noBase context.Context

func newUploadsEnv(base context.Context, t *testing.T, withUploads bool, probe http.Handler) (http.Handler, *auth.TokenStore) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Config{Addr: "127.0.0.1:8080", PublicURL: "http://mm.example.com", AuthMode: "proxy"}
	key := []byte("0123456789abcdef0123456789abcdef")
	tokens := auth.NewTokenStore(st, key, time.Now)
	o := httpserver.Options{Cfg: cfg, Sec: config.Secrets{Key: key, EdgeSecret: edge}, Store: st, Tokens: tokens,
		Sessions: auth.NewLocalAuth(st, key, cfg.PublicURL), BaseContext: base, MCP: probe}
	if withUploads {
		o.Uploads = upload.NewService(upload.ServiceOptions{Signer: upload.NewSigner(key), Store: st, PublicURL: cfg.PublicURL})
	}
	h, err := httpserver.New(o)
	if err != nil {
		t.Fatal(err)
	}
	return h, tokens
}

// TestHTTPServer_UploadsMounted: both worker routes are ClassMachine (no principal needed): the capability
// is the authorisation, so a link without a valid signature answers 403 problem+json; an unwired server
// answers 404.
func TestHTTPServer_UploadsMounted(t *testing.T) {
	t.Parallel()
	h, _ := newUploadsEnv(noBase, t, true, nil)
	for _, tc := range []struct{ method, path, code string }{
		{"PUT", "/worker/v1/uploads/0190aaaa-0000-7000-8000-000000000001?exp=1&sig=x", "UPLOAD_LINK_INVALID"},
		{"POST", "/worker/v1/attempts/0190aaaa-0000-7000-8000-000000000002/start?exp=1&sig=x", "ATTEMPT_LINK_INVALID"},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader("x"))) // no principal, no credentials
		if w.Code != http.StatusForbidden || w.Header().Get("Content-Type") != "application/problem+json" || !strings.Contains(w.Body.String(), `"`+tc.code+`"`) {
			t.Errorf("%s %s: %d %q %s", tc.method, tc.path, w.Code, w.Header().Get("Content-Type"), w.Body)
		}
	}
	w := httptest.NewRecorder()
	bare, _ := newUploadsEnv(noBase, t, false, nil)
	bare.ServeHTTP(w, httptest.NewRequest("PUT", "/worker/v1/uploads/x", strings.NewReader("x")))
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"NOT_FOUND"`) {
		t.Errorf("unwired uploads: %d %s", w.Code, w.Body)
	}
}

// TestHTTPServer_BaseContextReachesRequests: every request carries the server's shutdown signal so
// long-polls (wait_for_job) return when shutdown starts.
func TestHTTPServer_BaseContextReachesRequests(t *testing.T) {
	t.Parallel()
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	var seen <-chan struct{}
	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = reqinfo.ShutdownDone(r.Context()) })
	h, tokens := newUploadsEnv(base, t, false, probe)
	tok, _, err := tokens.Create(context.Background(), "agent-1", []auth.Scope{auth.ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	req := bearer(httptest.NewRequest("GET", "/mcp", nil), tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if seen == nil {
		t.Fatalf("handler saw no shutdown signal (status %d)", w.Code)
	}
	select {
	case <-seen:
		t.Fatal("fired before shutdown")
	default:
	}
	cancel()
	select {
	case <-seen:
	case <-time.After(time.Second):
		t.Fatal("did not fire after shutdown")
	}
	if reqinfo.ShutdownDone(context.Background()) != nil {
		t.Error("a context without a shutdown signal must yield a nil channel")
	}
}
