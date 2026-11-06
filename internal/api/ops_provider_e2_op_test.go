package api_test

import (
	"context"
	"encoding/json"
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

const aceBackend = "fake-acestep"

// newProviderRig serves two backends (fake and fake-acestep) through the real provider admin with spend wired for the bound.
// aceArmed is the starting state of the ACE-Step row.
func newProviderRig(t *testing.T, aceArmed bool) *jobsRig {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := func() time.Time { return t0 }
	for _, b := range []struct {
		name  string
		armed int
	}{{provider.BackendFake, 1}, {aceBackend, map[bool]int{true: 1, false: 0}[aceArmed]}} {
		q, _ := json.Marshal(providertest.GoodQuote(b.name, t0))
		if _, err := st.W.Exec(`INSERT INTO provider_state(backend, armed, changed_by, changed_at, quote_json, quote_at) VALUES(?,?,'system:default',?,?,?)`,
			b.name, b.armed, store.FormatTime(t0), string(q), store.FormatTime(t0)); err != nil {
			t.Fatal(err)
		}
	}
	def := provider.NewGate(st.R, provider.GateConfig{Mode: provider.ModeFake, KeysLoaded: true, ColdStartS: 180, MaxRuntime: 15 * time.Minute}, now)
	ace := provider.NewGate(st.R, provider.GateConfig{Mode: provider.ModeFake, Backend: aceBackend, Engine: provider.EngineACEStep, KeysLoaded: true, ColdStartS: 300, MaxRuntime: 15 * time.Minute}, now)
	gs, err := provider.NewGateSet(provider.BackendFake, def, ace)
	if err != nil {
		t.Fatal(err)
	}
	sp := spend.New(spend.Options{Store: st, Now: now})
	admin := jobs.NewProviderAdmin(jobs.ProviderOptions{Store: st, Gate: def, KillSlackS: 15, TTL: 2 * time.Hour, PriceMarginPPM: 100000})
	admin.UseBackends(gs, nil, nil)
	admin.UseBound(jobs.BoundInputs{Spend: sp})
	mux := http.NewServeMux()
	api.New(mux, api.Deps{Store: st, Jobs: jobs.New(jobs.Options{Store: st, Spend: sp}), Provider: admin, Now: now})
	return &jobsRig{rig: &rig{mux: mux}, st: st, gate: def, admin: admin}
}

// The bound of two armed backends at the rig's prices (hand-computed in jobs/provider_e2_op_test.go).
const (
	apiGBoth = 4 * 759000
	apiXBoth = 2*230863 + 2*256163
)

func TestGetProviderStatus_PerBackend(t *testing.T) {
	t.Parallel()
	r := newProviderRig(t, true)
	m := r.must(t, human, "GET", "/api/provider", "", 200)
	// the top level keeps the M3 fields of the default backend
	if m["backend"] != "fake" || m["engine"] != "yue2" || m["armed"] != true || m["cold_start_s"] != float64(180) {
		t.Errorf("top level = %v", m)
	}
	bs := m["backends"].([]any)
	if len(bs) != 2 {
		t.Fatalf("backends = %v", bs)
	}
	y, a := bs[0].(map[string]any), bs[1].(map[string]any)
	if y["backend"] != "fake" || y["engine"] != "yue2" || a["backend"] != aceBackend || a["engine"] != "acestep15" || a["cold_start_s"] != float64(300) || a["armed"] != true {
		t.Errorf("backends = %v", bs)
	}
	gb := m["global_bound"].(map[string]any)
	if gb["g_micro_per_hour"] != float64(apiGBoth) || gb["x_micro"] != float64(apiXBoth) || len(gb["backends"].([]any)) != 2 || gb["g_usd_per_hour"] == "0" || gb["x_usd"] == "0" {
		t.Errorf("global_bound = %v", gb)
	}
	// a disarmed ACE-Step backend contributes nothing
	r.must(t, human, "POST", "/human/api/provider/state", `{"armed":false,"reason":"off","backend":"fake-acestep"}`, 200)
	gb = r.must(t, human, "GET", "/api/provider", "", 200)["global_bound"].(map[string]any)
	if gb["g_micro_per_hour"] != float64(2*759000) || len(gb["backends"].([]any)) != 1 {
		t.Errorf("global_bound with ACE disarmed = %v", gb)
	}
	// no endpoint id, key or url is ever shown
	rec := r.callWith(human, "GET", "/api/provider", "")
	for _, bad := range []string{"api.runpod.ai", "request_urls", "endpoint_id"} {
		if strings.Contains(rec.Body.String(), bad) {
			t.Errorf("get_provider_status leaks %q", bad)
		}
	}
}

func TestUpdateProviderState_PerBackend_API(t *testing.T) {
	t.Parallel()
	r := newProviderRig(t, false)
	// arming ACE-Step answers with that backend on top and never touches yue2
	m := r.must(t, human, "POST", "/human/api/provider/state", `{"armed":true,"reason":"go","backend":"fake-acestep"}`, 200)
	if m["backend"] != aceBackend || m["engine"] != "acestep15" || m["armed"] != true {
		t.Errorf("arm ACE = %v", m)
	}
	if def := r.must(t, human, "GET", "/api/provider", "", 200); def["backend"] != "fake" || def["armed"] != true {
		t.Errorf("default after arming ACE = %v", def)
	}
	// disarming yue2 (no backend named) leaves ACE-Step armed
	m = r.must(t, human, "POST", "/human/api/provider/state", `{"armed":false,"reason":"maintenance"}`, 200)
	if m["backend"] != "fake" || m["armed"] != false {
		t.Errorf("disarm default = %v", m)
	}
	for _, b := range m["backends"].([]any) {
		if b := b.(map[string]any); b["backend"] == aceBackend && b["armed"] != true {
			t.Errorf("disarming yue2 disarmed ACE: %v", b)
		}
	}
	// an unknown backend is BACKEND_UNAVAILABLE
	e := r.fail(t, human, "POST", "/human/api/provider/state", `{"armed":true,"reason":"x","backend":"nope"}`, 422, "BACKEND_UNAVAILABLE")
	if d := e["details"].(map[string]any); d["backend"] != "nope" {
		t.Errorf("details = %v", d)
	}
}

func TestUpdateProviderState_ConcurrencyExceedsCap_API(t *testing.T) {
	t.Parallel()
	r := newProviderRig(t, false)
	if _, err := r.st.W.Exec(`UPDATE spend_caps SET batch_micro_usd = ? WHERE id = 1`, apiXBoth-1); err != nil {
		t.Fatal(err)
	}
	e := r.fail(t, human, "POST", "/human/api/provider/state", `{"armed":true,"reason":"go","backend":"fake-acestep"}`, 422, "CONCURRENCY_EXCEEDS_CAP")
	d := e["details"].(map[string]any)
	if d["g_micro_per_hour"] != float64(apiGBoth) || d["x_micro"] != float64(apiXBoth) || len(d["backends"].([]any)) != 2 {
		t.Errorf("details = %v", d)
	}
	if m := r.must(t, human, "GET", "/api/provider", "", 200); m["backends"].([]any)[1].(map[string]any)["armed"] != false {
		t.Error("the refused arm changed the row")
	}
	// raising the ceiling lets the arm through; disarming is never refused
	if _, err := r.st.W.Exec(`UPDATE spend_caps SET batch_micro_usd = ? WHERE id = 1`, apiXBoth); err != nil {
		t.Fatal(err)
	}
	r.must(t, human, "POST", "/human/api/provider/state", `{"armed":true,"reason":"go","backend":"fake-acestep"}`, 200)
}
