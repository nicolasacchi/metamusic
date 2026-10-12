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
