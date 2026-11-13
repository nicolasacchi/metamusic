package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/app"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// Build with an ffmpeg that cannot be probed (the production slow-boot shape) wires the refusal end to end: generation
// answers 503 UNAVAILABLE {component: ingest}, /healthz reports media_tools, and a healed probe lifts both.
func TestApp_BuildWithPendingProbeRefusesSpend(t *testing.T) {
	t.Parallel()
	s, _ := build(t, app.Options{})
	if s.Media == nil || s.Media.Lazy == nil || s.Media.Lazy.Ready() {
		t.Fatalf("probe should be pending: %+v", s.Media)
	}
	p := auth.Principal{Kind: auth.KindHuman, ID: "u", Name: "u", Via: "proxy"}
	_, err := s.Generation.Estimate(context.Background(), p, generation.EstimateIn{})
	var pe *problem.Error
	if !errors.As(err, &pe) || pe.Status != 503 || pe.Code != "UNAVAILABLE" || pe.Details["component"] != "ingest" {
		t.Fatalf("estimate: %v", err)
	}
	if got := s.Server.Degraded(); len(got) != 1 || got[0] != "media_tools" {
		t.Fatalf("healthz degraded = %v", got)
	}
}
