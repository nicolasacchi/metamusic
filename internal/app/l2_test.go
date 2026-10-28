package app_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/app"
)

// TestApp_L2Wiring: Build always constructs the L2 services (the operations answer 501 until their lanes land, never 503), shares
// them between Services, the API dependencies and the HTTP options, and builds the Syncer and the tape runner without starting
// them.
func TestApp_L2Wiring(t *testing.T) {
	t.Parallel()
	none := ""
	s := buildWith(t, nil, app.Options{L2: app.L2Options{FlacVersion: &none}})
	if s.Records == nil || s.Syncer == nil || s.RecordView == nil || s.Crates == nil || s.Tapes == nil || s.Curation == nil || s.Albums == nil {
		t.Fatalf("an L2 service is missing: %+v", s)
	}
	d, o := s.Deps, s.Server
	if d.Records != s.Records || d.RecordView != s.RecordView || d.Crates != s.Crates || d.Tapes != s.Tapes || d.Curation != s.Curation || d.Albums != s.Albums {
		t.Error("api.Deps must carry the services Build built")
	}
	if o.Records != s.Records || o.RecordView != s.RecordView || o.Crates != s.Crates || o.Tapes != s.Tapes || o.Curation != s.Curation || o.Albums != s.Albums {
		t.Error("httpserver.Options must carry the services Build built")
	}
}

// TestApp_L2MediaChainFallsThrough: the tape resolver is on the media chain, and a stub resolver that owns nothing answers
// ErrNotFound, so a sha no resolver knows is a plain 404 (never a 500) and the other resolvers keep serving.
func TestApp_L2MediaChainFallsThrough(t *testing.T) {
	t.Parallel()
	none := ""
	s := buildWith(t, nil, app.Options{L2: app.L2Options{FlacVersion: &none}})
	u := s.Deps.Signer.Sign(strings.Repeat("ab", 32), "flac", time.Hour, time.Minute).URL
	req := httptest.NewRequest("GET", u, nil)
	rec := httptest.NewRecorder()
	s.Server.Media.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown media object: %d, want 404", rec.Code)
	}
}
