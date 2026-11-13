package generation_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/generation"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// While ingest is unavailable (the media tool probe is still pending) no paid batch may be estimated, created or
// approved: a take that cannot be stored must never cost GPU money. Reject still works (it releases the reservation).
// Once ingest recovers the same service accepts them again.
func TestIngestUnavailable_RefusesSpend(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	pendingBatch := r.pending() // created while ingest was up

	approvedBefore := r.count(`SELECT COUNT(*) FROM approvals WHERE state = 'approved'`)
	var up atomic.Bool
	o := r.opts
	o.IngestReady = up.Load
	svc := generation.New(o)
	ctx := context.Background()
	item := newSeedItem(lt.TrackCitta, 1)

	_, err := svc.Estimate(ctx, agent, generation.EstimateIn{Items: []generation.Item{item}, Via: viaFor(agent)})
	if pe := wantProblem(t, err, 503, "UNAVAILABLE"); pe.Details["component"] != "ingest" {
		t.Fatalf("estimate details = %v", pe.Details)
	}
	// a token minted while ingest was up cannot be spent either
	e := r.est(agent, item)
	_, err = svc.Create(ctx, agent, generation.CreateIn{Token: e.Token, Via: spend.ViaAPI}, func(generation.Created) (int, []byte, error) { return 201, nil, nil })
	if pe := wantProblem(t, err, 503, "UNAVAILABLE"); pe.Details["component"] != "ingest" {
		t.Fatalf("create details = %v", pe.Details)
	}
	_, err = svc.Approve(ctx, human, pendingBatch.BatchID, "ok")
	if pe := wantProblem(t, err, 503, "UNAVAILABLE"); pe.Details["component"] != "ingest" {
		t.Fatalf("approve details = %v", pe.Details)
	}
	if n := r.count(`SELECT COUNT(*) FROM approvals WHERE state = 'approved'`); n != approvedBefore {
		t.Fatalf("%d approvals recorded while ingest was down", n)
	}
	if _, err := svc.Reject(ctx, human, pendingBatch.BatchID, "hold"); err != nil {
		t.Fatalf("reject must keep working: %v", err)
	}

	up.Store(true)
	if _, err := svc.Estimate(ctx, agent, generation.EstimateIn{Items: []generation.Item{item}, Via: viaFor(agent)}); err != nil {
		t.Fatalf("estimate after recovery: %v", err)
	}
}
