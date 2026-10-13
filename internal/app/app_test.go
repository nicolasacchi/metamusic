package app_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"github.com/nicolasacchi/metamusic/internal/ratelimit"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var key = []byte("0123456789abcdef0123456789abcdef")

func build(t *testing.T, o app.Options) (*app.Services, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	cfg := config.Config{AuthMode: "local", PublicURL: "http://mm.example.com", RateScale: 100, DataDir: dir}
	s, err := app.Build(context.Background(), cfg, config.Secrets{Key: key}, st, root, o)
	if err != nil {
		t.Fatal(err)
	}
	return s, st
}

func TestApp_BuildWiresGate(t *testing.T) {
	t.Parallel()
	s, _ := build(t, app.Options{})
	sv := s.Server
	if sv.Limiter == nil || sv.Audit == nil || sv.Idem == nil {
		t.Fatal("Server options must carry the gate dependencies")
	}
	if sv.Limiter != s.Limiter || sv.Audit != s.Audit || sv.Idem != s.Idem {
		t.Fatal("Server and Services must share one limiter, audit store and idem store")
	}
	if sv.Tokens != s.Tokens || sv.Sessions != s.Local || s.Deps.Tokens != s.Tokens {
		t.Fatal("token and session stores must be shared")
	}
	if s.Deps.Library == nil || sv.Library != s.Deps.Library || s.Deps.Signer == nil || sv.Media == nil {
		t.Fatal("M1 services not wired")
	}
	if s.Deps.Now == nil || sv.Now == nil {
		t.Fatal("clock not passed through")
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestApp_DefaultLimitsScaledByRateScale(t *testing.T) {
	t.Parallel()
	s, _ := build(t, app.Options{}) // RateScale 100 in build()
	for i := 0; i < 6000; i++ {
		if ok, _, _ := s.Limiter.Allow("agent:a", ratelimit.Agent); !ok {
			t.Fatalf("call %d refused; limits must be scaled by 100", i+1)
		}
	}
	s2, _ := build(t, app.Options{Limits: ratelimit.Config{ratelimit.Agent: {PerMinute: 60, Burst: 1}}})
	s2.Limiter.Allow("k", ratelimit.Agent)
	if ok, _, _ := s2.Limiter.Allow("k", ratelimit.Agent); ok {
		t.Fatal("explicit Limits must win over the scaled default")
	}
}

func TestApp_OneClockEverywhere(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	s, st := build(t, app.Options{Now: func() time.Time { return now }, NewID: func() string { return "fixed-id" }})
	if !s.Deps.Started.Equal(now) {
		t.Fatalf("Started = %v", s.Deps.Started)
	}
	if _, _, err := s.Tokens.Create(context.Background(), "bot", []auth.Scope{auth.ScopeRead}); err != nil {
		t.Fatal(err)
	}
	var created string
	if err := st.R.QueryRow(`SELECT created_at FROM api_tokens`).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if created != "2026-10-06T12:00:00Z" {
		t.Fatalf("token created_at = %s; the injected clock was not used", created)
	}
}

func TestApp_GatedServesAndAudits(t *testing.T) {
	t.Parallel()
	s, st := build(t, app.Options{})
	a, h := s.Gated()
	if a == nil {
		t.Fatal("no API")
	}
	r := httptest.NewRequest("GET", "/api/ping", nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Kind: auth.KindAgent, ID: "t1", Name: "bot", Scopes: []auth.Scope{auth.ScopeRead}}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var n int
	if err := st.R.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE op = 'ping' AND principal_id = 'agent:t1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit rows = %d (%v)", n, err)
	}
}

// TestApp_BuildAllM2aServices: a full config yields every M2a field of api.Deps
// and httpserver.Options; serve, mcp-stdio and servertest all come through Build.
func TestApp_BuildAllM2aServices(t *testing.T) {
	t.Parallel()
	s, _ := build(t, app.Options{})
	d, o := s.Deps, s.Server
	deps := map[string]bool{
		"Judgments": d.Judgments != nil, "Drafts": d.Drafts != nil, "Playlists": d.Playlists != nil, "Exports": d.Exports != nil,
		"Backups": d.Backups != nil, "Context": d.Context != nil, "Listening": d.Listening != nil, "Audit": d.Audit != nil, "Devices": d.Devices != nil,
	}
	opts := map[string]bool{
		"Judgments": o.Judgments != nil, "Drafts": o.Drafts != nil, "Playlists": o.Playlists != nil, "Exports": o.Exports != nil,
		"Backups": o.Backups != nil, "Context": o.Context != nil, "Listening": o.Listening != nil, "Audit": o.Audit != nil,
		"Idem": o.Idem != nil, "Limiter": o.Limiter != nil, "Devices": o.Devices != nil, "DeviceStore": o.DeviceStore != nil, "Notify": o.Notify != nil,
	}
	for name, ok := range deps {
		if !ok {
			t.Errorf("api.Deps.%s is nil", name)
		}
	}
	for name, ok := range opts {
		if !ok {
			t.Errorf("httpserver.Options.%s is nil", name)
		}
	}
	if d.Judgments != o.Judgments || d.Drafts != o.Drafts || d.Exports != o.Exports || d.Backups != o.Backups || s.Devices != o.DeviceStore || s.Exports != o.Exports {
		t.Error("Deps and Server options must share one instance of every service")
	}
	if s.Devices == nil || s.Backups == nil || s.Exports == nil {
		t.Error("Services must expose Devices, Backups and Exports")
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestApp_NotifierFromConfig: no MM_NTFY_URL selects the no-op notifier; an
// injected notifier wins.
func TestApp_NotifierFromConfig(t *testing.T) {
	t.Parallel()
	s, _ := build(t, app.Options{})
	if err := s.Server.Notify.Notify(context.Background(), notify.Message{Title: "x"}); err != nil {
		t.Fatalf("default notifier must not fail: %v", err)
	}
	custom := notify.Nop{}
	s2, _ := build(t, app.Options{Notify: custom})
	if s2.Server.Notify != notify.Notifier(custom) {
		t.Error("an injected notifier must be used as given")
	}
}
