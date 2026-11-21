package provider_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/provider"
)

const (
	digestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func quoteWith(digest string, at time.Time) *provider.Quote {
	q := goodQuote(at)
	q.Endpoint.ImageDigest = digest
	return &q
}

// TestWorkerCaps_DigestBinding: the open set is the latest row's caps while the fresh quote reports the row's image digest, and
// nothing otherwise (a new image, a stale quote, a backend with no row).
func TestWorkerCaps_DigestBinding(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	st := openGateStore(t)
	be := provider.BackendRunPod
	caps := func(q *provider.Quote, at time.Time) []string {
		t.Helper()
		got, err := provider.WorkerCaps(ctx, st.R, be, q, at)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := caps(quoteWith(digestA, gateNow), gateNow); len(got) != 0 || got == nil {
		t.Fatalf("no row: %v (want empty, non-nil)", got)
	}
	add := func(digest string, c ...string) {
		t.Helper()
		if err := provider.AppendContract(ctx, st.W, provider.WorkerContractRow{Backend: be, ImageDigest: digest, Caps: c, Via: provider.ContractViaCLI,
			ChangedBy: "system:cli", ChangedAt: gateNow, Reason: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	add(digestA, "v2", "ears", "v2") // duplicates and order are normalised
	for _, tc := range []struct {
		name string
		q    *provider.Quote
		at   time.Time
		want []string
	}{
		{"same image", quoteWith(digestA, gateNow), gateNow, []string{"ears", "v2"}},
		{"a new image closes everything", quoteWith(digestB, gateNow), gateNow, []string{}},
		{"stale quote", quoteWith(digestA, gateNow), gateNow.Add(provider.QuoteMaxAge + time.Second), []string{}},
		{"no quote", nil, gateNow, []string{}},
		{"quote without a digest", quoteWith("", gateNow), gateNow, []string{}},
	} {
		if got := caps(tc.q, tc.at); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	add(digestA, "v2") // the latest row wins
	if got := caps(quoteWith(digestA, gateNow), gateNow); !slices.Equal(got, []string{"v2"}) {
		t.Errorf("latest row: %v", got)
	}
	add(digestB)
	if got := caps(quoteWith(digestB, gateNow), gateNow); len(got) != 0 {
		t.Errorf("closed row: %v", got)
	}
}

// TestWorkerCaps_FakeBackends: a simulated backend has every capability until a row says otherwise, and its digest is not compared.
func TestWorkerCaps_FakeBackends(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	st := openGateStore(t)
	got, err := provider.WorkerCaps(ctx, st.R, provider.BackendFake, nil, gateNow)
	if err != nil || !slices.Equal(got, provider.Caps) {
		t.Fatalf("fake without a row: %v %v", got, err)
	}
	if err := provider.AppendContract(ctx, st.W, provider.WorkerContractRow{Backend: provider.BackendFake, ImageDigest: digestA, Caps: []string{"v2"},
		Via: provider.ContractViaCLI, ChangedBy: "system:cli", ChangedAt: gateNow, Reason: "t"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := provider.WorkerCaps(ctx, st.R, provider.BackendFake, nil, gateNow); !slices.Equal(got, []string{"v2"}) {
		t.Fatalf("fake with a row: %v", got)
	}
}

func TestNormalizeCaps(t *testing.T) {
	t.Parallel()
	if got, err := provider.NormalizeCaps([]string{" v2 ", "ears", "v2"}); err != nil || !slices.Equal(got, []string{"ears", "v2"}) {
		t.Errorf("%v %v", got, err)
	}
	if got, err := provider.NormalizeCaps(nil); err != nil || got == nil || len(got) != 0 {
		t.Errorf("empty: %v %v", got, err)
	}
	if _, err := provider.NormalizeCaps([]string{"v3"}); err == nil || !strings.Contains(err.Error(), "v3") {
		t.Errorf("unknown cap: %v", err)
	}
	for _, c := range provider.Caps {
		if !provider.ValidCap(c) {
			t.Errorf("%s not valid", c)
		}
	}
	if !slices.Equal(provider.MissingCaps([]string{"ears", "v2", "plan"}, []string{"v2"}), []string{"ears", "plan"}) {
		t.Error("MissingCaps")
	}
}

func TestAppendContract_Refusals(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	st := openGateStore(t)
	row := provider.WorkerContractRow{Backend: "b", ImageDigest: digestA, Caps: []string{"v2"}, Via: provider.ContractViaCLI, ChangedBy: "system:cli", ChangedAt: gateNow, Reason: "t"}
	bad := []struct {
		name string
		mut  func(*provider.WorkerContractRow)
	}{
		{"unknown cap", func(r *provider.WorkerContractRow) { r.Caps = []string{"nope"} }},
		{"auto without an attempt", func(r *provider.WorkerContractRow) { r.Via = provider.ContractViaAuto }},
		{"cli with an attempt", func(r *provider.WorkerContractRow) { r.AttemptID = "x" }},
		{"empty reason", func(r *provider.WorkerContractRow) { r.Reason = "" }},
		{"reason over 500", func(r *provider.WorkerContractRow) { r.Reason = strings.Repeat("x", 501) }},
		{"empty digest", func(r *provider.WorkerContractRow) { r.ImageDigest = "" }},
	}
	for _, tc := range bad {
		r := row
		tc.mut(&r)
		if err := provider.AppendContract(ctx, st.W, r); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
	if err := provider.AppendContract(ctx, st.W, row); err != nil {
		t.Fatal(err)
	}
	// append-only
	if _, err := st.W.ExecContext(ctx, `UPDATE worker_contract_log SET reason = 'x'`); err == nil {
		t.Error("update accepted")
	}
	if _, err := st.W.ExecContext(ctx, `DELETE FROM worker_contract_log`); err == nil {
		t.Error("delete accepted")
	}
}
