package mcpgen_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// m6nNightCases are the shared parity script's night-shift reads (lane M6N-N3 owns this file alone). The script's rig holds no mandate and
// the shift is off, so the read answers "no mandate" and the preview "would skip"; TestParity_M6NNight is the populated half.
var m6nNightCases = []ParityCase{
	{Op: "get_night_shift", Args: fixedArgs(map[string]any{})},
	{Op: "preview_night_shift", Args: fixedArgs(map[string]any{})},
	// the halt is idempotent (a repeat keeps the first) and stops the shift, so it runs last; who halted differs per rig
	{Op: "halt_night_shift", Args: fixedArgs(map[string]any{"reason": "parity"}), Masks: []string{"by", "updated_by"}, Last: true},
}

func init() { parityCases = append(parityCases, m6nNightCases...) }

// TestParity_M6NNight: get, preview and halt answer the same body on REST, the generic MCP route and stdio over a library that holds a live
// mandate; none is a dedicated tool; update_night_shift is human-only and absent from the catalogue (call_operation refuses it).
func TestParity_M6NNight(t *testing.T) {
	scopes := []auth.Scope{auth.ScopeRead, auth.ScopeWrite}
	opts := parityRigOptions()
	ra, rb, rc := servertest.New(t, opts), servertest.New(t, opts), servertest.New(t, opts)
	for _, r := range []*servertest.Rig{ra, rb, rc} {
		r.Settle(t)
		seedMandateParity(t, r)
	}
	rest, _ := ra.Agent(t, scopes...)
	faces := map[string]*mcp.ClientSession{"mcp": rb.MCP(t, scopes...), "stdio": rc.Stdio(t, scopes...)}
	ops := map[string]api.OpInfo{}
	for _, op := range api.Operations(ra.API) {
		ops[op.ID] = op
	}
	masks := []string{"by", "updated_by"} // the halting agent token differs per rig
	steps := []struct {
		op   string
		args map[string]any
	}{
		{"get_night_shift", map[string]any{}},
		{"preview_night_shift", map[string]any{}},
		{"halt_night_shift", map[string]any{"reason": "parity"}},
		{"halt_night_shift", map[string]any{"reason": "again"}},
		{"get_night_shift", map[string]any{}},
		{"preview_night_shift", map[string]any{}},
	}
	for i, s := range steps {
		op := ops[s.op]
		path, body, headers := restRequest(t, ra.API, op, s.args)
		resp := rest.Do(t, op.Method, path, body, headers...)
		if resp.Status != 200 {
			t.Fatalf("step %d %s: REST %d: %s", i, s.op, resp.Status, resp.Body)
		}
		want := servertest.Normalize(resp.JSON(t), masks...)
		for name, sess := range faces {
			res, tool := mcpRaw(t, sess, op, s.args, false)
			if tool != "call_operation" {
				t.Errorf("%s answered as the dedicated tool %s", s.op, tool)
			}
			if got := normalizeResult(t, res, op, tool, masks, false); !cmp.Equal(want, got) {
				t.Errorf("step %d %s: REST vs %s (-rest +%s):\n%s", i, s.op, name, name, cmp.Diff(want, got))
			}
		}
	}
	// update_night_shift is human-only: no MCP face, and call_operation does not know it
	op := ops["update_night_shift"]
	if !op.Meta.HumanOnly || op.Meta.MCP != "exclude:human-only" {
		t.Fatalf("update_night_shift meta %+v", op.Meta)
	}
	for name, sess := range faces {
		res, _ := mcpRaw(t, sess, op, map[string]any{"enabled": true}, true)
		if !res.IsError {
			t.Errorf("%s: call_operation ran update_night_shift", name)
		}
		tools, err := sess.ListTools(callCtx(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, tl := range tools.Tools {
			if tl.Name == "update_night_shift" {
				t.Errorf("%s lists update_night_shift", name)
			}
		}
	}
}
