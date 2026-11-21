package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/providertest"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type fakeFallback struct {
	configured bool
	pending    int
	last       *time.Time
	err        error
}

func (f fakeFallback) Status(context.Context) (bool, int, *time.Time, error) {
	return f.configured, f.pending, f.last, f.err
}

func fallbackRig(t *testing.T, r api.UploadFallbackReader) *jobsRig {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := func() time.Time { return t0 }
	q, _ := json.Marshal(providertest.GoodQuote(provider.BackendFake, t0))
	if _, err := st.W.Exec(`INSERT INTO provider_state(backend, armed, changed_by, changed_at, quote_json, quote_at) VALUES(?,1,'system:default',?,?,?)`,
		provider.BackendFake, store.FormatTime(t0), string(q), store.FormatTime(t0)); err != nil {
		t.Fatal(err)
	}
	gate := provider.NewGate(st.R, provider.GateConfig{Mode: provider.ModeFake, KeysLoaded: true, ColdStartS: 180, MaxRuntime: 15 * time.Minute}, now)
	sp := spend.New(spend.Options{Store: st, Now: now})
	admin := jobs.NewProviderAdmin(jobs.ProviderOptions{Store: st, Gate: gate, KillSlackS: 15, TTL: 2 * time.Hour, PriceMarginPPM: 100000})
	mux := http.NewServeMux()
	api.New(mux, api.Deps{Store: st, Jobs: jobs.New(jobs.Options{Store: st, Spend: sp}), Provider: admin, Now: now, UploadFallback: r})
	return &jobsRig{rig: &rig{mux: mux}, st: st, gate: gate, admin: admin}
}

func TestProviderStatus_UploadFallback(t *testing.T) {
	t.Parallel()
	last := time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC)
	for name, tc := range map[string]struct {
		reader api.UploadFallbackReader
		want   map[string]any
	}{
		"no reader":    {nil, map[string]any{"configured": false, "objects_pending": float64(0), "last_used_at": nil}},
		"unconfigured": {fakeFallback{}, map[string]any{"configured": false, "objects_pending": float64(0), "last_used_at": nil}},
		"configured":   {fakeFallback{configured: true, pending: 4, last: &last}, map[string]any{"configured": true, "objects_pending": float64(4), "last_used_at": "2026-11-20T01:02:03.000Z"}},
		"a read fault": {fakeFallback{configured: true, err: errors.New("boom")}, map[string]any{"configured": true, "objects_pending": float64(0), "last_used_at": nil}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := fallbackRig(t, tc.reader)
			for _, call := range []func() map[string]any{
				func() map[string]any { return r.must(t, human, "GET", "/api/provider", "", 200) },
				func() map[string]any {
					return r.must(t, human, "POST", "/human/api/provider/state", `{"armed":true,"reason":"go"}`, 200)
				},
			} {
				got, ok := call()["upload_fallback"].(map[string]any)
				if !ok {
					t.Fatal("no upload_fallback block")
				}
				if len(got) != len(tc.want) {
					t.Errorf("block keys = %v", got)
				}
				for k, v := range tc.want {
					if got[k] != v {
						t.Errorf("%s = %v, want %v", k, got[k], v)
					}
				}
			}
		})
	}
}

// TestProviderStatus_UploadFallbackLeaksNothing: the status body never names the endpoint, bucket, prefix, a key or a URL.
func TestProviderStatus_UploadFallbackLeaksNothing(t *testing.T) {
	t.Parallel()
	r := fallbackRig(t, fakeFallback{configured: true, pending: 1})
	rec := r.callWith(human, "GET", "/api/provider", "")
	for _, bad := range []string{"bucket", "prefix", "x-amz", "r2.env", "secret", "https://"} {
		if strings.Contains(strings.ToLower(rec.Body.String()), bad) {
			t.Errorf("get_provider_status leaks %q", bad)
		}
	}
}
