package httpserver_test

import (
	"context"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/httpserver"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestServer_DepsPassThrough: a dependency that only Options.Deps carries reaches the operations (G1 Revision 2); the precedence rule
// itself is api.TestDeps_FillTakesOnlyZeroFields.
func TestServer_DepsPassThrough(t *testing.T) {
	t.Parallel()
	build := func(o httpserver.Options) *env {
		st, err := store.Open(context.Background(), t.TempDir()+"/t.db")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		key := []byte("0123456789abcdef0123456789abcdef")
		o.Cfg = config.Config{Addr: "127.0.0.1:8080", PublicURL: "http://mm.example.com", AuthMode: "proxy",
			TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, Users: map[string]config.Role{"alice": config.RoleOwner}}
		o.Sec = config.Secrets{Key: key, EdgeSecret: edge}
		o.Store = st
		o.Tokens = auth.NewTokenStore(st, key, time.Now)
		o.Sessions = auth.NewLocalAuth(st, key, o.Cfg.PublicURL)
		o.SPA = spaFS
		h, err := httpserver.New(o)
		if err != nil {
			t.Fatal(err)
		}
		return &env{h: h, tokens: o.Tokens}
	}
	without := build(httpserver.Options{})
	if w := without.do(human(httptest.NewRequest("GET", "/api/duels/next", nil))); w.Code != 503 {
		t.Fatalf("without the service: %d", w.Code)
	}
	via := build(httpserver.Options{Deps: &api.Deps{Duels: duels.New(duels.Options{})}})
	if w := via.do(human(httptest.NewRequest("GET", "/api/duels/next", nil))); w.Code != 501 {
		t.Errorf("service only in Options.Deps: %d, want the stub's 501", w.Code)
	}
}
