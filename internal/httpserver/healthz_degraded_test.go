package httpserver_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/httpserver"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// A degraded media pipeline (the ffmpeg probe is still retrying) shows in /healthz as status "degraded" plus the named
// pieces, and clears when the pipeline recovers. The status code stays 200: a restart would not repair it.
func TestHealthz_ReportsDegradedPieces(t *testing.T) {
	t.Parallel()
	st, err := store.Open(context.Background(), t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Config{Addr: "127.0.0.1:8080", PublicURL: "http://mm.example.com", AuthMode: "proxy"}
	key := []byte("0123456789abcdef0123456789abcdef")
	var down atomic.Bool
	down.Store(true)
	h, err := httpserver.New(httpserver.Options{
		Cfg: cfg, Sec: config.Secrets{Key: key, EdgeSecret: edge}, Store: st, Tokens: auth.NewTokenStore(st, key, time.Now),
		Sessions: auth.NewLocalAuth(st, key, cfg.PublicURL), Version: "v-test", Commit: "abc",
		Degraded: func() []string {
			if down.Load() {
				return []string{"media_tools"}
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	get := func() string {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
		if w.Code != 200 {
			t.Fatalf("status %d", w.Code)
		}
		return strings.TrimSpace(w.Body.String())
	}
	if got, want := get(), `{"status":"degraded","ready":true,"degraded":["media_tools"]}`; got != want {
		t.Fatalf("degraded body %s, want %s", got, want)
	}
	down.Store(false)
	if got, want := get(), `{"status":"ok","ready":true}`; got != want {
		t.Fatalf("recovered body %s, want %s", got, want)
	}
}
