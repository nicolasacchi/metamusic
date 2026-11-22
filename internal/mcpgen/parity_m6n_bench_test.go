package mcpgen_test

import (
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
)

// M6N-B1 (C387): get_critic_bench is generic over MCP (D-M6N-15) and answers the same body on every face.
var benchCases = []ParityCase{
	{Op: "get_critic_bench", Variant: "defaults", Args: fixedArgs(map[string]any{})},
	{Op: "get_critic_bench", Variant: "without the critics that say nothing yet", Args: fixedArgs(map[string]any{"include_unknown": false})},
	{Op: "get_critic_bench", Variant: "unknown critic", Args: fixedArgs(map[string]any{"critic": "nope"}), Status: 404},
}

func init() { parityCases = append(parityCases, benchCases...) }

// TestParity_M6NBench: the operation exists, is generic (never a dedicated tool) and has its cases.
func TestParity_M6NBench(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	found := false
	for _, op := range api.Operations(r.api) {
		if op.ID == "get_critic_bench" {
			found = true
			if op.Meta.MCP != "generic" {
				t.Errorf("get_critic_bench is %q over MCP, want generic", op.Meta.MCP)
			}
		}
	}
	if !found {
		t.Fatal("get_critic_bench is not registered")
	}
	sess := r.client(t, r.token(t, "bench-read", auth.ScopeRead))
	res, err := sess.ListTools(callCtx(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		if tl.Name == "get_critic_bench" {
			t.Error("get_critic_bench is listed as a tool")
		}
	}
	if !slices.ContainsFunc(benchCases, func(c ParityCase) bool { return c.Op == "get_critic_bench" }) {
		t.Error("no parity case")
	}
}
