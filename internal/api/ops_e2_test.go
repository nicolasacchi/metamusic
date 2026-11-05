package api_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engine"
	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// implementedE2 lists the E2 operations whose stub a lane has replaced; a lane appends its ids from its own file's init()
// (ops_e2_<lane>_test.go) and never edits this file (D-E2-37).
var implementedE2 []string

// TestRegistry_E2Meta: list_engines is registered once, free, read scope, a generic MCP operation (D-E2-35: no dedicated
// tool), with the /engines UI route and no human-only flag. The trial and Suite operations of E2c are not registered here.
func TestRegistry_E2Meta(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	var found []api.OpInfo
	for _, op := range api.Operations(a) {
		if op.ID == "list_engines" {
			found = append(found, op)
		}
		for _, e2c := range []string{"propose_engine_trial", "list_engine_trials", "get_engine_trial", "create_trial_duels", "abandon_engine_trial", "decide_engine_trial", "create_suite", "get_suite", "update_suite"} {
			if op.ID == e2c {
				t.Errorf("%s belongs to E2c and must not be registered by the E2a/E2b freeze", e2c)
			}
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d list_engines operations", len(found))
	}
	op := found[0]
	if op.Method != "GET" || op.Path != "/api/engines" || op.Meta.Scope != auth.ScopeRead || op.Meta.Cost != "free" || op.Meta.MCP != "generic" ||
		op.Meta.UI != "/engines" || op.Meta.HumanOnly || op.Meta.Idempotency != "" || op.Meta.AliasOf != "" {
		t.Errorf("list_engines = %+v", op)
	}
}

// TestOps_StubsAnswer501E2: until E2-OP lands list_engines answers 501 NOT_IMPLEMENTED naming its lane; without the registry
// it answers 503 UNAVAILABLE {component: engines}.
func TestOps_StubsAnswer501E2(t *testing.T) {
	t.Parallel()
	agent := auth.Principal{Kind: auth.KindAgent, ID: "tok-e2", Name: "e2", Scopes: []auth.Scope{auth.ScopeRead}, Via: "token"}
	r := t1Rig(t, api.Deps{Engines: engine.Registry{}})
	if !slices.Contains(implementedE2, "list_engines") {
		rec := r.callWith(agent, "GET", "/api/engines", "")
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		d, _ := m["details"].(map[string]any)
		if rec.Code != 501 || m["code"] != "NOT_IMPLEMENTED" || d["task"] != "E2-OP" {
			t.Errorf("list_engines: %d %v, want 501 NOT_IMPLEMENTED task E2-OP", rec.Code, m)
		}
	}
	r2 := t1Rig(t, api.Deps{})
	rec := r2.callWith(agent, "GET", "/api/engines", "")
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	d, _ := m["details"].(map[string]any)
	if rec.Code != 503 || m["code"] != "UNAVAILABLE" || d["component"] != "engines" {
		t.Errorf("list_engines without the registry: %d %v", rec.Code, m)
	}
}

// TestE2Errors_Shapes: every E2 problem constructor yields its status, code and details names.
func TestE2Errors_Shapes(t *testing.T) {
	t.Parallel()
	rows := []struct {
		err     error
		status  int
		code    string
		details []string
	}{
		{api.ErrEngineCapability("acestep15", "extend", "extend"), 422, "ENGINE_CAPABILITY", []string{"engine", "mode", "capability"}},
		{api.ErrEngineCapability("acestep15", "", "max_seconds"), 422, "ENGINE_CAPABILITY", []string{"engine", "capability"}},
		{api.ErrEngineNotAdopted("acestep15", "metamusic engines adopt acestep15 --reason ..."), 409, "ENGINE_NOT_ADOPTED", []string{"engine", "hint"}},
		{api.ErrBackendUnavailable("acestep15", ""), 422, "BACKEND_UNAVAILABLE", []string{"engine", "backend"}},
		{api.ErrEngineParamsStale("ace0", "ace1"), 422, "ENGINE_PARAMS_STALE", []string{"engine_params_version", "current"}},
		{api.ErrConcurrencyExceedsCap([]string{"a", "b"}, 1, 2), 422, "CONCURRENCY_EXCEEDS_CAP", []string{"backends", "g_micro_per_hour", "x_micro"}},
		{api.ErrInvalidItem(0, "edit.from_s", "range_order"), 422, "INVALID_ITEM", []string{"item", "field", "rule"}},
		{api.ErrReferenceChanged("t1"), 409, "ESTIMATE_STALE", []string{"reason", "track_id"}},
	}
	for _, r := range rows {
		var pe *problem.Error
		if !errors.As(r.err, &pe) {
			t.Errorf("%v is not a *problem.Error", r.err)
			continue
		}
		if pe.Status != r.status || pe.Code != r.code {
			t.Errorf("%s: %d %s, want %d", r.code, pe.Status, pe.Code, r.status)
		}
		for _, k := range r.details {
			if _, ok := pe.Details[k]; !ok {
				t.Errorf("%s lacks details.%s", r.code, k)
			}
		}
		if len(pe.Details) != len(r.details) {
			t.Errorf("%s has %d details, want %v", r.code, len(pe.Details), r.details)
		}
	}
}

// TestSchema_E2Enums: the API enums repeat the engine package's lists (stem names, tasks) and the item mode enum holds the new
// modes.
func TestSchema_E2Enums(t *testing.T) {
	t.Parallel()
	enum := func(typ reflect.Type, field string) []string {
		f, ok := typ.FieldByName(field)
		if !ok {
			t.Fatalf("%s.%s missing", typ, field)
		}
		return splitEnum(f.Tag.Get("enum"))
	}
	if got := enum(reflect.TypeFor[api.EditIn](), "Tracks"); !slices.Equal(got, acestep.StemNames) {
		t.Errorf("EditIn.tracks enum %v, want the engine's stem names %v", got, acestep.StemNames)
	}
	modes := enum(reflect.TypeFor[api.ItemIn](), "Mode")
	for _, m := range []string{"port", "extend", "repaint", "reference", "complete", "stems"} {
		if !slices.Contains(modes, m) {
			t.Errorf("ItemIn.mode lacks %s", m)
		}
	}
	if got := enum(reflect.TypeFor[api.ItemIn](), "Engine"); !slices.Equal(got, []string{"yue2", "acestep15"}) {
		t.Errorf("ItemIn.engine enum %v", got)
	}
	if got := enum(reflect.TypeFor[api.Patch](), "Cot"); !slices.Contains(got, "think") {
		t.Errorf("Patch.cot enum %v lacks think", got)
	}
}

func splitEnum(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == ',' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(c)
	}
	return append(out, cur)
}
