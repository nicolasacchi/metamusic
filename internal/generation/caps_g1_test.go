package generation_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/generation"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

const v2Canary = "mm-canary-20261121-v2-wire-1"

// setCaps appends a CLI row for the fake backend: the capabilities the operator has opened ("none" is a closed gate).
func (r *rig) setCaps(caps ...string) {
	r.t.Helper()
	if err := provider.AppendContract(context.Background(), r.st.W, provider.WorkerContractRow{Backend: provider.BackendFake,
		ImageDigest: "sha256:" + strings.Repeat("3", 64), Caps: caps, Via: provider.ContractViaCLI, ChangedBy: "system:cli", ChangedAt: r.clk.Now(), Reason: "test"}); err != nil {
		r.t.Fatal(err)
	}
}

func v2Item() generation.Item {
	return generation.Item{Mode: lineage.ModeComposeNew, Recipe: recipe(v2Canary)}
}

func wantWorkerOutdated(t *testing.T, err error, missing ...string) {
	t.Helper()
	var pe *problem.Error
	if !errors.As(err, &pe) || pe.Status != http.StatusServiceUnavailable || pe.Code != "GENERATION_DISABLED" {
		t.Fatalf("err = %v, want 503 GENERATION_DISABLED", err)
	}
	if pe.Details["reason"] != generation.ReasonWorkerOutdated || pe.Details["backend"] != provider.BackendFake {
		t.Fatalf("details = %v", pe.Details)
	}
	got, _ := pe.Details["missing"].([]string)
	if !slices.Equal(got, missing) {
		t.Fatalf("missing = %v, want %v", got, missing)
	}
}

func TestEstimate_RefusesClosedNeeds(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.setCaps() // closed
	estimates, ledger := r.count(`SELECT COUNT(*) FROM estimates`), r.count(`SELECT COUNT(*) FROM ledger`)
	_, err := r.svc.Estimate(context.Background(), human, generation.EstimateIn{Items: []generation.Item{v2Item()}, Via: spend.ViaHumanCreate})
	wantWorkerOutdated(t, err, "v2")
	if n := r.count(`SELECT COUNT(*) FROM estimates`); n != estimates {
		t.Errorf("a refused estimate left %d rows", n-estimates)
	}
	if n := r.count(`SELECT COUNT(*) FROM ledger`); n != ledger {
		t.Errorf("a refused estimate wrote ledger rows")
	}
	// a plain render is untouched by the closed gate
	r.est(human, newSeedItem(lt.TrackLuna, 1))
	r.est(human, generation.Item{Mode: lineage.ModeComposeNew, Recipe: recipe("syn-a")})
	// opened: the canary take is estimable and carries needs [v2]
	r.setCaps("v2")
	e := r.est(human, v2Item())
	if got := r.str(`SELECT items_json FROM estimates WHERE id = ?`, e.ID); !strings.Contains(got, `"needs":["v2"]`) {
		t.Errorf("items_json = %.300s", got)
	}
	c := r.mustCreate(human, e, spend.ViaHumanCreate)
	if got := r.str(`SELECT needs_json FROM jobs WHERE batch_id = ?`, c.BatchID); got != `["v2"]` {
		t.Errorf("job needs = %s", got)
	}
}

func TestEstimate_PlainCanaryIDsStayPlain(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.setCaps()
	for _, id := range []string{"mm-canary-20261121-main-1", "my-v2-wire-song", "mm-canary-20261121-cot-full-2"} {
		e := r.est(human, generation.Item{Mode: lineage.ModeComposeNew, Recipe: recipe(id)})
		if got := r.str(`SELECT items_json FROM estimates WHERE id = ?`, e.ID); strings.Contains(got, `"needs"`) {
			t.Errorf("%s: a plain render carries needs: %.200s", id, got)
		}
	}
}

func TestCreate_RechecksNeeds(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.setCaps("v2")
	e := r.est(human, v2Item())
	r.setCaps() // the capability closes between estimate and create
	batches := r.count(`SELECT COUNT(*) FROM batches`)
	_, err := r.create(human, e, spend.ViaHumanCreate)
	wantWorkerOutdated(t, err, "v2")
	if n := r.count(`SELECT COUNT(*) FROM batches`); n != batches {
		t.Errorf("a refused create left a batch")
	}
	if n := r.count(`SELECT COUNT(*) FROM estimates WHERE id = ? AND consumed_at IS NOT NULL`, e.ID); n != 0 {
		t.Errorf("the token was consumed by a refusal")
	}
	r.setCaps("v2") // reopened: the same token creates
	r.mustCreate(human, e, spend.ViaHumanCreate)
}

// recipe is a compose_new request with the given request id.
func recipe(id string) []byte {
	return []byte(`{"id":"` + id + `","style":"indie pop, 100 bpm, female vocal","lyrics":"[Verse 1]\nline one\nline two\n[Chorus]\nline three\nline four","cot":"full","seed":4000}`)
}
