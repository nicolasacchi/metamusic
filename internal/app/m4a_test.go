package app_test

import (
	"context"
	"errors"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func buildWith(t *testing.T, mutate func(*config.Config), o app.Options) *app.Services {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	cfg := config.Config{AuthMode: "local", PublicURL: "http://mm.example.com", RateScale: 100, DataDir: dir}
	if mutate != nil {
		mutate(&cfg)
	}
	s, err := app.Build(context.Background(), cfg, config.Secrets{Key: key}, st, root, o)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestApp_M4aWiringOIDCOff: without MM_OIDC_ISSUER the connector verifier is a nil interface (never a typed nil),
// while the grant store, the metadata handler, the Subsonic handler and the media services are always built.
func TestApp_M4aWiringOIDCOff(t *testing.T) {
	t.Parallel()
	s := buildWith(t, nil, app.Options{})
	if s.Server.Connectors != nil {
		t.Fatalf("Server.Connectors = %#v, want a nil interface", s.Server.Connectors)
	}
	if s.OIDC != nil || s.Deps.OIDC != nil {
		t.Error("an OIDC verifier exists without MM_OIDC_ISSUER")
	}
	d := s.Deps
	if d.Connectors == nil || d.Subsonic == nil || d.Covers == nil || d.Downloads == nil || d.Prompts == nil {
		t.Fatalf("M4a deps missing: %+v", d)
	}
	if s.Server.ConnectorStore != d.Connectors || s.Server.SubsonicStore != d.Subsonic || s.Server.Covers != d.Covers ||
		s.Server.Downloads != d.Downloads || s.Server.Prompts == nil {
		t.Error("Server and Deps must share the M4a services")
	}
	if s.Server.PRM == nil || s.Server.Subsonic == nil {
		t.Fatal("PRM and Subsonic handlers are always built")
	}
	// the metadata handler works without OIDC (it omits authorization_servers)
	w := httptest.NewRecorder()
	s.Server.PRM.ServeHTTP(w, httptest.NewRequest("GET", "/.well-known/oauth-protected-resource/mcp", nil))
	if w.Code != 200 {
		t.Errorf("PRM: %d", w.Code)
	}
	// the stub Subsonic handler answers a protocol error, not a panic
	w = httptest.NewRecorder()
	s.Server.Subsonic.ServeHTTP(w, httptest.NewRequest("GET", "/rest/ping?f=json", nil))
	if w.Code != 200 {
		t.Errorf("subsonic: %d", w.Code)
	}
}

func TestApp_M4aWiringOIDCOn(t *testing.T) {
	t.Parallel()
	s := buildWith(t, func(c *config.Config) {
		c.PublicURL = "https://mm.example.com"
		c.OIDCIssuer = "https://auth.example.com"
		c.OIDCClients = []config.OIDCClient{{ID: "test-claude", Label: "Claude Test"}}
		c.OIDCScopes = []string{"openid", "metamusic"}
		c.TrustedUsers = []string{"alice"}
	}, app.Options{M4a: app.M4aOptions{OIDCClient: &http.Client{}}})
	if s.Server.Connectors == nil || s.OIDC == nil || s.Deps.OIDC != s.OIDC || s.Server.OIDC != s.OIDC {
		t.Fatalf("connector verifier not wired: %#v %v", s.Server.Connectors, s.OIDC)
	}
	if st := s.OIDC.Status(); st.Keys != 0 || st.FetchedAt != nil {
		t.Errorf("Build must not fetch keys (serve runs Prefetch): %+v", st)
	}
}

// TestApp_M4aPromptSourcesNeverNil: get_prompt must reach its handler (501 stub or the real one), never 503, so the
// scripted-agent and parity runs see the stub by name.
func TestApp_M4aPromptSourcesNeverNil(t *testing.T) {
	t.Parallel()
	s := buildWith(t, nil, app.Options{})
	if s.Deps.Prompts == nil {
		t.Fatal("Deps.Prompts is nil")
	}
}

// TestApp_PromptSourcesAreReal: the prompt sources read the listening and context services (no A1 stub left).
func TestApp_PromptSourcesAreReal(t *testing.T) {
	t.Parallel()
	s := buildWith(t, nil, app.Options{})
	sess, err := s.Deps.Prompts.Session(context.Background(), time.Now().Add(-time.Hour), 5)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if len(sess.Tracks) != 0 {
		t.Errorf("Session tracks = %d, want 0 on an empty library", len(sess.Tracks))
	}
	_, err = s.Deps.Prompts.ContextJSON(context.Background(), auth.Principal{Kind: auth.KindAgent, ID: "x", Name: "x"}, "no-such-track")
	if err == nil || errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("ContextJSON error = %v, want a not-found style error", err)
	}
}
