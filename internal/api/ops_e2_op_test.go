package api_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engine/registry"
	"github.com/nicolasacchi/metamusic/internal/engineadopt"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/providertest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// E2-OP replaces the list_engines stub (TestOps_StubsAnswer501E2 then skips the 501 assertion).
func init() { implementedE2 = append(implementedE2, "list_engines") }

var t0 = time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)

type enginesRig struct {
	*rig
	st *store.Store
	ad *engineadopt.Service
}

type enginesCfg struct {
	ace         bool   // register acestep15
	aceBackend  string // "" = none, else the backend name (fake-acestep)
	mode        string // gate mode of the ACE backend: fake (default) or runpod without keys
	armed, trip bool
	adopted     bool
}

func newEnginesRig(t *testing.T, c enginesCfg) *enginesRig {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "e.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := func() time.Time { return t0 }
	ad := engineadopt.New(engineadopt.Options{Store: st, Now: now})
	if c.adopted {
		if _, err := ad.Adopt(context.Background(), "cli:test", "acestep15", "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	bes := []provider.Backend{providertest.New(provider.BackendFake, providertest.Script{})}
	gates := []*provider.Gate{provider.NewGate(st.R, provider.GateConfig{Mode: provider.ModeFake, KeysLoaded: true, ColdStartS: 180, MaxRuntime: 15 * time.Minute}, now)}
	arm := func(name string, on bool) {
		a := 0
		if on {
			a = 1
		}
		if _, err := st.W.Exec(`INSERT INTO provider_state(backend, armed, changed_by, changed_at) VALUES(?,?,'system:default',?)`, name, a, store.FormatTime(t0)); err != nil {
			t.Fatal(err)
		}
	}
	arm(provider.BackendFake, true)
	if c.aceBackend != "" {
		bes = append(bes, providertest.New(c.aceBackend, providertest.Script{}))
		mode := c.mode
		if mode == "" {
			mode = provider.ModeFake
		}
		gates = append(gates, provider.NewGate(st.R, provider.GateConfig{Mode: mode, Backend: c.aceBackend, Engine: provider.EngineACEStep, KeysLoaded: false, ColdStartS: 300, MaxRuntime: 15 * time.Minute}, now))
		arm(c.aceBackend, c.armed)
		if c.trip {
			if _, err := st.W.Exec(`INSERT INTO provider_trips(id, backend, tripped_at, reason, details_json, actions_json) VALUES('t1', ?, ?, 'crash_loop', '{}', '[]')`, c.aceBackend, store.FormatTime(t0)); err != nil {
				t.Fatal(err)
			}
		}
	}
	set, err := provider.NewSet(provider.BackendFake, bes...)
	if err != nil {
		t.Fatal(err)
	}
	gs, err := provider.NewGateSet(provider.BackendFake, gates...)
	if err != nil {
		t.Fatal(err)
	}
	d := api.Deps{Engines: registry.Default(registry.Options{ACEStep: c.ace}), Backends: set, Gates: gs, Adoption: ad, Store: st}
	return &enginesRig{rig: t1Rig(t, d), st: st, ad: ad}
}

var reader = auth.Principal{Kind: auth.KindAgent, ID: "tok-op", Name: "op", Scopes: []auth.Scope{auth.ScopeRead}, Via: "token"}

func (r *enginesRig) list(t *testing.T) (map[string]any, []map[string]any, string) {
	t.Helper()
	rec := r.callWith(reader, "GET", "/api/engines", "")
	if rec.Code != 200 {
		t.Fatalf("list_engines = %d %s", rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, e := range body["engines"].([]any) {
		out = append(out, e.(map[string]any))
	}
	return body, out, rec.Body.String()
}

func opStr(v any) string {
	s, _ := v.(string)
	return s
}

func TestListEngines_Selectable_Table(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name     string
		cfg      enginesCfg
		enabled  bool
		state    string
		want     string // unavailable_reason; "" = selectable
		wantHint bool
		backend  string
	}{
		{"not registered", enginesCfg{}, false, "none", "not_enabled", true, ""},
		{"registered, not adopted", enginesCfg{ace: true, aceBackend: "fake-acestep", armed: true}, true, "none", "not_adopted", true, "fake-acestep"},
		{"adopted and armed", enginesCfg{ace: true, aceBackend: "fake-acestep", armed: true, adopted: true}, true, "adopted", "", false, "fake-acestep"},
		{"adopted, disarmed", enginesCfg{ace: true, aceBackend: "fake-acestep", adopted: true}, true, "adopted", "disarmed", false, "fake-acestep"},
		{"adopted, armed, tripped", enginesCfg{ace: true, aceBackend: "fake-acestep", armed: true, trip: true, adopted: true}, true, "adopted", "tripped", false, "fake-acestep"},
		{"adopted, backend disabled (no keys)", enginesCfg{ace: true, aceBackend: "runpod-serverless-acestep", mode: provider.ModeRunPod, armed: true, adopted: true}, true, "adopted", "backend_disabled", false, "runpod-serverless-acestep"},
		{"adopted, no backend", enginesCfg{ace: true, adopted: true}, true, "adopted", "backend_absent", false, ""},
		{"not adopted wins over a missing backend", enginesCfg{ace: true}, true, "none", "not_adopted", true, ""},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			r := newEnginesRig(t, row.cfg)
			body, es, raw := r.list(t)
			if len(es) != 2 || opStr(es[0]["name"]) != "yue2" || opStr(es[1]["name"]) != "acestep15" || body["default_engine"] != "yue2" {
				t.Fatalf("engines = %v", raw)
			}
			ace := es[1]
			if ace["enabled"] != row.enabled || ace["selectable"] != (row.want == "") || opStr(ace["unavailable_reason"]) != row.want {
				t.Errorf("enabled %v selectable %v reason %v, want %v %v %q", ace["enabled"], ace["selectable"], ace["unavailable_reason"], row.enabled, row.want == "", row.want)
			}
			if got := opStr(ace["adoption"].(map[string]any)["state"]); got != row.state {
				t.Errorf("adoption state %q, want %q", got, row.state)
			}
			if hint, ok := ace["adopt_hint"].(string); ok != row.wantHint || (ok && !strings.Contains(hint, "engines adopt acestep15 --reason")) {
				t.Errorf("adopt_hint = %v", ace["adopt_hint"])
			}
			if ace["open_trial_id"] != nil {
				t.Errorf("open_trial_id = %v", ace["open_trial_id"])
			}
			if b, ok := ace["backend"].(map[string]any); (row.backend != "") != ok || (ok && opStr(b["name"]) != row.backend) {
				t.Errorf("backend = %v, want %q", ace["backend"], row.backend)
			}
			// yue2 is the seed: adopted, armed, selectable
			y := es[0]
			if y["selectable"] != true || opStr(y["adoption"].(map[string]any)["via"]) != "seed" || y["adopt_hint"] != nil || y["features"] != nil {
				t.Errorf("yue2 = %v", y)
			}
		})
	}
}

func TestListEngines_ACEShape(t *testing.T) {
	t.Parallel()
	r := newEnginesRig(t, enginesCfg{ace: true, aceBackend: "fake-acestep", trip: true, adopted: true})
	_, es, raw := r.list(t)
	ace := es[1]
	b := ace["backend"].(map[string]any)
	if b["tripped"] != true || opStr(b["trip_reason"]) != "crash_loop" || b["armed"] != false || b["cold_start_s"] == nil {
		t.Errorf("backend = %v", b)
	}
	caps := ace["caps"].(map[string]any)
	if caps["text2song"] != true || caps["max_seconds"] != float64(480) || caps["min_seconds"] != float64(10) || caps["extend"] != false {
		t.Errorf("caps = %v", caps)
	}
	tasks := caps["tasks"].([]any)
	if !slices.Contains(tasks, any("compose_new")) || slices.Contains(tasks, any("repaint")) {
		t.Errorf("tasks = %v: only the verified tasks may be offered", tasks)
	}
	f := ace["features"].(map[string]any)
	if f["verified_at"] != "1970-01-01" {
		t.Errorf("features = %v", f)
	}
	un := f["unverified"].([]any)
	for _, w := range []string{"repaint", "extend", "complete", "extract", "reference_audio", "lrc"} {
		if !slices.Contains(un, any(w)) {
			t.Errorf("unverified %v lacks %s", un, w)
		}
	}
	if slices.Contains(un, any("text2music")) {
		t.Errorf("text2music is verified: %v", un)
	}
	pools := f["pools"].([]any)
	if len(pools) != 2 || pools[0].(map[string]any)["max_s"] != float64(480) || pools[0].(map[string]any)["verified"] != false {
		t.Errorf("pools = %v", pools)
	}
	if tc := ace["task_classes"].([]any); len(tc) != 1 || tc[0] != "turbo" {
		t.Errorf("task_classes = %v", tc)
	}
	if w := ace["warnings"].([]any); len(w) < 2 {
		t.Errorf("warnings = %v (want the canary note and the trip)", w)
	}
	// no endpoint id, key, url or worker reference anywhere in the answer (TestNoSecretLeak_E2)
	for _, bad := range []string{"endpoint_id", "api_key", "https://", "runpod.ai", "request_urls", "Bearer"} {
		if strings.Contains(raw, bad) {
			t.Errorf("list_engines leaks %q", bad)
		}
	}
}

func TestListEngines_AdoptionIsHumanCLIOnly(t *testing.T) {
	t.Parallel()
	r := newEnginesRig(t, enginesCfg{ace: true, aceBackend: "fake-acestep", armed: true})
	// there is no operation, path or verb that adopts: the hint is text and every non-GET on /api/engines is refused
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		for _, path := range []string{"/api/engines", "/api/engines/acestep15", "/api/engines/acestep15/adopt", "/human/api/engines/acestep15/adopt"} {
			agent := auth.Principal{Kind: auth.KindAgent, ID: "tok-a", Name: "a", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate, auth.ScopeAdmin}, Via: "token"}
			if rec := r.callWith(agent, m, path, `{"reason":"x"}`); rec.Code < 400 {
				t.Errorf("%s %s answered %d", m, path, rec.Code)
			}
		}
	}
	a, _ := newTestAPI(t)
	for _, op := range api.Operations(a) {
		if strings.Contains(op.ID, "adopt") {
			t.Errorf("operation %s looks like adoption", op.ID)
		}
	}
	if st, _ := r.ad.State(context.Background(), r.st.R, "acestep15"); st.Adopted() {
		t.Error("an HTTP call adopted the engine")
	}
	if n := 0; r.st.R.QueryRow(`SELECT COUNT(*) FROM engine_adoption_log WHERE engine = 'acestep15'`).Scan(&n) != nil || n != 0 {
		t.Errorf("%d adoption rows written by HTTP", n)
	}
}

func TestListEngines_Unavailable(t *testing.T) {
	t.Parallel()
	for _, d := range []api.Deps{{}, {Engines: registry.Default(registry.Options{})}} {
		rec := t1Rig(t, d).callWith(reader, "GET", "/api/engines", "")
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		if rec.Code != 503 || m["code"] != "UNAVAILABLE" {
			t.Errorf("%d %v", rec.Code, m)
		}
	}
}

func TestGetSystemInfo_Engines(t *testing.T) {
	t.Parallel()
	for _, ace := range []bool{false, true} {
		r := t1Rig(t, api.Deps{Engines: registry.Default(registry.Options{ACEStep: ace})})
		rec := r.callWith(reader, "GET", "/api/system", "")
		var m struct {
			Engines []string `json:"engines"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil || rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		want := []string{"yue2"}
		if ace {
			want = []string{"acestep15", "yue2"}
		}
		if !slices.Equal(m.Engines, want) {
			t.Errorf("engines = %v, want %v", m.Engines, want)
		}
	}
}
