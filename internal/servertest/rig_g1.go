package servertest

import (
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/provider"
)

// OpenCaps opens the worker capabilities caps for backend, as `metamusic runpod contract open` would (C322.6): it appends a CLI row
// to the gate log bound to the image digest of the backend's fresh cached quote, or, for a simulated backend whose quote is absent,
// to the fake image's digest. With no caps it closes everything. A simulated backend that has never had a row has every capability
// open, so a test that wants a closed gate calls OpenCaps(tb, backend) first.
func (r *Rig) OpenCaps(tb testing.TB, backend string, caps ...string) {
	tb.Helper()
	ctx := tb.Context()
	qt, err := provider.ReadQuote(ctx, r.Store.R, backend)
	if err != nil {
		tb.Fatal(err)
	}
	digest := provider.QuoteImageDigest(qt, r.Clock.Now())
	if digest == "" {
		digest = "sha256:" + strings.Repeat("0", 64) // the simulated worker image
	}
	if err := provider.AppendContract(ctx, r.Store.W, provider.WorkerContractRow{Backend: backend, ImageDigest: digest, Caps: caps,
		Via: provider.ContractViaCLI, ChangedBy: "system:test", ChangedAt: r.Clock.Now(), Reason: "test rig"}); err != nil {
		tb.Fatal(err)
	}
}
