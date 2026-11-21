package mcpgen_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// m6nMandatesCases are the shared parity script's cases for the mandate reads (lane M6N-M1 owns this file alone). The script's rig holds
// no mandate, so the list is empty and the id operations answer the same 404 on every face; TestParity_M6NMandates is the populated half.
var m6nMandatesCases = []ParityCase{
	{Op: "list_mandates", Args: fixedArgs(map[string]any{"limit": 5})},
	{Op: "get_mandate", Args: fixedArgs(map[string]any{"id": "01900000-0000-7000-8000-00000000ffff"}), Status: 404},
	{Op: "revoke_mandate", Args: fixedArgs(map[string]any{"id": "01900000-0000-7000-8000-00000000ffff", "reason": "parity"}), Status: 404},
}

func init() { parityCases = append(parityCases, m6nMandatesCases...) }

const parityMandate = "01900000-0000-7000-8000-00000000dd01"

// seedMandateParity writes one live mandate with a fixed id, straight through SQL, so every face sees the same row.
func seedMandateParity(t *testing.T, r *servertest.Rig) {
	t.Helper()
	now := lt.FixedNow
	if _, err := r.Store.W.Exec(`INSERT INTO mandates(id,holder_key,title,budget_micro_usd,valid_from,valid_until,scope_json,state,created_by,created_at)
		VALUES(?, 'agent:nightshift', 'Night shift', 1000000, ?, ?, ?, 'approved', 'human:alice', ?)`, parityMandate,
		now.Add(-time.Hour).UTC().Format("2006-01-02T15:04:05.000Z"), now.Add(24*time.Hour).UTC().Format("2006-01-02T15:04:05.000Z"),
		`{"modes":["new_seed","style_edit"],"engines":["yue2"],"min_stars":3,"root_track_ids":null,"max_takes_per_lineage":3,"max_takes_per_batch":6,`+
			`"max_batch_ceiling_micro_usd":943587,"max_batches_per_day":1,"tier_b":true}`, now.UTC().Format("2006-01-02T15:04:05.000Z")); err != nil {
		t.Fatal(err)
	}
}

// TestParity_M6NMandates: list, get and revoke answer the same body on REST, the generic MCP route and stdio over a library that holds a
// live mandate; none is a dedicated tool; create_mandate is human-only and absent from the catalogue (call_operation refuses it).
func TestParity_M6NMandates(t *testing.T) {
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
	masks := []string{"by"} // the revoking agent token differs per rig
	steps := []struct {
		op     string
		args   map[string]any
		status int
	}{
		{"list_mandates", map[string]any{"effective_state": []string{"live"}}, 0},
		{"get_mandate", map[string]any{"id": parityMandate}, 0},
		{"list_mandates", map[string]any{"effective_state": []string{"revoked"}}, 0},
		{"revoke_mandate", map[string]any{"id": parityMandate, "reason": "parity"}, 0},
		{"get_mandate", map[string]any{"id": parityMandate}, 0},
		{"revoke_mandate", map[string]any{"id": parityMandate, "reason": "again"}, 409},
		{"list_mandates", map[string]any{"effective_state": []string{"revoked"}}, 0},
	}
	for i, s := range steps {
		op := ops[s.op]
		path, body, headers := restRequest(t, ra.API, op, s.args)
		resp := rest.Do(t, op.Method, path, body, headers...)
		wantStatus := s.status
		if wantStatus == 0 {
			wantStatus = 200
		}
		if resp.Status != wantStatus {
			t.Fatalf("step %d %s: REST %d, want %d: %s", i, s.op, resp.Status, wantStatus, resp.Body)
		}
		want := servertest.Normalize(resp.JSON(t), masks...)
		for name, sess := range faces {
			res, tool := mcpRaw(t, sess, op, s.args, false)
			if tool != "call_operation" {
				t.Errorf("%s answered as the dedicated tool %s", s.op, tool)
			}
			if got := normalizeResult(t, res, op, tool, masks, s.status >= 400); !cmp.Equal(want, got) {
				t.Errorf("step %d %s: REST vs %s (-rest +%s):\n%s", i, s.op, name, name, cmp.Diff(want, got))
			}
		}
	}
	// create_mandate is human-only: no MCP face, and call_operation does not know it
	op := ops["create_mandate"]
	if !op.Meta.HumanOnly || op.Meta.MCP != "exclude:human-only" {
		t.Fatalf("create_mandate meta %+v", op.Meta)
	}
	for name, sess := range faces {
		res, _ := mcpRaw(t, sess, op, map[string]any{}, true)
		if !res.IsError {
			t.Errorf("%s: call_operation ran create_mandate", name)
		}
		tools, err := sess.ListTools(callCtx(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, tl := range tools.Tools {
			if tl.Name == "create_mandate" {
				t.Errorf("%s lists create_mandate", name)
			}
		}
	}
}
