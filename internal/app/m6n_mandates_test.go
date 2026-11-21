package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestWireMandates_NoJobsServiceServes503: without a jobs service wireMandates wires nothing, so all four mandate operations answer
// 503 UNAVAILABLE {component: mandates} and nothing is written. A revoke must cancel the mandate's queued intents through the jobs
// service, so it may never succeed (or any mandate op run) without one.
func TestWireMandates_NoJobsServiceServes503(t *testing.T) {
	t.Parallel()
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
	s, err := Build(context.Background(), cfg, config.Secrets{Key: make([]byte, 32)}, st, root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Deps.Mandates == nil || s.Jobs == nil || s.Spend == nil {
		t.Fatalf("precondition: a full build wires mandates (%v) over jobs (%v) and spend (%v)", s.Deps.Mandates != nil, s.Jobs != nil, s.Spend != nil)
	}

	// the same build hook over a Services that has spend but no jobs service
	env := &BuildEnv{Store: st, Services: &Services{Spend: s.Spend}, Now: time.Now, NewID: s.Deps.NewIDFunc()}
	if err := wireMandates(env); err != nil {
		t.Fatal(err)
	}
	if env.Services.Deps.Mandates != nil {
		t.Fatal("wireMandates wired the mandate service without a jobs service")
	}
	s.Deps.Mandates = env.Services.Deps.Mandates
	_, h := s.Gated()

	owner := auth.Principal{Kind: auth.KindHuman, ID: "owner", Name: "owner", User: "owner", Role: auth.RoleOwner, Scopes: auth.ScopesFor(auth.RoleOwner), Via: "local", StepUpAt: time.Now()}
	createBody := `{"holder":"night_shift","title":"Night shift","budget_micro_usd":1000000,"valid_until":"` + time.Now().Add(7*24*time.Hour).UTC().Format(time.RFC3339) +
		`","scope":{"modes":["new_seed"],"engines":["yue2"],"min_stars":3,"root_track_ids":null,"max_takes_per_lineage":3,"max_takes_per_batch":6,` +
		`"max_batch_ceiling_micro_usd":943587,"max_batches_per_day":1,"tier_b":true}}`
	const id = "01900000-0000-7000-8000-000000000001"
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/human/api/mandates", createBody},
		{"GET", "/api/mandates", ""},
		{"GET", "/api/mandates/" + id, ""},
		{"POST", "/api/mandates/" + id + "/revoke", `{"reason":"x"}`},
	} {
		r := httptest.NewRequest(c.method, "http://mm.example.com"+c.path, strings.NewReader(c.body))
		r = r.WithContext(auth.WithPrincipal(r.Context(), owner))
		r.Header.Set("Idempotency-Key", "k-"+c.method+c.path)
		if c.body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"UNAVAILABLE"`) || !strings.Contains(w.Body.String(), "mandates") {
			t.Errorf("%s %s = %d %s, want 503 UNAVAILABLE {component: mandates}", c.method, c.path, w.Code, w.Body)
		}
	}
	var n int
	if err := st.R.QueryRow(`SELECT COUNT(*) FROM mandates`).Scan(&n); err != nil || n != 0 {
		t.Errorf("mandates rows = %d (%v), want 0", n, err)
	}
}
