package provider_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	os.Exit(m.Run())
}

var gateNow = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

func goodQuote(at time.Time) provider.Quote {
	idle := 20
	return provider.Quote{Backend: provider.BackendRunPod, ListPricePPH: 690000, ObservedAt: at,
		Endpoint: provider.EndpointConfig{Name: "ep", Type: "QUEUE", ScalerType: "QUEUE_DELAY", ScalerValue: "240", WorkersMax: 2, IdleTimeoutS: &idle,
			TimeoutMS: 900000, GPUCount: 1, RequestURLs: map[string]string{"run": "https://api.runpod.ai/v2/x/run"}}}
}

func openGateStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func putState(t *testing.T, st *store.Store, backend string, armed int, q *provider.Quote) {
	t.Helper()
	var qj, qa any
	if q != nil {
		b, _ := json.Marshal(q)
		qj, qa = string(b), store.FormatTime(q.ObservedAt)
	}
	if _, err := st.W.Exec(`INSERT INTO provider_state(backend, armed, changed_by, changed_at, quote_json, quote_at) VALUES (?,?,?,?,?,?)`,
		backend, armed, "system:default", store.FormatTime(gateNow), qj, qa); err != nil {
		t.Fatal(err)
	}
}

// TestGate_Reasons: the C68 table, computed from local facts only.
func TestGate_Reasons(t *testing.T) {
	t.Parallel()
	mut := func(f func(q *provider.Quote)) *provider.Quote {
		q := goodQuote(gateNow.Add(-time.Minute))
		f(&q)
		return &q
	}
	zero := 0
	good := goodQuote(gateNow.Add(-time.Minute))
	old := goodQuote(gateNow.Add(-2 * time.Hour))
	for _, tc := range []struct {
		name  string
		mode  string
		keys  bool
		quote *provider.Quote
		want  string
	}{
		{"none", provider.ModeNone, false, nil, provider.ReasonBackendNone},
		{"fake needs nothing", provider.ModeFake, false, nil, ""},
		{"runpod without keys", provider.ModeRunPod, false, &good, provider.ReasonKeysAbsent},
		{"runpod without a quote", provider.ModeRunPod, true, nil, provider.ReasonQuoteStale},
		{"runpod stale quote", provider.ModeRunPod, true, &old, provider.ReasonQuoteStale},
		{"runpod good quote", provider.ModeRunPod, true, &good, ""},
		{"inactive endpoint", provider.ModeRunPod, true, mut(func(q *provider.Quote) { q.Endpoint.WorkersMax = 0 }), provider.ReasonEndpointInactive},
		{"too many workers", provider.ModeRunPod, true, mut(func(q *provider.Quote) { q.Endpoint.WorkersMax = 3 }), provider.ReasonEndpointConfig},
		{"no idle timeout", provider.ModeRunPod, true, mut(func(q *provider.Quote) { q.Endpoint.IdleTimeoutS = nil }), provider.ReasonEndpointConfig},
		{"wrong scaler", provider.ModeRunPod, true, mut(func(q *provider.Quote) { q.Endpoint.ScalerType = "REQUEST_COUNT" }), provider.ReasonEndpointConfig},
		{"wrong queue delay", provider.ModeRunPod, true, mut(func(q *provider.Quote) { q.Endpoint.ScalerValue = "5" }), provider.ReasonEndpointConfig},
		{"wrong timeout", provider.ModeRunPod, true, mut(func(q *provider.Quote) { q.Endpoint.TimeoutMS = 1000 }), provider.ReasonEndpointConfig},
		{"two gpus", provider.ModeRunPod, true, mut(func(q *provider.Quote) { q.Endpoint.GPUCount = 2 }), provider.ReasonEndpointConfig},
		{"foreign request host", provider.ModeRunPod, true, mut(func(q *provider.Quote) {
			q.Endpoint.RequestURLs = map[string]string{"run": "https://evil.example.com/x"}
		}), provider.ReasonEndpointConfig},
		{"no price", provider.ModeRunPod, true, mut(func(q *provider.Quote) { q.ListPricePPH = money.MicroUSD(zero) }), provider.ReasonCatalogUnreadable},
		{"sweep unreadable", provider.ModeRunPod, true, mut(func(q *provider.Quote) { q.SweepUnreadable = true }), provider.ReasonSweepUnreadable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := openGateStore(t)
			g := provider.NewGate(st.R, provider.GateConfig{Mode: tc.mode, KeysLoaded: tc.keys, ColdStartS: 180, MaxRuntime: 15 * time.Minute}, func() time.Time { return gateNow })
			if name := g.Config().BackendName(); name != "" {
				putState(t, st, name, 1, tc.quote)
			}
			s, err := g.State(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if s.Reason != tc.want || s.Enabled != (tc.want == "") {
				t.Errorf("enabled=%v reason=%q, want reason %q", s.Enabled, s.Reason, tc.want)
			}
		})
	}
}

func TestGate_ArmedAndTripped(t *testing.T) {
	t.Parallel()
	st := openGateStore(t)
	g := provider.NewGate(st.R, provider.GateConfig{Mode: provider.ModeFake}, func() time.Time { return gateNow })
	s, err := g.State(context.Background())
	if err != nil || s.Armed || s.Tripped || !s.Enabled {
		t.Fatalf("no row: %+v %v", s, err)
	}
	putState(t, st, provider.BackendFake, 1, nil)
	if _, err := st.W.Exec(`INSERT INTO provider_trips(id, backend, tripped_at, reason, details_json, actions_json) VALUES ('t1', 'fake', ?, 'stuck_worker', '{}', '[]')`, store.FormatTime(gateNow)); err != nil {
		t.Fatal(err)
	}
	s, err = g.State(context.Background())
	if err != nil || !s.Armed || !s.Tripped || s.TripID != "t1" || s.TripReason != "stuck_worker" || s.ArmedChangedBy != "system:default" {
		t.Fatalf("armed+tripped: %+v %v", s, err)
	}
}
