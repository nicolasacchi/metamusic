package mcpgen_test

import (
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
)

// e2ParityIDs are the E2 operations with an MCP face: list_engines only (the trial and Suite operations are E2c). All are
// generic, none human-only (D-E2-35: no dedicated tool).
var e2ParityIDs = []string{"list_engines"}

// e2Cases are the parity cases of the E2 operations (lane E2-OP owns this list): list_engines answers the same 200 body on
// every face.
var e2Cases = []ParityCase{
	{Op: "list_engines"},
}

func init() { parityCases = append(parityCases, e2Cases...) }

// TestParity_E2Cases: a parity case exists for each E2 operation and for no other; all are generic MCP operations and none is
// human-only.
func TestParity_E2Cases(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	found := 0
	for _, op := range api.Operations(r.api) {
		if !slices.Contains(e2ParityIDs, op.ID) {
			continue
		}
		found++
		if op.Meta.MCP != "generic" || op.Meta.HumanOnly {
			t.Errorf("%s: MCP %q human-only %v, want generic and not human-only", op.ID, op.Meta.MCP, op.Meta.HumanOnly)
		}
	}
	if found != len(e2ParityIDs) {
		t.Errorf("found %d E2 operations, want %d", found, len(e2ParityIDs))
	}
	for _, id := range e2ParityIDs {
		if !slices.ContainsFunc(e2Cases, func(c ParityCase) bool { return c.Op == id }) {
			t.Errorf("no parity case for %s", id)
		}
	}
	for _, c := range e2Cases {
		if !slices.Contains(e2ParityIDs, c.Op) {
			t.Errorf("parity case for %s, which is not an E2 operation", c.Op)
		}
	}
}

// TestToolsList_E2NoTools: list_engines is not a first-class tool.
func TestToolsList_E2NoTools(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sess := r.client(t, r.token(t, "e2-budget", auth.ScopeRead))
	res, err := sess.ListTools(callCtx(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		if slices.Contains(e2ParityIDs, tl.Name) {
			t.Errorf("%s is listed as a tool: E2 adds no dedicated MCP tool", tl.Name)
		}
	}
}
