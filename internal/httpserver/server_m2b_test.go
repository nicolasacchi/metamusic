package httpserver_test

import (
	"context"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/httpserver"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The M2b services in Options reach api.Deps: absent ones answer 503 UNAVAILABLE naming the component, present
// ones (stubs at Stage 1) answer through their operations.
func TestServer_M2bOptionsReachDeps(t *testing.T) {
	t.Parallel()
	build := func(o httpserver.Options) *env {
		st, err := store.Open(context.Background(), t.TempDir()+"/t.db")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		key := []byte("0123456789abcdef0123456789abcdef")
		o.Cfg = config.Config{Addr: "127.0.0.1:8080", PublicURL: "http://mm.example.com", AuthMode: "proxy",
			TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, TrustedUsers: []string{"alice"}}
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
	absent := build(httpserver.Options{})
	for path, component := range map[string]string{"/api/duels/next": "duels", "/api/tracks/t1/analysis": "ears", "/api/tracks/t1/clip": "clips"} {
		w := absent.do(human(httptest.NewRequest("GET", path, nil)))
		m := decode(t, w)
		d, _ := m["details"].(map[string]any)
		if w.Code != 503 || m["code"] != "UNAVAILABLE" || d["component"] != component {
			t.Errorf("%s without services: %d %v", path, w.Code, m)
		}
	}
	present := build(httpserver.Options{Duels: duels.New(duels.Options{}), Ears: ears.New(ears.Options{})})
	for _, path := range []string{"/api/duels/next", "/api/tracks/t1/analysis"} {
		w := present.do(human(httptest.NewRequest("GET", path, nil)))
		if m := decode(t, w); w.Code != 501 || m["code"] != "NOT_IMPLEMENTED" {
			t.Errorf("%s with stub services: %d %v", path, w.Code, m)
		}
	}
	// clips stay unavailable when Options.Clips is nil
	w := present.do(human(httptest.NewRequest("GET", "/api/tracks/t1/clip", nil)))
	if decode(t, w)["code"] != "UNAVAILABLE" {
		t.Errorf("get_clip: %d %s", w.Code, w.Body)
	}
}
